import { useEffect, useState } from 'react'
import { api } from '../../lib/api'

/**
 * OrderFlow renders the raw order/fill history for a trader — every open,
 * close, stop-loss and take-profit order the account executed, newest first.
 * Self-fetching (silent) so failures degrade to an empty state, refreshed on
 * a slow poll; complements ExecutionLog, which shows AI decisions per cycle.
 */

interface TraderOrder {
  id: number
  symbol: string
  side: string
  order_action: string
  quantity: number
  price: number
  avg_fill_price: number
  commission: number
  status: string
  created_at: number
}

const REFRESH_MS = 30_000

function fmtTime(ms: number): string {
  const d = new Date(ms)
  if (Number.isNaN(d.getTime())) return '--'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

function baseSymbol(raw: string): string {
  return raw.toUpperCase().replace(/^XYZ:/, '').replace(/(USDT|USDC|USD)$/, '')
}

function actionColor(action: string): string {
  const a = action.toLowerCase()
  if (a === 'open_long' || a === 'close_short') return 'var(--tm-up)'
  if (a === 'open_short' || a === 'close_long') return 'var(--tm-dn)'
  return 'var(--tm-muted)'
}

export function OrderFlow({ traderId, height = 260 }: { traderId?: string; height?: number }) {
  const [orders, setOrders] = useState<TraderOrder[]>([])

  useEffect(() => {
    if (!traderId) return
    let alive = true
    const load = async () => {
      try {
        const list = await api.getOrders(traderId, 80, true)
        if (alive && Array.isArray(list)) setOrders(list)
      } catch {
        /* silent — panel keeps last known data */
      }
    }
    load()
    const id = setInterval(load, REFRESH_MS)
    return () => {
      alive = false
      clearInterval(id)
    }
  }, [traderId])

  const sorted = [...orders].sort((a, b) => b.created_at - a.created_at)

  return (
    <div style={{ fontFamily: 'var(--tm-mono)' }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 2 }}>
        <span className="tm-px" style={{ fontSize: 11 }}>Order flow</span>
        <span className="tm-sc" style={{ marginLeft: 'auto', color: sorted.length ? 'var(--tm-up)' : 'var(--tm-muted)' }}>
          {sorted.length ? `${sorted.length} fills` : '—'}
        </span>
      </div>
      <div className="tm-sc" style={{ fontSize: 9, marginBottom: 5 }}>
        Every executed order · opens, closes, stops · newest first
      </div>
      <div className="tm-hair" />
      {!sorted.length ? (
        <div className="tm-sc" style={{ padding: '12px 0' }}>No orders yet.</div>
      ) : (
        <div style={{ height, overflowY: 'auto', fontSize: 10, lineHeight: 1.6, fontVariantNumeric: 'tabular-nums' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead>
              <tr className="tm-sc" style={{ fontSize: 9, textAlign: 'left', color: 'var(--tm-muted)' }}>
                <th style={{ padding: '3px 6px', fontWeight: 400 }}>time</th>
                <th style={{ padding: '3px 6px', fontWeight: 400 }}>action</th>
                <th style={{ padding: '3px 6px', fontWeight: 400 }}>symbol</th>
                <th style={{ padding: '3px 6px', fontWeight: 400, textAlign: 'right' }}>qty</th>
                <th style={{ padding: '3px 6px', fontWeight: 400, textAlign: 'right' }}>price</th>
                <th style={{ padding: '3px 6px', fontWeight: 400, textAlign: 'right' }}>fee</th>
                <th style={{ padding: '3px 6px', fontWeight: 400 }}>status</th>
              </tr>
            </thead>
            <tbody>
              {sorted.map((o) => {
                const price = o.avg_fill_price > 0 ? o.avg_fill_price : o.price
                const c = actionColor(o.order_action)
                return (
                  <tr key={o.id} style={{ borderTop: '1px solid var(--tm-hair)', color: 'var(--tm-ink-2)' }}>
                    <td style={{ padding: '2px 6px', color: 'var(--tm-muted)', whiteSpace: 'nowrap' }}>{fmtTime(o.created_at)}</td>
                    <td style={{ padding: '2px 6px', color: c, whiteSpace: 'nowrap' }}>{o.order_action || o.side.toLowerCase()}</td>
                    <td style={{ padding: '2px 6px', color: 'var(--tm-ink)', fontWeight: 600 }}>{baseSymbol(o.symbol)}</td>
                    <td style={{ padding: '2px 6px', textAlign: 'right' }}>{o.quantity}</td>
                    <td style={{ padding: '2px 6px', textAlign: 'right' }}>{price > 0 ? price.toLocaleString(undefined, { maximumFractionDigits: 6 }) : '—'}</td>
                    <td style={{ padding: '2px 6px', textAlign: 'right', color: 'var(--tm-muted)' }}>{o.commission ? o.commission.toFixed(4) : '—'}</td>
                    <td style={{ padding: '2px 6px', color: 'var(--tm-muted)' }}>{o.status.toLowerCase()}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

export default OrderFlow
