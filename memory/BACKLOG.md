# Backlog

## Deferred — no consumer yet

- **System/Knowledge Memory** (static config: supported indicators, DSL version, broker config) — this already lives in `docs/DSL_SPEC.md`, `docs/ENGINE_SPEC.md`, and the engine's own indicator registry. Duplicating it into SQLite is a sync-drift risk (two sources of truth) for a layer that changes rarely and isn't queried dynamically by anything today.
- **User Preference Memory** — no UI exists yet to collect "user prefers swing, medium risk, avoid options." The Strategy Lab wizard currently collects this per-session (style/risk/objective/holding), not as a persisted cross-session preference. Add a `preferences` table once there's an actual "remember my defaults" feature request, not before.
- **Research Memory** (prompt, sources used, strategies generated, rejected ideas) — needs a research agent that runs research sessions; none exists (`agent/` is still just a README). Building the schema before there's a producer means guessing its shape.
- **Semantic/vector search** ("find similar strategies") — nothing queries this yet. Same call GPT itself made about this layer: not required in V1. SQLite full-text search or an embedding column can be added later without restructuring the existing tables.
- **Event replay / rebuild-derived-tables-from-events** — architecturally supported (events are the source of truth, derived tables are just a cache of them) but not implemented. No current scenario needs to rebuild state from scratch; add it if derived-table corruption or a schema migration ever actually requires replaying history.

## Real gaps worth closing next

- **Nothing calls this module yet.** `engine/` doesn't write to `memory/` on strategy create/backtest/trade events, and `agent/` doesn't exist to read from it. This is infrastructure ahead of its wiring — the natural next step is having the engine's API handlers (`POST /strategies`, `POST /backtest`, the trade hooks) call into a `memory.Manager` instance, OR having a thin sync process read the engine's own SQLite (`trading.db`) and mirror events into `memory.db`. Neither is built yet; pick one when `agent/` starts getting built for real, since the shape of that integration should follow how the agent actually consumes this data, not be guessed now.
- **Lesson-key taxonomy** — `RecordLesson` takes a free-form string key (e.g. `"high_vix_ema_crossover"`). Nothing enforces a naming convention yet, so as usage grows, near-duplicate keys (`"high_vix_ema"` vs `"high_vix_ema_crossover"`) could fragment the same lesson into two rows. Worth a controlled vocabulary once there are enough real lessons to see what taxonomy actually emerges — inventing one now would be guessing.
