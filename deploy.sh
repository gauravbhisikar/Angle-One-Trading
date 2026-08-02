#!/usr/bin/env bash
# Pull latest code, build engine/contextbuilder-server/agent, (re)start all
# three, and health-check them. Safe to re-run after every `git pull` —
# each run stops whatever it previously started (tracked via .pids/*.pid)
# before starting the freshly-built version, so redeploying an update is
# just: git pull (or let this script do it) && ./deploy.sh again.
#
# Never creates or fetches secrets — .env must already exist in this repo
# root on the server (copy it there once, out of band, before first run).
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_DIR"

LOG_DIR="$REPO_DIR/logs"
PID_DIR="$REPO_DIR/.pids"
mkdir -p "$LOG_DIR" "$PID_DIR"

log() { echo "[deploy] $*"; }

# ---------- 0. secrets must already be in place ----------
if [ ! -f "$REPO_DIR/.env" ]; then
  log "ERROR: .env not found at $REPO_DIR/.env"
  log "This script never creates secrets. Copy your .env onto the server (ANGLE_ONE_*, OPENROUTER_API_KEY, etc.) then rerun."
  exit 1
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
check engine         "http://localhost:${ENGINE_PORT}/strategies"
check contextbuilder  "http://localhost:${CB_PORT}/health"
check agent           "http://localhost:${AGENT_PORT_LOCAL}/health"

if [ "$FAILED" -ne 0 ]; then
  log "one or more services failed — check logs/ before trusting this deploy."
  exit 1
fi

log "deploy OK. Dashboard: http://<this-server-ip>:${ENGINE_PORT}/"
