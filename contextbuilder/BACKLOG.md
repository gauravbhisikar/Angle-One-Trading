# Backlog

## Deferred — no consumer yet

- **User Preference persistence** — `UserContext` is caller-supplied per request (e.g. the Strategy Lab wizard's answers), not loaded from a stored-preferences table. Same call `memory/BACKLOG.md` already made: no UI collects persisted cross-session preferences yet.
- **`optimize_strategy`'s "similar successful strategies"** — GPT's spec wanted optimize to load "similar successful strategies," not just this one's own history. "Similar" needs a similarity measure (same archetype? same indicators? same regime it was built in?) that doesn't exist yet — `memory/BACKLOG.md` already deferred semantic/vector search for exactly this reason. `optimize_strategy` currently gets the same sections as `build_strategy` plus nothing beyond what `memory`'s `StrategyID`-scoped queries return. Revisit once there's a real similarity signal, not an invented one.
- **Task-specific engine features beyond the latest row** — `MarketProvider.latestFeatures` only asks for the last 10 days and uses the most recent row. A `review_strategy` call might want the feature-store history spanning the exact backtest/deployment period instead of "today." Not built because nothing consumes that longer window yet.

## Real gaps worth closing next

- **Nothing calls `Builder.Build` in production yet.** This module is exercised by its own live test (`builder_test.go`), not by anything real — same status `memory/BACKLOG.md` notes for the memory store itself ("nothing calls this module yet"). The natural next caller is `agent/`, whenever that gets built for real.
- **Regime playbook is a first-pass rule, not backtested.** `regimePlaybook`'s recommend/avoid mapping (bull→momentum/trend, bear→trend-only, sideways→mean-reversion) is a reasonable starting rule, not validated against historical regime-vs-style performance. Once enough `memory` backtest/lesson data accumulates across regimes, this table should be derived from that data rather than hand-written — but hand-written-and-disclosed beats a black-box guess for a first version.
- **`MarketProvider`'s VIX/breadth/FII-DII calls are best-effort** (same NSE reachability caveat as `connectors/README.md` — some of these endpoints 403 from datacenter IPs). A caller running this from a residential/office connection should see fewer `Warnings` than this sandbox did.
