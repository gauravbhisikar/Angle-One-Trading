# Agent

The AI strategy-generation agent — built, not just designed. A LangGraph
(Python) graph that turns real market/portfolio/memory context into a
NIFTYBEES DSL strategy, backtests it, and explains why. The agent never
executes trades and never writes raw code: it only ever produces DSL JSON
that the engine independently validates and runs (`docs/DSL_SPEC.md`).

## Architecture

The agent process (`agent/`) never imports Go code — it can't, and
shouldn't. It talks to two Go HTTP services:

- **engine** (`ENGINE_URL`, default `:8080`) — `/strategies/validate`,
  `/backtest`, `/backtest/sample-data`, strategy CRUD/lifecycle
  (`/strategies`, `/strategies/{id}/run`).
- **contextbuilder-server** (`CONTEXTBUILDER_URL`, default `:8090`) —
  `/context/build` (market/portfolio/memory/regime/recommendations, one
  call), `/research/query` (curated news+RBI feed search), and
  `/memory/*` read/write routes so the agent can persist what it did
  without ever touching `memory/`'s SQLite file directly.

All three processes (engine, contextbuilder-server, agent) run
independently and talk over localhost HTTP — same boundary discipline as
the rest of this project (`connectors/` fetches, `engine/` executes,
neither knows about the other directly).

## Graph (`agent/graph.py`)

```
gather_context → plan ─┬─(research_needed)→ research → generate_dsl
                        └─(no)──────────────────────→ generate_dsl
generate_dsl → validate → backtest → rank → self_review → memory_update
```

- **gather_context** — one call to `contextbuilder-server`'s
  `/context/build`: market regime, portfolio state, strategy memory,
  lessons, rule-based regime + recommendations.
- **plan** — the LLM's only real judgment call: pick 2-4 candidates from
  a **fixed 6-archetype menu** (momentum, trend_following, pullback,
  mean_reversion, volatility_expansion, hybrid_momentum) + risk tier +
  holding period, via Pydantic structured output (`nodes/schemas.py`).
  The LLM does **not** write DSL JSON — see "Why the LLM doesn't write
  DSL" below. Intraday has exactly one archetype (VWAP reversion), so
  there's nothing to plan/choose — no LLM call needed there.
- **research** (conditional) — only runs if the plan flags
  `research_needed` (an unusual/conflicting signal in the context worth
  checking). Searches curated `connectors/news` + `connectors/rbi`
  feeds by keyword and fetches full article bodies
  (`connectors/webreader`). This is **not** general web search — no
  discovery-search API exists yet (Google Trends/Reddit were both dead
  ends checked earlier this project). Most requests skip this node.
- **generate_dsl** — pure code, no LLM. Assembles the actual DSL from
  `nodes/templates.py`, a deterministic port of the same templates the
  old Strategy Lab wizard used, already live-tested against 5 years of
  real NIFTYBEES data.
- **validate** — calls the engine's real validator per candidate. Not
  very meaningful in practice since templates are pre-verified, but kept
  as defense-in-depth (a future free-form DSL path would need it more).
- **backtest** — runs each valid swing candidate against the bundled
  5-year dataset. **Intraday is never backtested** — no intraday
  historical dataset exists yet, same gap the old wizard had. The result
  says so plainly rather than fabricating numbers.
- **rank** — `score = 0.5*Sharpe + 0.3*CAGR - 0.2*Drawdown`, same
  transparent formula the old wizard used client-side. Top score wins
  for swing; intraday has only one candidate, nothing to rank.
- **self_review** — LLM explains the pick, grounded only in the real
  backtest numbers and a **code-derived evidence checklist** (which
  context sections actually had data, whether research ran, how many
  past lessons existed) — never a self-reported "I considered X."
- **memory_update** — persists **every** candidate (not just the
  selected one) to `memory/` via contextbuilder-server: strategy,
  backtest, context snapshot, and a lesson (success or failure). A
  rejected candidate is exactly as valuable to remember as the winner.

Every LLM-dependent node (`plan`, `self_review`) retries a structured
call up to twice (`llm.invoke_structured`) before falling back to a
deterministic template — OpenRouter models occasionally emit malformed
JSON. The fallback is always honestly labeled: `state["llm_used"]` tells
the caller whether real reasoning ran or not.

**No checkpointer.** "Human approval" is the Strategy Lab's Deploy
button — a separate UI action taken after reviewing `/generate`'s
result — not a graph-level `interrupt()`. Simpler, and sufficient for V1
since there's no multi-turn conversation to resume mid-graph.

## Why the LLM doesn't write DSL

Free-form DSL generation from an LLM, validated against DSL_SPEC's
strict schema, risks a validate-retry loop that provides no real benefit
over a known-working template (see `nodes/schemas.py` docstring). V1
scopes the LLM's judgment to "which archetype fits this regime, how
aggressive" — a Pydantic-constrained choice from a menu — not "invent
JSON syntax." `nodes/templates.py` deterministically assembles the real
DSL from that choice.

## Running it

```
cd agent
python -m venv venv && venv\Scripts\activate  # or source venv/bin/activate
pip install -r requirements.txt
python api.py   # serves FastAPI on :8091 (AGENT_PORT)
```

Requires `engine.exe` and `contextbuilder-server.exe` already running
(`ENGINE_URL`/`CONTEXTBUILDER_URL` in root `.env`).

### Endpoints (`agent/api.py`)

- `POST /generate {"style": "swing"|"intraday"}` — runs the full graph,
  returns `{llm_used, description, rationale, evidence, selected,
  ranked, research_findings, errors}`. This is what the Strategy Lab's
  "Generate Strategy" button calls.
- `POST /deploy {"dsl": {...}}` — creates the strategy in the engine and
  immediately starts it (`POST /strategies` → `POST /strategies/{id}/run`),
  then records a paper deployment in memory. This is what the Strategy
  Lab's "Deploy to Paper Trading" button calls — it's a real deploy, not
  a save-only action; the user watches it trade live from there.

## Config (root `.env`)

```
OPENROUTER_API_KEY=
OPENROUTER_MODEL=deepseek/deepseek-v4-pro
ENGINE_URL=http://localhost:8080
CONTEXTBUILDER_URL=http://localhost:8090
AGENT_PORT=8091
```

If `OPENROUTER_API_KEY` is unset, every LLM node falls back to a
deterministic template and `llm_used=False` is returned honestly — the
whole graph is exercisable end-to-end without a real key.

## Known gaps

- **Intraday never gets a real backtest** — no intraday historical
  dataset is wired in anywhere in this project yet.
- **Research is curated-feed search, not open web search** — it can
  only find what's already in `connectors/news`/`connectors/rbi`.
- **DSL generation is menu-constrained, not free-form** — see above.
