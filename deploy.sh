#!/usr/bin/env bash
# Full end-to-end deploy: install missing system deps, create .env
# interactively on first run, pull latest code, build, (re)start all
# three services, health-check. Safe to re-run after every `git pull` —
# each run stops whatever it previously started (tracked via .pids/*.pid)
# before starting the freshly-built version, so redeploying an update is
# just: ./deploy.sh again.
#
# .env is NEVER hardcoded in this script (it's committed to a public
# repo) — first run prompts for real credentials interactively and
# writes them straight to .env on THIS machine only, chmod 600. That
# file is gitignored and never leaves this server via git.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_DIR"

LOG_DIR="$REPO_DIR/logs"
PID_DIR="$REPO_DIR/.pids"
mkdir -p "$LOG_DIR" "$PID_DIR"

log() { echo "[deploy] $*"; }

# ---------- 0a. system prerequisites ----------
SUDO=""
if [ "$(id -u)" -ne 0 ]; then SUDO="sudo"; fi

need_install=()
command -v git >/dev/null 2>&1 || need_install+=(git)
command -v curl >/dev/null 2>&1 || need_install+=(curl)
command -v python3 >/dev/null 2>&1 || need_install+=(python3)
python3 -c "import venv" >/dev/null 2>&1 || need_install+=(python3-venv)
command -v pip3 >/dev/null 2>&1 || need_install+=(python3-pip)

if [ "${#need_install[@]}" -gt 0 ]; then
  if command -v apt-get >/dev/null 2>&1; then
    log "installing missing packages via apt: ${need_install[*]}"
    $SUDO apt-get update -qq
    $SUDO apt-get install -y -qq "${need_install[@]}"
  else
    log "ERROR: missing ${need_install[*]} and this isn't an apt-based system."
    log "Install them manually with your distro's package manager, then rerun."
    exit 1
  fi
fi

if ! command -v go >/dev/null 2>&1; then
  log "Go not found — installing go1.23.4 (bootstrap only; this repo's go.mod may pin a"
  log "newer version, which Go's own toolchain manager auto-downloads on first build)."
  GO_TARBALL="go1.23.4.linux-$(dpkg --print-architecture 2>/dev/null || uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz"
  curl -fsSL "https://go.dev/dl/${GO_TARBALL}" -o "/tmp/${GO_TARBALL}"
  $SUDO rm -rf /usr/local/go
  $SUDO tar -C /usr/local -xzf "/tmp/${GO_TARBALL}"
  rm -f "/tmp/${GO_TARBALL}"
  export PATH="/usr/local/go/bin:$PATH"
  if ! grep -q '/usr/local/go/bin' "$HOME/.profile" 2>/dev/null; then
    echo 'export PATH=/usr/local/go/bin:$PATH' >> "$HOME/.profile"
    log "added /usr/local/go/bin to PATH in ~/.profile for future logins"
  fi
fi
log "go: $(go version)"

# ---------- 0b. .env — create interactively on first run, never hardcoded here ----------
if [ ! -f "$REPO_DIR/.env" ]; then
  log "No .env found — first-time setup. Values are written to .env on THIS machine only,"
  log "never committed (it's gitignored) and never sent anywhere by this script."
  read -rp "ANGLE_ONE_API_KEY: " IN_ANGLE_API_KEY
  read -rp "ANGLE_ONE_CLIENT_CODE: " IN_ANGLE_CLIENT_CODE
  read -rsp "ANGLE_ONE_PIN (hidden): " IN_ANGLE_PIN; echo
  read -rsp "ANGLE_ONE_TOTP_SECRET (hidden): " IN_ANGLE_TOTP; echo
  read -rsp "OPENROUTER_API_KEY (hidden, blank OK — no LLM = deterministic fallback): " IN_OPENROUTER_KEY; echo
  read -rp "OPENROUTER_MODEL [deepseek/deepseek-v4-pro]: " IN_OPENROUTER_MODEL
  IN_OPENROUTER_MODEL="${IN_OPENROUTER_MODEL:-deepseek/deepseek-v4-pro}"

  cat > "$REPO_DIR/.env" <<ENV_EOF
ANGLE_ONE_API_KEY=${IN_ANGLE_API_KEY}
ANGLE_ONE_CLIENT_CODE=${IN_ANGLE_CLIENT_CODE}
ANGLE_ONE_PIN=${IN_ANGLE_PIN}
ANGLE_ONE_TOTP_SECRET=${IN_ANGLE_TOTP}
OPENROUTER_API_KEY=${IN_OPENROUTER_KEY}
OPENROUTER_MODEL=${IN_OPENROUTER_MODEL}
ENGINE_URL=http://localhost:9080
CONTEXTBUILDER_URL=http://localhost:9090
AGENT_PORT=9091
ENV_EOF
  chmod 600 "$REPO_DIR/.env"
  log ".env created (chmod 600, readable only by this user)."
fi

set -a
# shellcheck disable=SC1091
source "$REPO_DIR/.env"
set +a

# ---------- 1. pull latest code ----------
if [ -d "$REPO_DIR/.git" ]; then
  log "git pull..."
  git pull --ff-only
else
  log "ERROR: $REPO_DIR is not a git checkout. Clone the repo first:"
  log "  git clone https://github.com/gauravbhisikar/Angle-One-Trading.git"
  exit 1
fi

# ---------- 2. stop whatever this script previously started ----------
stop_by_pidfile() {
  local pidfile="$1"
  if [ -f "$pidfile" ]; then
    local pid
    pid="$(cat "$pidfile")"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      log "stopping pid $pid ($(basename "$pidfile" .pid))"
      kill "$pid" 2>/dev/null || true
      sleep 1
      kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$pidfile"
  fi
}
stop_by_pidfile "$PID_DIR/engine.pid"
stop_by_pidfile "$PID_DIR/contextbuilder.pid"
stop_by_pidfile "$PID_DIR/agent.pid"

# ---------- 3. build ----------
log "building engine..."
( cd "$REPO_DIR/engine" && go build -o engine ./cmd/engine )

log "building contextbuilder-server..."
( cd "$REPO_DIR/contextbuilder" && go build -o contextbuilder-server ./cmd/server )

log "setting up agent venv..."
if [ ! -d "$REPO_DIR/agent/venv" ]; then
  python3 -m venv "$REPO_DIR/agent/venv"
fi
"$REPO_DIR/agent/venv/bin/pip" install -q -r "$REPO_DIR/agent/requirements.txt"

# ---------- 4. start ----------
log "starting engine..."
( cd "$REPO_DIR/engine" && nohup ./engine >"$LOG_DIR/engine.log" 2>&1 & echo $! >"$PID_DIR/engine.pid" )

log "starting contextbuilder-server..."
( cd "$REPO_DIR/contextbuilder" && nohup ./contextbuilder-server >"$LOG_DIR/contextbuilder.log" 2>&1 & echo $! >"$PID_DIR/contextbuilder.pid" )

log "starting agent..."
( cd "$REPO_DIR/agent" && nohup venv/bin/python api.py >"$LOG_DIR/agent.log" 2>&1 & echo $! >"$PID_DIR/agent.pid" )

sleep 3

# ---------- 5. health check ----------
ENGINE_PORT="${API_ADDR:-:9080}"; ENGINE_PORT="${ENGINE_PORT#:}"
CB_PORT="${CONTEXTBUILDER_PORT:-9090}"
AGENT_PORT_LOCAL="${AGENT_PORT:-9091}"

FAILED=0
check() {
  local name="$1" url="$2"
  if curl -sf -o /dev/null "$url"; then
    log "OK   $name  ($url)"
  else
    log "FAIL $name  ($url) — see logs/$name.log"
    FAILED=1
  fi
}
log "health check..."
check engine          "http://localhost:${ENGINE_PORT}/strategies"
check contextbuilder   "http://localhost:${CB_PORT}/health"
check agent            "http://localhost:${AGENT_PORT_LOCAL}/health"

if [ "$FAILED" -ne 0 ]; then
  log "one or more services failed — check logs/ before trusting this deploy."
  exit 1
fi

log "deploy OK. Dashboard: http://<this-server-ip>:${ENGINE_PORT}/"
