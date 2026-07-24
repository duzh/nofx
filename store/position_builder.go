package store

import (
	"fmt"
	"math"
	"nofx/logger"
	"strings"
	"time"
)

// PositionBuilder handles position creation and updates with support for:
// - Position averaging (merging multiple opens)
// - Partial closes (reducing quantity)
// - FIFO matching
// - Time-ordered processing
type PositionBuilder struct {
	positionStore *PositionStore
}

// NewPositionBuilder creates a new PositionBuilder
func NewPositionBuilder(positionStore *PositionStore) *PositionBuilder {
	return &PositionBuilder{
		positionStore: positionStore,
	}
}

// ProcessTrade processes a single trade and updates position accordingly
// tradeTimeMs is Unix milliseconds UTC
func (pb *PositionBuilder) ProcessTrade(
	traderID, exchangeID, exchangeType, symbol, side, action string,
	quantity, price, fee, realizedPnL float64,
	tradeTimeMs int64,
	orderID string,
) error {
	if strings.HasPrefix(action, "open_") {
		return pb.handleOpen(traderID, exchangeID, exchangeType, symbol, side, quantity, price, fee, tradeTimeMs, orderID)
	} else if strings.HasPrefix(action, "close_") {
		return pb.handleClose(traderID, exchangeID, exchangeType, symbol, side, quantity, price, fee, realizedPnL, tradeTimeMs, orderID)
	}
	return nil
}

// handleOpen handles opening positions (create new or average into existing)
// tradeTimeMs is Unix milliseconds UTC
func (pb *PositionBuilder) handleOpen(
	traderID, exchangeID, exchangeType, symbol, side string,
	quantity, price, fee float64,
	tradeTimeMs int64,
	orderID string,
) error {
	// Get existing OPEN position for (symbol, side)
	existing, err := pb.positionStore.GetOpenPositionBySymbol(traderID, symbol, side)
	if err != nil {
		return fmt.Errorf("failed to get open position: %w", err)
	}

	nowMs := time.Now().UTC().UnixMilli()
	if existing == nil {
		// Create new position
		position := &TraderPosition{
			TraderID:           traderID,
			ExchangeID:         exchangeID,
			ExchangeType:       exchangeType,
			ExchangePositionID: fmt.Sprintf("sync_%s_%s_%d", symbol, side, tradeTimeMs),
			Symbol:             symbol,
			Side:               side,
			Quantity:           quantity,
			EntryPrice:         price,
			EntryOrderID:       orderID,
			EntryTime:          tradeTimeMs,
			Leverage:           1,
			Status:             "OPEN",
			Source:             "sync",
			Fee:                fee,
			CreatedAt:          nowMs,
			UpdatedAt:          nowMs,
		}
		return pb.positionStore.CreateOpenPosition(position)
	}

	// Merge: Calculate weighted average entry price and update position
	logger.Infof("  📊 Averaging position: %s %s %.6f @ %.2f + %.6f @ %.2f",
		symbol, side, existing.Quantity, existing.EntryPrice, quantity, price)

	// Also update exchange_id and exchange_type if they were empty
	if existing.ExchangeID == "" || existing.ExchangeType == "" {
		if err := pb.positionStore.UpdatePositionExchangeInfo(existing.ID, exchangeID, exchangeType); err != nil {
			logger.Infof("  ⚠️  Failed to update exchange info: %v", err)
		}
	}

	return pb.positionStore.UpdatePositionQuantityAndPrice(existing.ID, quantity, price, fee)
}

// handleClose handles closing positions (partial or full)
// tradeTimeMs is Unix milliseconds UTC
func (pb *PositionBuilder) handleClose(
	traderID, exchangeID, exchangeType, symbol, side string,
	quantity, price, fee, realizedPnL float64,
	tradeTimeMs int64,
	orderID string,
) error {
	// Get OPEN position
	position, err := pb.positionStore.GetOpenPositionBySymbol(traderID, symbol, side)
	if err != nil {
		return fmt.Errorf("failed to get open position: %w", err)
	}

	if position == nil {
		// Orphan close: the fill's OPEN row is gone (row lifecycle mishap —
		// e.g. a reconcile pass closed it before this fill arrived). Silently
		// skipping loses the trade's realized PnL from every statistic, so
		// reconstruct the entry from the most recent matching open order and
		// record a CLOSED row directly.
		entryPrice, entryTimeMs := pb.lookupRecentOpenFill(traderID, symbol, side, tradeTimeMs)
		if entryPrice <= 0 {
			logger.Infof("  ⚠️  No matching open position or open order for %s %s (orderID: %s), skipping", symbol, side, orderID)
			return nil
		}
		pnl := realizedPnL
		if pnl == 0 {
			if side == "LONG" {
				pnl = (price - entryPrice) * quantity
			} else {
				pnl = (entryPrice - price) * quantity
			}
			pnl = math.Round(pnl*100) / 100
		}
		if entryTimeMs <= 0 {
			entryTimeMs = tradeTimeMs
		}
		logger.Infof("  🩹 Orphan close healed: %s %s %.6f entry %.6f → exit %.6f, PnL %.2f (orderID: %s)",
			symbol, side, quantity, entryPrice, price, pnl, orderID)
		return pb.positionStore.Create(&TraderPosition{
			TraderID:      traderID,
			ExchangeID:    exchangeID,
			ExchangeType:  exchangeType,
			Symbol:        symbol,
			Side:          side,
			Quantity:      0,
			EntryQuantity: quantity,
			EntryPrice:    entryPrice,
			ExitPrice:     price,
			RealizedPnL:   pnl,
			Fee:           fee,
			Status:        "CLOSED",
			CloseReason:   "orphan_heal",
			EntryTime:     entryTimeMs,
			ExitTime:      tradeTimeMs,
			ExitOrderID:   orderID,
		})
	}

	const QUANTITY_TOLERANCE = 0.0001

	// Calculate realized PnL if not provided (some exchanges like Lighter don't return it)
	if realizedPnL == 0 && position.EntryPrice > 0 {
		if side == "LONG" {
			realizedPnL = (price - position.EntryPrice) * quantity
		} else {
			realizedPnL = (position.EntryPrice - price) * quantity
		}
		// Round to 2 decimal places
		realizedPnL = math.Round(realizedPnL*100) / 100
	}

	if quantity < position.Quantity-QUANTITY_TOLERANCE {
		// Partial close: reduce quantity and update weighted average exit price
		logger.Infof("  📉 Partial close: %s %s %.6f → %.6f (closed %.6f @ %.2f, PnL: %.2f)",
			symbol, side, position.Quantity, position.Quantity-quantity, quantity, price, realizedPnL)
		return pb.positionStore.ReducePositionQuantity(position.ID, quantity, price, fee, realizedPnL)
	} else {
		// Full close (or close with tolerance): mark as CLOSED
		closeQty := quantity
		if quantity > position.Quantity {
			logger.Infof("  ⚠️  Over-close detected: %s %s trying to close %.6f but only %.6f open, closing full position",
				symbol, side, quantity, position.Quantity)
			closeQty = position.Quantity
		}

		// Calculate final weighted average exit price
		// Include previously accumulated partial close prices + this final close
		closedBefore := position.EntryQuantity - position.Quantity
		totalClosed := closedBefore + closeQty
		var finalExitPrice float64
		if totalClosed > 0 {
			finalExitPrice = (position.ExitPrice*closedBefore + price*closeQty) / totalClosed
			// Use adaptive precision based on price magnitude (for meme coins with very small prices)
			finalExitPrice = adaptivePriceRound(finalExitPrice, position.ExitPrice, price, position.EntryPrice)
		} else {
			finalExitPrice = price
		}

		// Calculate total PnL (existing + new)
		totalPnL := position.RealizedPnL + realizedPnL

		// Calculate total fee (existing + new)
		totalFee := position.Fee + fee

		logger.Infof("  ✅ Full close: %s %s %.6f @ %.2f (avg exit: %.2f, entry: %.2f, PnL: %.2f)",
			symbol, side, closeQty, price, finalExitPrice, position.EntryPrice, totalPnL)

		return pb.positionStore.ClosePositionFully(
			position.ID,
			finalExitPrice,
			orderID,
			tradeTimeMs,
			totalPnL,
			totalFee,
			"sync",
		)
	}
}

// quantitiesMatch checks if two quantities are close enough (within tolerance)
func quantitiesMatch(a, b float64) bool {
	const QUANTITY_TOLERANCE = 0.0001
	return math.Abs(a-b) < QUANTITY_TOLERANCE
}

// lookupRecentOpenFill finds the most recent open fill for symbol+side before
// closeTimeMs in the orders ledger. Used to heal orphan closes whose OPEN
// position row was lost, so their realized PnL still reaches the statistics.
func (pb *PositionBuilder) lookupRecentOpenFill(traderID, symbol, side string, closeTimeMs int64) (float64, int64) {
	action := "open_long"
	if side == "SHORT" {
		action = "open_short"
	}
	var row struct {
		AvgFillPrice float64
		Price        float64
		CreatedAt    int64
	}
	err := pb.positionStore.db.Table("trader_orders").
		Select("avg_fill_price, price, created_at").
		Where("trader_id = ? AND symbol = ? AND order_action = ? AND created_at <= ?", traderID, symbol, action, closeTimeMs).
		Order("created_at DESC").
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return 0, 0
	}
	price := row.AvgFillPrice
	if price <= 0 {
		price = row.Price
	}
	return price, row.CreatedAt
}
