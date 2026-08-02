# Memory

Standalone Go module (`memory`, no dependency on `engine/` or `connectors/`) — the persistent, cross-agent memory store. Every strategy ever created, the market context the AI saw when creating it, every backtest, every paper-trading deployment, every trade, every daily snapshot, every review, and every lesson learned. Nothing here depends on conversation history; a future agent process restarts cold and rebuilds everything it knows by querying this store.

## Architecture

**Event-sourced core**: every `Save*`/`Record*` call appends an immutable row to `events` (what happened, when, what payload) AND updates a read-optimized derived table, in the same transaction. `events` is the audit trail; derived tables are for fast queries. Verified live: a full strategy lifecycle (create → context → backtest → deploy → trade open/close → daily snapshot → review) produces a complete, in-order, replayable event history.

**Layers built (V1 — each has a concrete consumer today):**

| Layer | Table(s) | API |
|---|---|---|
| Strategy lineage | `strategies` | `SaveStrategy`, `GetStrategyHistory`, `GetSuccessfulStrategies`, `GetFailedStrategies` |
| Context | `strategy_context` | `SaveContext`, `GetContextForStrategy` |
| Backtests | `backtests` | `SaveBacktest`, `GetBacktestsForStrategy` |
| Execution (deployments) | `deployments` | `SaveDeployment`, `UpdateDeploymentStatus`, `GetCurrentDeployments` |
| Execution (trades) | `trades` | `SaveTrade` (upserts by ID: open then close is the same row), `GetTradesForStrategy` |
| Execution (daily) | `daily_snapshots` | `SaveDailySnapshot`, `GetDailySnapshots` |
| Reflection | `reviews` | `SaveReview`, `GetReviewsForStrategy` |
| Lessons | `lessons` | `RecordLesson` (increments times_seen/success/failed, recomputes confidence), `GetLessons` |
| Audit trail | `events` | `EventsForStrategy` |

Strategies are never overwritten (DSL_SPEC Sec 26) — a strategy_id + version is the primary key, editing means inserting version+1 with `ParentStrategyID`/`ChangeReason` set, same discipline the engine's own storage already follows.

## Deliberately NOT built in V1

See `BACKLOG.md`. Short version: a static "System/Knowledge" config layer (duplicates `docs/DSL_SPEC.md`/`docs/ENGINE_SPEC.md`, low value), a "Research Memory" layer (no research agent exists yet to produce runs), User Preference memory (no preference-collection UI exists), and semantic/vector search (nothing queries "find similar strategies" yet). Building these now would be guessing at a shape before there's a real consumer — the same discipline `connectors/BACKLOG.md` already applies to deferred items there.

Event-replay (rebuilding derived tables from the `events` log alone) is architecturally possible but not implemented — nothing needs to rebuild state from scratch yet; the derived tables are always written directly alongside the event.

## Usage

```go
m, _ := memory.Open(ctx, "memory.db")
defer m.Close()

m.SaveStrategy(ctx, memory.StrategyRecord{StrategyID: "momentum-1", Version: 1, Name: "Momentum Breakout", DSLJSON: dslJSON, Status: "backtest"})
m.SaveContext(ctx, memory.ContextSnapshot{StrategyID: "momentum-1", Version: 1, MarketRegime: "bull", VIX: "12.4", ...})
m.SaveBacktest(ctx, memory.BacktestRecord{StrategyID: "momentum-1", Version: 1, CAGR: "8.4", Sharpe: "1.7", ...})

// Before generating a new strategy, an agent queries what's already known:
successful, _ := m.GetSuccessfulStrategies(ctx, 20)
lessons, _ := m.GetLessons(ctx)
running, _ := m.GetCurrentDeployments(ctx)
```

Live-tested (`manager_test.go`): full lifecycle — versioning, context round-trip, backtest-driven success/failure classification, deployment status transitions, trade open→close upsert (verified NOT duplicated), daily snapshots, reviews with structured strengths/weaknesses, and lesson-confidence aggregation across repeated observations (12 recordings → correct 9/3 split, confidence ~0.75) — all pass with real assertions, not just "no error."
