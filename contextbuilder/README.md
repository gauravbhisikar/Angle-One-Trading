# Context Builder (Decision Context Engine)

Standalone Go module. Answers one question: **"given this task, what does the AI need right now?"** — task-aware, not a dump of every data point into every request. Ties together the three services built before it: `connectors/` (external market data), `memory/` (persistent learning), and `engine/` (portfolio state, feature store). None of those three know about each other; this is the first thing that does.

## Why now, not earlier

Built last, deliberately — it has nothing to aggregate until Connectors, Memory, Feature Store, Backtest, and Paper Trading all exist. That sequence was explicit, not incidental.

## Architecture

`ContextProvider` interface, one per section:

| Provider | Fills | Source |
|---|---|---|
| `MarketProvider` | price, VIX, technical (RSI/EMA/ADX/trend), breadth, FII/DII, news sentiment, overnight cascade | `connectors/yahoo,nse,news,sentiment,overnight` + engine's Feature Store (never recomputes indicators independently — that would be a 5th reimplementation of the same math) |
| `PortfolioProvider` | running strategies, total cash/PnL, recent trades | engine's own HTTP API (`GET /strategies`, `GET /strategies/{id}/trades`) |
| `MemoryProvider` | successful/failed strategies, lessons, (for review/optimize) strategy history/backtests/reviews | `memory.Manager` |
| `RegimeProvider` | bull/bear/sideways + confidence | fixed rule over `dc.Market` (EMA20 vs EMA50 for direction, ADX>=20 for "trending", VIX>20 discounts confidence) — disclosed in `RegimeContext.Basis`, never a hidden score |
| `RecommendationProvider` | recommended/avoid styles | regime playbook, then overridden by low-confidence Lessons (a lesson with <35% success over >=5 observations moves that style from recommended to avoid) |

`Builder.Build(ctx, req)` runs only the providers `taskSections` declares for `req.Task` — `build_strategy` loads market+portfolio+memory+regime+recommendation; `review_strategy` skips recommendation (reviewing what exists, not proposing new styles) and adds that specific strategy's history/backtests/reviews/trades.

A provider failing doesn't abort the whole context — it's recorded in `Warnings`, so a caller can tell "NSE breadth unreachable" apart from "breadth was neutral."

## Verified live (2026-08-01/02)

Full `TaskBuildStrategy` run against a real running engine + real connectors + a seeded memory store:
- Real NIFTYBEES price (₹277.42) and India VIX (11.76) from Yahoo
- Real RSI/EMA/ADX/trend from the engine's Feature Store (computed earlier this session from 5 real years of data)
- Real overnight cascade (fell through to the US-markets composite rung, as expected with no GIFT/futures configured)
- Regime correctly classified "sideways" from real ADX=10.7 (below the trending threshold)
- A seeded low-confidence lesson ("momentum underperforms in high VIX", 2/8 success) correctly available in `Lessons`; recommendation logic correctly excludes momentum (regime playbook already did, independently confirming the override path is reachable — see `builder_test.go`'s assertion, which checks the outcome regardless of which mechanism produced it)

## Usage

```go
mgr, _ := memory.Open(ctx, "memory.db")
builder := contextbuilder.NewBuilder(
    contextbuilder.NewMarketProvider("http://localhost:8080"),
    contextbuilder.NewPortfolioProvider("http://localhost:8080"),
    contextbuilder.NewMemoryProvider(mgr),
    contextbuilder.NewRegimeProvider(),
    contextbuilder.NewRecommendationProvider(),
)

dc, _ := builder.Build(ctx, contextbuilder.BuildRequest{
    Task: contextbuilder.TaskBuildStrategy, Symbol: "NIFTYBEES",
    UserPreferences: contextbuilder.UserContext{Style: "swing", Risk: "moderate", Objective: "beat_nifty", Capital: 100000},
})
// dc is the one structured object handed to the AI proposal step.
```

Requires a running engine (for `PortfolioProvider`/`MarketProvider`'s feature-store lookup) — `go test ./... -run TestBuildStrategyContext -v` skips gracefully if `localhost:8099` isn't reachable.

## HTTP server (`cmd/server`, `server.go`, `memory_handlers.go`)

The Python agent (`agent/`) can't import Go code, so this is its only way
into `Builder.Build`, research, and `memory/` — same reasoning as
`connectors/` being the only thing that talks to external APIs.

```
cd contextbuilder
go build -o contextbuilder-server.exe ./cmd/server
CONTEXTBUILDER_PORT=8090 ENGINE_URL=http://localhost:8080 MEMORY_DB_PATH=memory.db ./contextbuilder-server.exe
```

Routes:
- `POST /context/build` — wraps `Builder.Build`; defaults `task` to `build_strategy` and `symbol` to `NIFTYBEES` if omitted.
- `POST /research/query {"query": "...", "max_results": N}` — wraps `research.go`'s curated news/RBI feed search.
- `POST /memory/strategy`, `/memory/context`, `/memory/backtest`, `/memory/deployment` — decode straight into `memory.StrategyRecord`/`ContextSnapshot`/`BacktestRecord`/`Deployment`. **These structs have no JSON tags** — Go's case-insensitive fallback matching doesn't bridge `snake_case` to `PascalCase`, so callers must send the exact Go field names (`StrategyID`, not `strategy_id`).
- `POST /memory/lesson {"key","description","success"}`, `GET /memory/lessons`.
- `GET /health`.

See `BACKLOG.md` for what's deliberately not built yet.
