# Engine Spec — India Market Execution Rules v1.2

Rules the engine enforces at execution time, independent of DSL content. AI never expresses these — engine applies them unconditionally to every order, every strategy. Companion to [DSL_SPEC.md](DSL_SPEC.md).

## 0. Multi-Strategy Concurrency and Shared Indicator Pipeline

Engine runs up to **10 strategies concurrently** (configurable cap, `MAX_CONCURRENT_STRATEGIES=10`), each strategy exactly one DSL document, mixed freely — intraday and swing side by side, no separation needed since they differ only in `holding`/`session` fields (DSL_SPEC Sec 10, 15). Design goal: adding strategy #10 costs barely more CPU/memory than #2, because the expensive work (ticks, candles, indicators) is computed once and shared, never once per strategy.

### 0.1 Single process, async event loop

One process, one `asyncio` event loop. Strategies are NOT separate OS processes or threads — each is a lightweight evaluator callback registered against the events it cares about. 10 strategies means 10 cheap tree-evaluations per relevant candle-close, not 10 duplicate pipelines. This is what keeps resource use flat as strategy count grows.

### 0.2 One broker connection, deduped subscriptions

Single Angel One websocket session for the whole engine. Symbol subscription list = union of every active strategy's `symbols` (DSL_SPEC Sec 1), deduped — if 6 of 10 strategies trade `RELIANCE`, engine subscribes to `RELIANCE` ticks once, fans the tick out internally to every strategy watching it. Adding a strategy that reuses an already-subscribed symbol costs zero extra broker connection overhead.

### 0.3 Shared candle builders

One candle builder per `(symbol, timeframe)` pair, not per strategy. Ticks feed a single 1-minute OHLC builder per symbol; every higher timeframe (5m/15m/1h/1d, DSL_SPEC Sec 1 timeframe enum) is built by aggregating that same 1-minute stream, one aggregator instance per `(symbol, timeframe)` regardless of how many strategies reference it.

```
tick -> 1m candle builder (per symbol, singleton)
          -> 5m aggregator  (per symbol, shared by all strategies using 5m)
          -> 15m aggregator (per symbol, shared)
          -> 1h / 1d aggregator (per symbol, shared)
```

### 0.4 Shared indicator cache

Indicator values cached and computed once per `(symbol, timeframe, indicator, params)` key on every candle close for that timeframe — never recomputed per strategy. Two strategies both using `ema_cross(fast=20, slow=50)` on `RELIANCE 15m` hit the same cache entry; the second strategy's condition evaluation is a cache read, not a recompute.

```
cache_key = hash(symbol, timeframe, indicator_name, sorted(params))
on candle_close(symbol, timeframe):
    for each indicator subscribed by any active strategy at this (symbol, timeframe):
        update cache[cache_key] incrementally (e.g. EMA/RSI updated O(1) from prior state,
        not recomputed over the full history each time)
    notify all strategies subscribed to this (symbol, timeframe)
```

Incremental (streaming) indicator updates are mandatory, not optional — recomputing a 200-period EMA over full history on every single candle close, times 10 strategies, is the exact resource waste this design avoids.

### 0.5 Strategy isolation despite shared pipeline

Sharing stops at ticks/candles/indicators. Each strategy keeps fully isolated: portfolio ledger, risk block state (Sec `max_daily_loss`, `max_positions`), order/trade records, cooldown/reentry counters (DSL_SPEC Sec 11-12). One strategy's fill, breach, or error never touches another's state — same guarantee as strategy versioning (DSL_SPEC Sec 26), extended across concurrently running strategies.

### 0.6 Resource bounds

- Rolling candle history per `(symbol, timeframe)` capped to the longest lookback any active indicator needs (e.g. longest EMA/SMA period × small buffer) — not unbounded in-memory history. Full history lives in Postgres/SQLite for backtest/analytics, not held in RAM.
- 11th "run strategy" request beyond the concurrency cap is rejected at the API layer (`POST /strategies/{id}/run` returns `concurrency_limit_reached`), not silently queued to run degraded.
- Per-strategy evaluation is O(condition tree size) against already-cached indicator values — cost of strategy #10 is a few boolean comparisons, not a new market-data pipeline.

## 1. Quantity, Lot, and Tick Rules {#quantity-lot-and-tick-rules}

### 1.1 No fractional shares

Indian exchanges do not allow fractional quantities. Engine never places, and paper engine never simulates, a fractional-share order.

```
STOCK / ETF / INDEX (cash-equivalent) -> quantity = floor(amount / ltp)
FUTURES / OPTIONS                     -> quantity = floor(amount / (lot_size * ltp)) * lot_size
```

If resolved quantity is `0`, reject the trade. Never round up, never allow partial-unit fallback. Engine logs the rejection with reason `qty_zero_after_rounding` so Daily Review can surface "capital_per_trade too small for this symbol's price."

### 1.2 Lot size validation (F&O)

Every `FUTURES`/`OPTIONS` order quantity must be an exact multiple of the instrument's lot size, sourced from the Instrument Master (Sec 8) — never hardcoded per strategy. Order rejected pre-submit if `qty % lot_size != 0`.

### 1.3 Tick size — dynamic, never hardcoded

Tick size is not uniform across NSE instruments. Engine fetches tick size per-instrument from the Instrument Master (Sec 8), refreshed daily — never a fixed constant like `0.05` in code. All `LIMIT`/`STOP_LIMIT` prices rounded to the instrument's actual tick before submit:

```
tick_size = instrument_master.get(symbol).tick_size
rounded_price = round(price / tick_size) * tick_size
```

`MARKET` orders unaffected. Engine rejects any DSL-derived limit price that cannot be rounded within `slippage_pct` tolerance without crossing through a stop level.

### 1.4 Integer quantities, always

Quantity is stored and computed as `int`, never `float` or `Decimal`. No operation in the engine ever produces a fractional quantity mid-calculation — sizing math floors to `int` at the point of computation (Sec 1.1), not at display time.

## 2. Price and Quantity Data Types

All prices — LTP, entry, exit, stop levels, computed indicator values used for price comparisons — stored and computed as `Decimal`, never `float`, throughout the engine (candles, indicators, PnL, cost model). Floating-point rounding error is unacceptable in a financial ledger. Rounding to tick size or currency precision (paise) happens only at the execution boundary (order submit, fill recording) — internal computation stays full-precision `Decimal`.

## 3. Circuit Limits

Engine tracks each symbol's daily upper/lower circuit band from the Instrument Master feed. Before submitting any order:

- If LTP is at or beyond the circuit band and the order would need to trade further in that direction, reject with `circuit_limit_hit` (upper or lower).
- An open position whose exit cannot fill because of a circuit hit is NOT force-exited and does NOT transition to a Trade State (`STOPPED`/`TARGET_HIT`/`CLOSED`, DSL_SPEC Sec 25) yet. Engine flags it with an internal order-level marker `EXIT_BLOCKED` — the strategy's exit intent is real, the market is refusing the fill. Trade stays `ACTIVE`. Once the circuit lifts and the exit order actually fills, the trade transitions normally to whichever terminal state the fill reason implies.

Paper trading must simulate this — a backtest/paper run that always assumes fills at circuit-hit prices is unrealistically optimistic and gets flagged as invalid in Daily Review if circuit data isn't wired in.

## 4. Liquidity and Fill Simulation

Two configurable modes, set per environment (not per strategy):

```
Basic mode    -> every order fills in full, immediately, at expected price + slippage_pct.
                 Use for early strategy iteration where fill mechanics aren't the concern.

Advanced mode -> use L2 market depth if the data feed provides it.
                 If L2 unavailable, estimate available liquidity via a configurable
                 liquidity model (e.g. participation-rate cap against recent average volume),
                 never raw single-candle volume — a candle's traded volume does not represent
                 what was actually available at the moment the order arrived.
```

Uses existing order states (`OPEN -> PARTIAL -> FILLED`, DSL_SPEC Sec 24) — this is a fill-simulation rule, not a schema change.

## 5. Brokerage and Tax Model

Split into two layers — India-wide statutory taxes (shared, never broker-specific) and per-broker brokerage (varies):

**Statutory (India equity, applies regardless of broker):**

| Charge | Applies to | Notes |
|---|---|---|
| STT (Securities Transaction Tax) | sell side (delivery), both sides (intraday) | rate differs delivery vs intraday |
| Exchange transaction charges | every fill | NSE rate, percent of turnover |
| SEBI turnover fee | every fill | fixed percent of turnover |
| Stamp duty | buy side only | state-mandated, percent of turnover |
| GST | on (brokerage + exchange charges) | 18% |

**Brokerage (broker-specific, pluggable preset per `cost_model`):**

```
angel_equity
zerodha_equity
dhan_equity
upstox_equity
```

Each preset = statutory layer (shared module, computed once) + that broker's brokerage formula (flat fee, percent, or capped-percent — broker-specific). Adding a new broker's cost preset never touches the statutory module. DSL's `cost_model` field (DSL_SPEC Sec 18) names the broker-specific preset directly, e.g. `"cost_model": "angel_equity"`. `india_fno`, `us_equity`, `crypto`, `forex` follow later as separate statutory modules, same interface.

Every simulated trade nets out all charges — a strategy's reported profit is always post-cost, never gross.

## 6. Market Hours (NSE)

Hardcoded, not DSL-configurable:

```
Pre-open     09:00 - 09:15
Normal       09:15 - 15:30
Post-close   15:40 - 16:00
```

Engine rejects any new order attempt outside `09:15–15:30`, regardless of what `session.entry_start`/`entry_end` in the DSL say — DSL session window is a strategy-level narrowing inside market hours, never a way to extend past them.

## 6.1 Automatic Session Start/Stop

`internal/marketsession.Monitor` polls the market-hours check above (`MARKET_SESSION_POLL_SECONDS`, default 30s) and auto-pauses/resumes strategies around it — the engine "starting" and "stopping" trading activity in step with the real session, not a human remembering to click Pause every evening:

- **At close**: every currently *running* strategy is paused (blocks new entries; a paused strategy still manages and exits whatever's already open — same as a manual pause, `internal/strategy/runtime.go`'s `StatePaused`). Never `Stop` — that would tear down the runtime and free its concurrency slot, which is wrong for a strategy that's simply waiting for tomorrow's session, not being retired.
- **At open**: only strategies the Monitor itself paused get auto-resumed. A strategy a human paused for their own reason before close is left alone — the Monitor tracks its own auto-paused set and never claims credit for (or reverses) someone else's decision.
- **Every transition is logged**, not just flipped silently: a `market_open`/`market_close` entry under the reserved `SYSTEM` strategy ID (`GET /system/logs`), plus a per-strategy `auto-paused`/`auto-resumed` entry (`GET /strategies/{id}/logs`) for each strategy actually touched — so an AI agent reviewing what happened has an explicit, queryable trail instead of having to infer session boundaries from trade timestamps.
- Holiday-unaware, same caveat as Sec 7 below: a listed NSE holiday still reports "open" (weekday, within 09:15-15:30), since the holiday calendar isn't wired into this check yet.

## 7. Trading Holiday Calendar

Engine loads NSE's published holiday calendar (annual list, refreshed yearly) into `storage/calendar/nse_holidays.json`. No strategy declares its own holidays — DSL's `calendar.holiday: "skip"` (DSL_SPEC Sec 16) just toggles whether this global calendar is consulted for that strategy; the calendar itself is never AI- or per-strategy-defined.

## 8. Instrument Master

Central reference service, refreshed every morning before market open. Every other rule in this doc (lot size, tick size, circuit bands, expiry, symbol changes) reads from this, never from ad-hoc per-strategy config.

```
Fields per instrument:
  symbol
  token
  exchange
  instrument_type   (EQ, FUT, OPT, ETF, INDEX)
  lot_size
  tick_size
  expiry            (F&O only)
  strike            (options only)
  circuit_band      (upper/lower, refreshed daily)
```

Symbol changes (e.g. corporate rename, `-EQ` suffix changes) resolved here — engine maps old-symbol references in open positions to the new instrument record on refresh, logs a `symbol_remapped` event, never silently drops the position.

## 9. Corporate Actions

Engine adjusts open positions for corporate actions effective during the holding period — bonus issue, stock split, dividend, rights issue. Sourced from the Instrument Master's corporate action feed, applied automatically:

```
Split / bonus -> position quantity and average entry price adjusted proportionally,
                 stop_loss / take_profit levels (which are percent-based off entry) recompute
                 against the adjusted entry price, not the pre-split one.
Dividend      -> recorded against the position as a cash credit in the paper ledger,
                 does not alter quantity or entry price.
Rights issue  -> logged as a corporate action event on the position; auto-subscription
                 not simulated (AI/user decision, out of engine scope) — position adjusted
                 only if the entitlement is later reflected in market price/quantity feed.
```

Without this, a swing strategy holding across a split/bonus reports wrong PnL and wrong `average_hold_days` in Daily Review.

## 10. F&O Expiry Handling

Engine checks every `FUTURES`/`OPTIONS` position's `expiry` field (Instrument Master, Sec 8) each session. On or after expiry date, engine blocks any further order (new or exit-adjust) against that contract and marks the trade `CLOSED` with reason `contract_expired` if still open — this is a hard boundary, not something `holding.max_days`/`force_square_off` governs.

## 11. Settlement — Product Type Square-off

Engine distinguishes product types (DSL_SPEC `execution.product`, Sec 9) and applies India-specific settlement behavior:

```
MIS   (intraday)       -> engine auto-squares-off ahead of the broker's own MIS cutoff
                           (broker cutoffs vary ~15:15-15:20; engine uses the earlier of
                           strategy's own force_square_off or a safety-margin cutoff, e.g. 15:15),
                           so a strategy never gets broker-auto-squared at an unmodeled price.
CNC   (delivery)        -> no forced square-off; holding governed purely by DSL's holding block.
NRML  (carry-forward)   -> no forced square-off; subject to F&O Expiry Handling (Sec 10) instead.
```

## 12. Position Reconciliation

After every order execution, paper engine verifies internal consistency:

```
cash + sum(holdings market value) + sum(open order reserved margin) == expected ledger total
```

Mismatch logged as a `reconciliation_error` (engine bug signal, not a trading event) and halts new order submission for that strategy version until resolved — this check exists to catch engine bugs, not market conditions.

## 12.1 Backtesting

`POST /backtest` (body: `{"strategy": <DSL>, "candles": [...], "starting_capital": N}`) runs a DSL document against a historical candle series before it's ever saved as a strategy version — the engine's answer to "would this have worked over the last 5 years," gating what's even eligible for paper trading.

Deliberately reuses the exact live/paper strategy runtime (`internal/strategy`, same condition evaluator, indicator cache, holding/exit-priority/risk/cost rules) rather than a separate backtest simulator — a backtest that reimplements strategy logic independently can silently drift from what live trading actually does, defeating the point of a backtest. The only substitution is the Feed: candles are replayed from the request body instead of a live tick stream, processed as fast as possible instead of paced to wall-clock time. Fill simulation is `FillBasic` (ENGINE_SPEC Sec 4) — instant fill at candle close + slippage, the same default the live paper broker uses.

Candles are supplied by the caller (an agent, via `connectors/historical`), not fetched by the engine itself — this preserves the engine's boundary of "AI/tooling fetches data, engine only executes deterministically against what it's given."

Response includes: closed+open trades, `Metrics` (win rate, profit factor, Sharpe/Sortino, CAGR, max drawdown, best/worst trade, total trades), an equity curve, and an `ai_review` block in the exact DSL_SPEC Sec 27 shape — so backtest results and live-trading AI Reviews are the same schema the agent already knows how to consume. The engine never interprets these numbers as "good" or "bad" — same rule as live trading (Sec 27 preamble): a human or the AI proposal layer decides what backtest results mean, the engine only produces them.

## 12.2 Feature Store

`POST /features/compute` (body: `{"symbol": "NIFTYBEES", "candles": [...]}`), `GET /features/{symbol}?from=&to=`, `POST /features/{symbol}/macro` — persists daily computed technical indicators (RSI14, EMA20/50, EMA/MACD cross flags, ADX14, ATR14, Bollinger %B) per symbol per day, queryable historically, instead of every consumer (memory context snapshots, a future Decision Context Engine) recomputing them from raw candles every time.

Lives in `internal/featurestore`, reusing the engine's actual indicator implementations (`internal/indicators`) rather than a fourth reimplementation of RSI/EMA/MACD math — the same drift-avoidance reasoning as Sec 12.1's backtest engine reusing the live strategy runtime. This is also why Feature Store is engine-internal rather than a separate top-level module like `connectors/` or `memory/`: Go's `internal/` package visibility only allows imports from within the same module tree, so reusing the real indicator code requires living inside `engine/`.

Macro/sentiment columns (VIX, FII/DII net, breadth advance/decline ratio, news sentiment) exist in the schema but are sparse by design — those connectors (`connectors/nse`, `connectors/sentiment`) only reliably provide current-day snapshots, not free historical series, so they're merged in via the `/macro` endpoint for whatever day a caller actually has data for, never backfilled or guessed.

## 13. Explicit Non-Goals

Engine intentionally does NOT implement:

- Margin / leverage calculations
- Option Greeks
- Basket orders
- Iceberg orders
- GTT (Good Till Triggered) orders
- AMO (After Market Orders)

This is an AI strategy execution platform, not a broker terminal. Anything above is out of scope unless a future version explicitly adds it as its own spec section.

## 14. Summary — Engine-Enforced, Not DSL-Expressed

| Rule | Enforced by |
|---|---|
| 10 concurrent strategies, shared tick/candle/indicator pipeline | Engine, single async event loop (Sec 0) |
| Integer quantities only, floor-rounded | Engine, `execution/sizing.py` |
| Lot-size multiples for F&O | Engine, Instrument Master lookup |
| Tick-size rounding, per-instrument | Engine, Instrument Master lookup |
| `Decimal` prices, `int` quantities everywhere | Engine-wide type contract |
| Circuit-limit rejection, `EXIT_BLOCKED` marker | Engine, pre-submit check + order state |
| Fill simulation (Basic / Advanced modes) | Paper engine fill simulator |
| Statutory tax + per-broker brokerage split | `portfolio/costs/<broker>_equity.py` |
| NSE market hours | Engine, order gate — overrides nothing in DSL |
| NSE holiday calendar | Engine, global calendar store |
| Instrument Master (daily refresh) | Engine, central reference service |
| Corporate action adjustment | Engine, position adjuster on Instrument Master feed |
| F&O expiry block | Engine, per-position expiry check |
| Product-type square-off (MIS/CNC/NRML) | Engine, session-close scheduler |
| Position reconciliation | Engine, post-execution consistency check |

None of these are fields the AI sets. AI expresses *what* to trade (DSL). Engine enforces *how* it's legal to trade it in India.
