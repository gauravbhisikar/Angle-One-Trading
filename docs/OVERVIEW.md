# Overview

High-level map of this project's parts, with every source file. Each part is independently buildable/testable; deeper detail lives in each folder's own README/BACKLOG.

## `engine/`
The deterministic trading engine (Go module `tradingengine`). Parses and validates the Strategy DSL, runs strategies live/paper (SQLite-backed, HTTP API + built-in dashboard), backtests them against historical candles using that same execution code, and computes/stores daily technical indicators (Feature Store) — all without any AI involved.

- **`cmd/engine/main.go`** — entrypoint; wires config, storage, feature store, scheduler, and the HTTP server together.
- **`internal/dsl/`** — the Strategy DSL itself.
  - `schema.go` — Go structs for the DSL JSON shape (entry/exit/risk/holding/etc).
  - `condition.go` — the recursive `all`/`any`/`not` condition tree evaluator.
  - `registry_meta.go` — canonical indicator → allowed-operator table.
  - `validate.go` — DSL_SPEC Sec 23 validation rules.
- **`internal/indicators/`** — every technical indicator, computed incrementally (streaming, not recomputed per candle).
  - `indicator.go` — the `Indicator` interface and registry.
  - `cache.go` — shared cache so 10 strategies using the same indicator share one instance.
  - `ema.go`, `sma.go`, `momentum.go` (RSI/StochRSI/ROC/CCI/MFI), `trend.go` (ATR/ADX/SuperTrend), `volume.go` (volume spike/OBV), `channel.go` (Donchian/highest-high/lowest-low/support/resistance), `price.go` (OHLC/prev-high-low/gap), `pattern.go` (candlestick patterns), `vwap.go`, `ring.go` (shared rolling-window buffer).
- **`internal/strategy/`** — the live/backtest execution runtime.
  - `runtime.go` — subscribes indicators, drives the candle-close event loop.
  - `lifecycle.go` — entry/exit/holding/exit-priority logic, market-hours gate.
  - `resolver.go` — binds a DSL condition tree to live indicator values.
  - `extract.go` — pulls take-profit/stop-loss shorthand out of a condition tree.
- **`internal/backtest/runner.go`** — replays historical candles through the exact same strategy runtime as live trading (no separate simulator).
- **`internal/featurestore/`** — daily computed features (RSI/EMA/ADX/MACD/Bollinger) persisted per symbol, reusing `internal/indicators` directly.
  - `compute.go`, `schema.go`, `store.go`.
- **`internal/marketdata/`** — tick → candle pipeline.
  - `pipeline.go`, `builder.go`, `feed.go`, `mockfeed.go` (synthetic feed for local dev).
  - `angelone/` — Angel One SmartAPI client (`client.go`, `totp.go`, `ws.go`) for live broker data.
- **`internal/execution/`** — order placement and position sizing.
  - `broker.go` (interface), `paper.go` (simulated fills), `angelone.go` (live broker adapter), `sizing.go` (lot/tick/qty rules).
- **`internal/portfolio/`** — money.
  - `ledger.go` — cash/position tracking.
  - `cost/` — brokerage + statutory tax model (`model.go`, `presets.go`, `statutory.go`).
- **`internal/risk/`** — `risk.go` (per-strategy limits), `portfolio_guard.go` (cross-strategy exposure caps).
- **`internal/scheduler/engine.go`** — runs multiple strategies concurrently, owns the shared candle pipeline.
- **`internal/marketsession/`** — `status.go` (NSE open/closed classification, shared by the API and the monitor), `monitor.go` (auto-pauses running strategies at market close, auto-resumes only the ones it paused at open, logs every transition).
- **`internal/storage/`** — SQLite persistence for strategies/orders/trades/logs/reviews/predicted-metrics (`db.go` + one `repo_*.go` per entity — `repo_predicted_metrics.go` holds what a backtest predicted at deploy time, for later comparison against real performance).
- **`internal/analytics/`** — `metrics.go` (Sharpe/Sortino/CAGR/drawdown/etc), `review.go` (AI Review JSON shape).
- **`internal/models/`** — shared types: `candle.go`, `domain.go` (Order/Trade), `enums.go`.
- **`internal/config/config.go`** — env/`.env` loading.
- **`internal/api/`** — the HTTP surface.
  - `server.go` — routes, including `DELETE /strategies/{id}` (stops the runtime if running, then wipes every version/order/trade/log/review/predicted-metrics row — irreversible) and `POST`/`GET /strategies/{id}/predicted-metrics` (what a backtest predicted at deploy time, so the dashboard can compare it against real live performance later).
  - `dashboard.go` / `dashboard_data.go` — the built-in web dashboard and its data endpoints.
  - `backtest.go`, `features.go`, `market.go` (market status, delegates to `internal/marketsession`), `system.go` (`GET /system/logs` — engine-wide events), `seed.go` (demo data), `util.go`.

## `connectors/`
Standalone Go module. Free external data fetchers, each independently live-tested against its real source.

- **`yahoo/yahoo.go`** — price/quote/candles for NIFTYBEES, NIFTY 50, India VIX, global cues (Dow/Nasdaq/crude/USD-INR).
- **`angelone/`** — `scripmaster.go` (free instrument master: lot size/tick size/expiry), `client.go` (authenticated REST client), `totp.go`, `optionchain.go` (OI/PCR/max pain), `greeks.go` (IV/Delta/Gamma/Theta/Vega), `futures.go` (NIFTY futures + basis).
- **`amfi/nav.go`** — official NIFTYBEES NAV from AMFI's daily flat file.
- **`nse/nse.go`** — holidays, corporate actions/announcements, FII/DII flow, market breadth (advance/decline, 52-week highs/lows).
- **`news/news.go`** — RSS headlines (Economic Times, Moneycontrol).
- **`sentiment/`** — `lexicon.go` (finance-tuned word list), `score.go` (scorer), `news.go` (batch-scores headlines).
- **`rbi/rbi.go`** — RBI policy-announcement RSS.
- **`overnight/overnight.go`** — GIFT Nifty → futures-basis → US-markets cascade with confidence per rung.
- **`tri/tri.go`** — NIFTY 50 Total Return Index, CSV-drop import (no free live API found).
- **`historical/historical.go`** — 5-year daily OHLCV sync/refresh, used for backtesting.
- **`webreader/`** — `jina.go` (Jina Reader), `reader.go` (pluggable `Reader` interface + fallback).
- **`global/`** — global market context, reduced to a disclosed composite rather than raw quotes: `us.go`/`asia.go`/`commodities.go`/`currencies.go` (S&P 500/VIX/Dow/Nasdaq, Nikkei/Hang Seng/Shanghai, Gold/Silver/Crude, USD/INR/DXY), `session.go` (which markets are open right now), `events.go` (keyword-filtered global-relevant headlines — raw, never an AI-style "impact score" fabricated in Go), `risk.go` (the `risk_mode`/`confidence` composite formula, disclosed via `Context.Basis`).
- **`store/store.go`** — shared SQLite cache with per-source retention/auto-prune.

## `memory/`
Standalone Go module. Persistent, event-sourced learning store.

- **`schema.go`** — SQLite schema: `events` (immutable log) plus derived tables.
- **`events.go`** — event type vocabulary + append helper.
- **`strategy.go`** — versioned strategy lineage, `GetSuccessfulStrategies`/`GetFailedStrategies`.
- **`context.go`** — market-context snapshot at the moment a strategy was created.
- **`backtest.go`** — backtest results per strategy version.
- **`deployment.go`** — paper/live deployment lifecycle.
- **`trade.go`** — trade open→close records.
- **`snapshot.go`** — daily portfolio snapshots.
- **`review.go`** — human-readable strengths/weaknesses/recommendations.
- **`lesson.go`** — aggregated "lesson learned" confidence scores.

## `contextbuilder/`
Standalone Go module. The Decision Context Engine — aggregates connectors + memory + the engine's Feature Store into one task-aware snapshot.

- **`types.go`** — `DecisionContext` and all its sub-sections (Market/Portfolio/StrategyMemory/PaperTrading/Lessons/Regime/Recommendations).
- **`provider.go`** — `ContextProvider` interface, `Builder`, and the task → sections map.
- **`engineclient.go`** — thin HTTP client for the engine's own API (portfolio, trades, features).
- **`market_provider.go`**, **`global_provider.go`**, **`portfolio_provider.go`**, **`memory_provider.go`** — one provider per data source. `global_provider.go` wraps `connectors/global` into `DecisionContext.GlobalMarket`.
- **`regime_provider.go`** — rule-based bull/bear/sideways classification (disclosed formula).
- **`recommendation_provider.go`** — regime playbook + lesson-based overrides.
- **`research.go`** — curated-feed research: keyword-matches `connectors/news`/`connectors/rbi` headlines, fetches full bodies via `connectors/webreader`. Not general web search — no discovery-search API is wired in.
- **`server.go`**, **`memory_handlers.go`** — the HTTP surface (`cmd/server/main.go` entrypoint, default `:9090`): `POST /context/build`, `POST /research/query`, `GET /health`, and `POST /memory/{strategy,context,backtest,deployment,lesson}` + `GET /memory/lessons` — the sole gateway the Python agent has into `memory/`, since it can't import Go code.

## `agent/`
Python (LangGraph) module — built. Turns a `contextbuilder-server` context snapshot into a NIFTYBEES DSL strategy, backtests it, and explains why. Never executes trades or writes raw code; only produces DSL JSON the engine validates/runs. Talks to the engine and contextbuilder-server over HTTP only — see `agent/README.md` for the full graph shape.

- **`state.py`** — `AgentState`/`Candidate` TypedDicts.
- **`llm.py`** — OpenRouter client (`OPENROUTER_API_KEY`/`OPENROUTER_MODEL` in root `.env`), `None` if unconfigured; `invoke_structured` retries a structured call before falling back.
- **`clients.py`** — thin HTTP clients for the engine + contextbuilder-server.
- **`graph.py`** — the LangGraph `StateGraph` wiring all nodes; `run_agent(style)` entrypoint.
- **`api.py`** — FastAPI wrapper (default `:9091`): `POST /generate`, `POST /deploy`.
- **`nodes/`** — `gather_context.py`, `plan.py` (the one real LLM judgment call — archetype/risk/holding from a fixed menu), `research.py`, `generate_dsl.py`, `templates.py` (deterministic DSL assembly, ported from the Strategy Lab's old client-side generator), `validate.py`, `backtest.py`, `rank.py`, `self_review.py` (LLM explanation grounded in a code-derived evidence checklist), `memory_update.py`, `schemas.py` (Pydantic structured-output shapes).

## `docs/`
Specifications, not code.

- **`DSL_SPEC.md`** — the strategy language the AI writes and the engine executes.
- **`ENGINE_SPEC.md`** — India-specific execution rules (lot sizes, costs, market hours, backtesting, feature store) the engine enforces.
- **`OVERVIEW.md`** — this file.

## `start.bat`
One-click Windows launcher: builds the engine, starts it, and opens the dashboard in a browser.
