# AGENTS.md

NIFTYBEES (Indian ETF) AI trading stack: a deterministic Go engine + a Python agent that proposes strategies. All specs live in `docs/` (`DSL_SPEC.md`, `ENGINE_SPEC.md`, `OVERVIEW.md` — trust these over prose).

## Repo shape: 5 independent parts, strict boundary discipline

- `engine/` (Go module `tradingengine`) — parses/validates the Strategy DSL, runs strategies, backtests, Feature Store, HTTP API + built-in dashboard. Port `:9080`.
- `connectors/` (Go module `connectors`) — free external data fetchers. **Does not depend on `engine/`.** Never modifies strategy execution.
- `memory/` (Go module `memory`) — event-sourced persistent store (strategies, contexts, backtests, deployments, trades, lessons). **No dep on `engine/` or `connectors/`.**
- `contextbuilder/` (Go module `contextbuilder`) — aggregates connectors + memory + engine Feature Store into one task-aware snapshot. Port `:9090`. **The only module that knows about the other three.** Uses `replace connectors => ../connectors` and `replace memory => ../memory` — sibling modules must be present when building.
- `agent/` (Python, LangGraph + FastAPI) — turns a contextbuilder snapshot into a DSL strategy, backtests, explains. **Never imports Go; talks to engine + contextbuilder over HTTP only.** Port `:9091`. Never executes trades and never writes raw code — only produces DSL JSON the engine validates and runs.

Boundary rule (deliberate, don't break it): each Go module builds/testable standalone; anything cross-module goes through HTTP or is owned by `contextbuilder/`.

## Build & run

Windows dev: `start.bat` (root) builds `engine.exe` + `contextbuilder-server.exe`, starts the agent venv if present, opens the dashboard.

- engine: `cd engine && go build -o engine.exe ./cmd/engine && ./engine.exe` — reads `.env` from `../.env` (one level above the binary's cwd, hardcoded in `cmd/engine/main.go`), falling back to env vars.
- contextbuilder: `cd contextbuilder && go build -o contextbuilder-server.exe ./cmd/server && ./contextbuilder-server.exe`
- agent: `cd agent && python -m venv venv && pip install -r requirements.txt && python api.py`

Server (Linux) deploy is `deploy.sh` — server-local only, gitignored, never commit it (standing user rule). It builds all three, kills by port, restarts, health-checks.

## Tests

- Each Go module: `cd <module> && go test ./...`
- `connectors` tests hit **real external endpoints** (`go test ./... -v`, ~10s, needs network; flaky sources like NSE can 403).
- `contextbuilder`'s `TestBuildStrategyContext` needs a running engine — skips gracefully if `localhost:9099` isn't up.
- `engine` tests are offline/unit — safe anywhere. Use table tests feeding fixed `OpenTime` candles for entry/exit logic.

## Gotchas

- **Env var name mismatch (verified):** root `.env` uses `ANGLE_ONE_API_KEY`/`ANGLE_ONE_CLIENT_CODE`/`ANGLE_ONE_PIN`/`ANGLE_ONE_TOTP_SECRET`, but `engine/internal/config/config.go` reads `ANGEL_API_KEY`/`ANGEL_CLIENT_ID`/`ANGEL_PIN`/`ANGEL_TOTP_SECRET` — the Angel creds in `.env` are never picked up by the engine. `.env` is gitignored; never commit secrets.
- **Engine defaults to `MockFeed`** (synthetic random-walk ticks, no creds/market hours needed). Angel One live feed exists (`internal/marketdata/angelone`) but is never auto-selected; only enabled via `USE_ANGEL_LIVE=true` and even then needs wiring.
- **Market-hours gate:** entries are hard-blocked outside NSE 09:15–15:30 regardless of DSL `session` window (intentional, `ENGINE_SPEC.md` Sec 6). Testing entry/exit off-hours must feed candles with an `OpenTime` inside market hours — don't rely on the live clock.
- **Backtest data is caller-supplied:** `POST /backtest` runs draft DSL (no need to save a strategy) through the same runtime as live trading, but the engine never fetches candles — get them from `connectors/historical` (Yahoo, 5yr NIFTYBEES).
- **`contextbuilder` `/memory/*` routes:** the request structs have no JSON tags — callers must send exact Go field names (`StrategyID`, not `strategy_id`).
- **Cleanup:** `engine.exe`, `trading.db`, `features.db`, `*.log` in `engine/` are build/test artifacts — remove after manual runs, don't leave them in the tree.
- **Agent without `OPENROUTER_API_KEY`:** every LLM node falls back to a deterministic template and honestly reports `llm_used=False` — the full graph still runs end-to-end.
- **Intraday is never backtested** — no intraday historical dataset exists; backtest results say so plainly rather than fabricating numbers.

## Operation

- Dashboard: `http://localhost:<API_ADDR>/` (default `:9080`). Empty? `POST /dev/seed` loads 3 demo strategies.
- Engine env vars: `SQLITE_PATH` (default `trading.db`), `API_ADDR` (default `:9080`), `STARTING_CAPITAL` (default 100000), `MAX_CONCURRENT_STRATEGIES` (default 10), `MARKET_SESSION_POLL_SECONDS` (default 30).
- Engine auto-pauses running strategies at market close and auto-resumes only the ones it paused (never ones paused manually).
- A `run-engine` skill exists locally at `.claude/skills/run-engine/SKILL.md` — load it when asked to build/test/run the engine.

## Key API surface (engine `:9080`)

`/strategies` (list/CRUD), `/strategies/validate`, `/strategies/{id}/run|pause|resume|stop`, `DELETE /strategies/{id}` (irreversible wipe), `/backtest`, `/features/compute`, `/market/status`, `/dev/seed`, `/system/logs`.