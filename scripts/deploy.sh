#!/usr/bin/env bash
# NOFX file deploy (no docker) — builds the frontend locally, syncs the repo
# to the server over SSH (password auth via sshpass), builds the Go binary on
# the server, and runs it as a systemd service on the port the old docker
# frontend used, so the existing domain nginx proxy keeps working unchanged.
#
# Credentials & target live in .env.deploy (gitignored — NEVER commit it).
#   cp .env.deploy.example .env.deploy   # then fill in your server info
#   ./scripts/deploy.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$REPO_ROOT/.env.deploy"

# The Go binary serves BOTH the API and the built frontend. Default 3000 =
# the host port the old nofx-frontend container published, so the domain's
# nginx proxy target stays valid. Override with DEPLOY_APP_PORT in .env.deploy.
APP_PORT_DEFAULT=3000
SERVICE_NAME=nofx

# ---------- Load deploy config ----------
if [[ ! -f "$ENV_FILE" ]]; then
    echo "❌ Missing $ENV_FILE  (cp .env.deploy.example .env.deploy)"
    exit 1
fi
# shellcheck disable=SC1090
source "$ENV_FILE"

: "${DEPLOY_HOST:?DEPLOY_HOST is required in .env.deploy}"
: "${DEPLOY_USER:?DEPLOY_USER is required in .env.deploy}"
: "${DEPLOY_PASS:?DEPLOY_PASS is required in .env.deploy}"
: "${DEPLOY_PATH:?DEPLOY_PATH is required in .env.deploy}"
DEPLOY_PORT="${DEPLOY_PORT:-22}"
APP_PORT="${DEPLOY_APP_PORT:-$APP_PORT_DEFAULT}"

# ---------- Preflight (local) ----------
for tool in sshpass rsync npm; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "❌ $tool not installed locally"
        [[ "$tool" == "sshpass" ]] && echo "   macOS: brew install esolitos/ipa/sshpass"
        exit 1
    fi
done

SSH_OPTS=(-p "$DEPLOY_PORT" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)
run_remote() {
    sshpass -p "$DEPLOY_PASS" ssh "${SSH_OPTS[@]}" "$DEPLOY_USER@$DEPLOY_HOST" "$@"
}

echo "🔌 Testing SSH connection to $DEPLOY_USER@$DEPLOY_HOST:$DEPLOY_PORT ..."
run_remote "echo ok" >/dev/null
echo "✅ SSH connection OK"

# ---------- Build frontend locally ----------
echo "🏗  Building frontend (vite) ..."
(cd "$REPO_ROOT/web" && { [[ -d node_modules ]] || npm install; } && npm run build)
[[ -f "$REPO_ROOT/web/dist/index.html" ]] || { echo "❌ web/dist/index.html missing after build"; exit 1; }
echo "✅ Frontend built"

# ---------- Sync code + dist ----------
echo "📦 Syncing repo to $DEPLOY_HOST:$DEPLOY_PATH ..."
run_remote "mkdir -p '$DEPLOY_PATH'"
# Excluded paths are also protected from --delete, so server-side runtime
# state (.env with live keys, data/ with the SQLite DB) survives deploys.
sshpass -p "$DEPLOY_PASS" rsync -az --delete \
    -e "ssh -p $DEPLOY_PORT -o StrictHostKeyChecking=accept-new" \
    --exclude '.git/' \
    --exclude 'node_modules/' \
    --exclude 'web/node_modules/' \
    --exclude 'data/' \
    --exclude '.env' \
    --exclude '.env.deploy' \
    --exclude '*.db' \
    --exclude 'nofx-server' \
    "$REPO_ROOT/" "$DEPLOY_USER@$DEPLOY_HOST:$DEPLOY_PATH/"
echo "✅ Code + frontend synced"

# ---------- Ensure Go toolchain on server ----------
# go.mod requires go >= 1.25; distro/panel Go installs are often ancient, so
# manage our own copy under /usr/local/go and always build with that binary.
echo "🔍 Checking Go on the server ..."
GO_VER="1.25.11"
GO_BIN="/usr/local/go/bin/go"
if ! run_remote "test -x $GO_BIN && $GO_BIN version | grep -q 'go1\.2[5-9]'"; then
    echo "⬇️  Installing Go $GO_VER on the server ..."
    run_remote "curl -fsSL https://go.dev/dl/go${GO_VER}.linux-amd64.tar.gz -o /tmp/go.tgz && rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz && rm /tmp/go.tgz"
fi
GO_CMD="$GO_BIN"

# ---------- Build backend on server (CGO needed by sqlite driver) ----------
echo "🔨 Building backend on the server (first build can take several minutes) ..."
run_remote "cd '$DEPLOY_PATH' && $GO_CMD build -o nofx-server ."
echo "✅ Backend built"

# ---------- Ensure server .env exists ----------
if ! run_remote "test -f '$DEPLOY_PATH/.env'"; then
    echo "⚠️  No .env on the server — creating from .env.example."
    echo "   Edit $DEPLOY_PATH/.env (JWT_SECRET etc.) before real use!"
    run_remote "cp '$DEPLOY_PATH/.env.example' '$DEPLOY_PATH/.env'"
fi

# ---------- Stop old docker deployment (frees the app port) ----------
echo "🛑 Stopping old docker deployment (if present) ..."
run_remote "cd '$DEPLOY_PATH' && (docker compose down 2>/dev/null || docker-compose down 2>/dev/null || true)"

# ---------- Install / refresh systemd service ----------
echo "⚙️  Installing systemd service ($SERVICE_NAME, port $APP_PORT) ..."
run_remote "cat > /etc/systemd/system/${SERVICE_NAME}.service <<UNIT
[Unit]
Description=NOFX AI trading terminal (file deploy)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$DEPLOY_PATH
ExecStart=$DEPLOY_PATH/nofx-server
Environment=API_SERVER_PORT=$APP_PORT
Environment=API_SERVER_HOST=127.0.0.1
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload && systemctl enable $SERVICE_NAME && systemctl restart $SERVICE_NAME"

# ---------- Health check ----------
echo "🏥 Waiting for health check on port $APP_PORT ..."
for i in $(seq 1 24); do
    if run_remote "curl -fsS http://127.0.0.1:$APP_PORT/api/health >/dev/null 2>&1"; then
        echo "✅ Deploy complete — service healthy on port $APP_PORT"
        run_remote "systemctl status $SERVICE_NAME --no-pager -l | head -12"
        exit 0
    fi
    sleep 5
done

echo "❌ Health check failed after 120s. Recent logs:"
run_remote "journalctl -u $SERVICE_NAME --no-pager -n 50"
exit 1
