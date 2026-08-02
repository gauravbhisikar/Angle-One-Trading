# Strategy DSL Spec v1.2

Contract between AI (generates DSL) and Engine (executes DSL). AI never writes Python. Engine never modifies DSL.

Indicators are never fetched from the broker. Angel One (or any broker) only provides OHLC candles, ticks, LTP, volume. Engine computes every indicator locally from candles it builds itself (tick → 1m candle → higher timeframe → indicator).

## 1. Top-Level Schema

```json
{
  "version": "1.2",
  "strategy_id": "uuid",
  "strategy_name": "string",
  "strategy_version": 1,
  "type": "intraday | swing",
  "asset_type": "ETF | STOCK | INDEX | FUTURES | OPTIONS",
  "direction": "long | short | both",
  "enabled": true,
  "timeframe": "1m | 3m | 5m | 10m | 15m | 30m | 1h | 4h | 1d | 1w",
  "symbols": ["NIFTYBEES"],
  "entry": { "...": "Condition" },
  "exit": { "...": "Condition" },
  "exit_priority": ["stop_loss", "take_profit", "signal"],
  "confirmation": "close | intrabar",
  "position_sizing": { "...": "PositionSizingBlock" },
  "execution": { "...": "ExecutionBlock" },
  "session": { "...": "SessionBlock" },
  "cooldown": { "...": "CooldownBlock" },
  "reentry": { "...": "ReentryBlock" },
  "risk": { "...": "RiskBlock" },
  "portfolio": { "...": "PortfolioBlock" },
  "holding": { "...": "HoldingBlock" },
  "calendar": { "...": "CalendarBlock" },
  "market_regime": ["bull", "bear", "sideways"],
  "cost_model": "india_equity",
  "benchmark": "NIFTYBEES",
  "review": { "...": "ReviewBlock" },
  "metadata": { "...": "MetadataBlock" },
  "tags": ["trend", "momentum", "swing"]
}
```

Fields are immutable once saved. Edits create new `strategy_version` (v1, v2, v3...), never overwrite.

## 2. Condition Grammar

Recursive boolean tree. Three combinators, infinitely nestable:

```json
{ "all": [ Condition, Condition ] }   // AND
{ "any": [ Condition, Condition ] }   // OR
{ "not": Condition }                  // NOT
```

Leaf node = one `Rule`. Nesting example:

```json
{
  "all": [
    { "indicator": "ema_cross", "fast": 20, "slow": 50, "operator": "bullish" },
    {
      "any": [
        { "indicator": "rsi", "operator": "<", "value": 35 },
        { "indicator": "adx", "operator": ">", "value": 25 }
      ]
    }
  ]
}
```

## 3. Rule (leaf node) Schema

```json
{
  "indicator": "string",       // required, must be in Indicator Registry (Sec 6)
  "operator": "string",        // <, <=, >, >=, ==, crosses_above, crosses_below, bullish, bearish
  "value": number | "string",  // literal, OR
  "compare_to": { ... },       // another indicator ref, for indicator-vs-indicator rules
  "params": { ... },           // indicator-specific params (period, fast, slow, std_dev, etc.)
  "timeframe": "string"        // optional override of strategy-level timeframe
}
```

Indicator-vs-indicator example (price crossing above an EMA):

```json
{
  "indicator": "close",
  "operator": "crosses_above",
  "compare_to": { "indicator": "ema", "params": { "period": 20 } }
}
```

Exit-only shorthand leaves (no `indicator` field, engine-resolved):

```json
{ "take_profit": 10 }   // percent from entry price
{ "stop_loss": 5 }      // percent from entry price
{ "trailing_sl": 3 }    // percent, trails favorable move
```

## 4. Exit Priority

When multiple exit conditions fire on the same bar, `exit_priority` (top-level array) decides which one wins and gets recorded as the trade's close reason:

```json
"exit_priority": ["stop_loss", "take_profit", "signal"]
```

`signal` = any exit condition resolved from `exit.any`/`exit.all` tree that isn't `take_profit`/`stop_loss`/`trailing_sl`. Default order if omitted: `stop_loss` > `trailing_sl` > `take_profit` > `signal`.

## 5. Confirmation

```json
"confirmation": "close"      // default. Rule evaluated only on candle close.
"confirmation": "intrabar"   // evaluated on every tick/price update, before candle closes
```

Default is `close`. `intrabar` must be explicitly opted into per strategy — changes fill realism and backtest-vs-live parity.

## 6. Indicator Registry (pluggable)

Each indicator = one entry in `engine/dsl/indicators/registry.py`, implementing a common `Indicator` interface (`compute(candles, params) -> series`). Adding a new one never touches the parser or validator. All computed locally from candles — never fetched from broker.

| Indicator | Params | Operators |
|---|---|---|
| `price` / `close` | — | <, <=, >, >=, ==, crosses_above, crosses_below |
| `open` / `high` / `low` | — | <, <=, >, >=, ==, crosses_above, crosses_below |
| `ema` | period | <, <=, >, >=, crosses_above, crosses_below |
| `ema_cross` | fast, slow | bullish, bearish |
| `sma` | period | <, <=, >, >=, crosses_above, crosses_below |
| `vwap` | — | <, >, crosses_above, crosses_below |
| `rsi` | period (default 14) | <, <=, >, >=, crosses_above, crosses_below |
| `stochastic_rsi` | period, k, d | <, >, crosses_above, crosses_below |
| `macd` | fast, slow, signal | bullish, bearish, crosses_above, crosses_below |
| `adx` | period (default 14) | <, > |
| `atr` | period (default 14) | <, > |
| `cci` | period (default 20) | <, >, crosses_above, crosses_below |
| `mfi` | period (default 14) | <, >, crosses_above, crosses_below |
| `roc` | period | <, >, crosses_above, crosses_below |
| `obv` | — | crosses_above, crosses_below, rising, falling |
| `supertrend` | period, multiplier | bullish, bearish |
| `bollinger_bands` | period, std_dev | price_above_upper, price_below_lower, squeeze |
| `donchian_channel` | period | price_above_upper, price_below_lower, breakout_up, breakout_down |
| `highest_high` | lookback | crosses_above, crosses_below, == |
| `lowest_low` | lookback | crosses_above, crosses_below, == |
| `volume` | — | <, >, spike_pct |
| `prev_high` | — | crosses_above |
| `prev_low` | — | crosses_below |
| `gap_up` | min_pct | true |
| `gap_down` | min_pct | true |
| `support` | lookback | crosses_above, crosses_below, bounce |
| `resistance` | lookback | crosses_above, crosses_below, bounce |
| `pattern` | pattern_name (hammer, shooting_star, doji, bullish_engulfing, bearish_engulfing, morning_star, evening_star, harami) | true |

Multi-timeframe: any Rule can carry its own `"timeframe"` overriding strategy default — e.g. daily trend filter + 15m entry trigger in the same `all` block.

## 7. Position Sizing Block

```json
{ "type": "fixed_pct", "value": 2 }          // 2% of total capital per trade
{ "type": "fixed_amount", "value": 10000 }   // flat ₹10,000 per trade
{ "type": "fixed_qty", "value": 100 }        // fixed 100 shares
{ "type": "risk_based", "value": 1 }         // risk 1% of portfolio, qty derived from stop_loss distance
```

`risk_based` requires `exit` tree to contain a `stop_loss` or `trailing_sl` leaf — engine rejects otherwise.

No fractional shares. Engine always resolves final quantity to `floor(amount / price)` for `STOCK`/`ETF`/`INDEX`, or nearest lower multiple of lot size for `FUTURES`/`OPTIONS`. If resolved quantity is 0, trade rejected, not rounded up. Full quantity/lot/tick rules: [ENGINE_SPEC.md](ENGINE_SPEC.md#quantity-lot-and-tick-rules).

## 8. Direction

```json
"direction": "long | short | both"
```

Required, no default. `long` = buy-to-open only. `short` = sell-to-open only (futures/options, or equity short where broker/product allows). `both` lets entry tree signal either side (needs a `side` hint per branch in a future version — v1.2 treats `both` as long-only until short-signal grammar is added). Engine rejects any order leg contradicting this field regardless of what the condition tree resolves to.

## 9. Execution Block

```json
{
  "mode": "paper | live",
  "broker": "angel | zerodha | upstox | dhan | ibkr",
  "exchange": "NSE | BSE | NFO",
  "product": "MIS | CNC | NRML",
  "order_type": "MARKET | LIMIT | STOP_LIMIT",
  "entry": "market | limit | stop_limit",
  "slippage_pct": 0.05
}
```

Broker is a pluggable adapter (`execution/brokers/<name>.py`) implementing a common `BrokerAdapter` interface (`place_order`, `cancel_order`, `get_positions`). Engine core never branches on broker name.

## 10. Session Block

Intraday only, required. Bounds when entries are allowed — does not affect exits (exits always allowed to protect an open position):

```json
{
  "entry_start": "09:20",
  "entry_end": "14:45"
}
```

## 11. Cooldown Block

```json
{ "bars": 5 }
```

After a trade closes (any reason), engine blocks new entries on that symbol for N bars of the strategy's `timeframe`.

## 12. Reentry Block

```json
{
  "allowed": true,
  "max_reentries": 2
}
```

Caps how many times a strategy can re-open a position on the same symbol within one trading day (intraday) or one holding cycle (swing), after a prior exit. `allowed: false` = one shot per symbol per session.

## 13. RiskBlock

```json
{
  "max_daily_loss": 5,         // percent, halts new entries for the day when breached
  "max_positions": 5,          // concurrent open positions cap
  "max_position_size_pct": 10  // percent of capital in single symbol, optional
}
```

## 14. Portfolio Block

Cross-strategy / cross-symbol exposure caps, enforced by the portfolio manager, not the individual strategy:

```json
{
  "max_sector_exposure": 25,
  "max_symbol_exposure": 10
}
```

Percent of total capital. Engine blocks a new entry that would breach either cap even if the strategy's own `risk` block would allow it — portfolio limits are the outer bound.

## 15. HoldingBlock

```json
{
  "max_days": 15,                          // swing only. Trading days, not calendar days.
  "force_square_off": "15:20",              // intraday only. HH:MM, engine auto-exits regardless of exit conditions.
  "max_open_trade_duration_minutes": 90     // intraday only, optional. Exits at whichever comes first: this or force_square_off.
}
```

Swing rule: at `max_days`, if an exit condition is already true, exit normally. If not, do NOT force-close — mark trade `EXPIRED`, keep monitoring, notify AI via Daily Review. Intraday rule: `force_square_off` always fires, no exceptions; `max_open_trade_duration_minutes` fires earlier if the trade hits its time cap first.

## 16. Calendar Block

```json
{
  "holiday": "skip",       // skip: no entries on exchange holidays
  "expiry_day": "allow"    // allow | skip — F&O expiry day handling
}
```

## 17. Market Regime

```json
"market_regime": ["bull", "bear", "sideways"]
```

Declares which regime(s) this strategy is designed for. Engine computes current regime (index vs its 200 SMA + trend slope) each day and refuses new entries when current regime isn't in the list. Existing open positions unaffected.

## 18. Cost Model

```json
"cost_model": "india_equity"
```

Named preset resolved by the paper engine's cost module (`portfolio/costs/<name>.py`) — brokerage, STT, exchange charges, GST, stamp duty, slippage. Pluggable per market: `india_equity`, `india_fno`, `us_equity`, `crypto`, `forex` (future).

## 19. Benchmark

```json
"benchmark": "NIFTYBEES"
```

Required. Daily Review and AI Review always report strategy return against this benchmark's return over the same period.

## 20. Review Block

```json
{
  "min_completed_trades": 50,
  "review_after_days": 30
}
```

Guards against premature strategy judgment. AI Review (Sec 27) must not recommend retiring or revising a strategy until both thresholds are met.

## 21. Metadata Block

```json
{
  "author": "AI",
  "created_at": "2026-07-31T00:00:00Z",
  "description": "string",
  "reason": "Strong earnings + EMA breakout",
  "notes": "string"
}
```

`reason` is the AI's stated rationale for proposing this strategy/version — surfaces directly in Daily Review.

## 22. Tags

```json
"tags": ["trend", "momentum", "swing"]
```

Free-form labels, used for filtering/grouping in the API and analytics only. No engine behavior depends on tags.

## 23. Validation Rules (engine enforces, rejects on violation)

1. `version` must match a schema version the engine parser supports.
2. `type` must be `intraday` or `swing`.
3. `intraday` requires `holding.force_square_off` and `session`; `swing` requires `holding.max_days`.
4. `direction` required, must be `long`, `short`, or `both`.
5. Every `indicator` name must exist in the Indicator Registry.
6. Every `operator` must be valid for that indicator (Sec 6 table).
7. Condition tree must resolve to a boolean — no unbalanced `all`/`any`/`not`.
8. No duplicate leaf Rules: two leaves with identical `(indicator, params, operator, value, timeframe)` in the same entry/exit tree is rejected as redundant.
9. `position_sizing.type == "risk_based"` requires a `stop_loss` or `trailing_sl` leaf in `exit`.
10. `risk.max_positions` combined with `position_sizing` must not allow >100% capital deployed (warn if > 60%).
11. `portfolio.max_sector_exposure` / `max_symbol_exposure` must each be between 0 and 100.
12. `symbols` must be non-empty, uppercase, exchange-valid format.
13. `benchmark` required, must be a valid tradeable symbol.
14. `exit_priority`, if present, must only contain `stop_loss`, `take_profit`, `trailing_sl`, `signal`, each at most once.
15. `confirmation` must be `close` or `intrabar`.
16. No `eval`-style free-form expressions permitted anywhere — every field is a fixed enum or typed literal.

Validation runs at DSL submit time (API `POST /strategies/validate`) and again at load time before execution. Failure returns structured error list with `path` (JSON pointer) + `reason`, never a partial-accept.

## 24. Order States

`PENDING -> OPEN -> PARTIAL -> FILLED | REJECTED | CANCELLED`, and `FILLED -> EXITED`.

## 25. Trade States

`OPEN -> ACTIVE -> (CLOSED | STOPPED | TARGET_HIT | EXPIRED)`

`EXPIRED` only reachable from swing trades past `max_days` with no exit condition met.

## 26. Versioning

`strategy_id` fixed across versions. `strategy_version` increments per edit. Each version paper-trades independently under its own portfolio ledger — no shared state, no silent migration of open positions between versions.

## 27. AI Review Output Schema

Engine-generated, AI-consumed only — AI never reads raw trade rows directly, only this structured summary:

```json
{
  "strategy_id": "uuid",
  "strategy_version": 1,
  "market_type": "bull | bear | sideways",
  "period": { "from": "date", "to": "date" },
  "summary": "string",
  "completed_trades": 0,
  "open_positions": 0,
  "missed_entries": 0,
  "false_entries": 0,
  "metrics": {
    "win_rate": 0.0,
    "profit_factor": 0.0,
    "sharpe": 0.0,
    "sortino": 0.0,
    "drawdown": 0.0,
    "average_hold_days": 0.0,
    "largest_winner": 0.0,
    "largest_loser": 0.0,
    "benchmark_return": 0.0,
    "strategy_return": 0.0
  },
  "mistakes": ["string"],
  "good_decisions": ["string"],
  "recommendations": ["string"]
}
```

Engine populates this only after `review.min_completed_trades` and `review.review_after_days` are both satisfied (Sec 20). Engine never acts on `recommendations` — a human or a separate AI proposal step creates the next `strategy_version`.

## 28. Future: DSL Split (v2)

v1.2 keeps `risk` and `portfolio` embedded in the Strategy DSL for simplicity. Planned v2 split, once multi-strategy/multi-broker portfolios are live:

- **Strategy DSL** — entry, exit, indicators, sizing, execution (this doc, minus risk/portfolio).
- **Risk DSL** — per-strategy risk block, versioned independently, shared across strategy versions if desired.
- **Portfolio DSL** — cross-strategy allocation, sector/symbol caps, applies at the account level.

Not implemented in v1.2. Parser written with block-level modularity (each top-level block validated/compiled independently) so the v2 split is a config change, not a rewrite.

## 29. Full Example — Swing

```json
{
  "version": "1.2",
  "strategy_id": "8f14e...",
  "strategy_name": "EMA Momentum",
  "strategy_version": 1,
  "type": "swing",
  "asset_type": "ETF",
  "direction": "long",
  "enabled": true,
  "timeframe": "1d",
  "symbols": ["NIFTYBEES"],
  "entry": {
    "all": [
      { "indicator": "ema_cross", "fast": 20, "slow": 50, "operator": "bullish" },
      { "indicator": "rsi", "operator": "<", "value": 35 }
    ]
  },
  "exit": {
    "any": [
      { "indicator": "ema_cross", "operator": "bearish" },
      { "take_profit": 10 },
      { "stop_loss": 5 }
    ]
  },
  "exit_priority": ["stop_loss", "take_profit", "signal"],
  "confirmation": "close",
  "position_sizing": { "type": "fixed_pct", "value": 2 },
  "execution": {
    "mode": "paper",
    "broker": "angel",
    "exchange": "NSE",
    "product": "CNC",
    "order_type": "MARKET",
    "entry": "market",
    "slippage_pct": 0.05
  },
  "cooldown": { "bars": 5 },
  "reentry": { "allowed": true, "max_reentries": 2 },
  "risk": { "max_daily_loss": 5, "max_positions": 5, "max_position_size_pct": 10 },
  "portfolio": { "max_sector_exposure": 25, "max_symbol_exposure": 10 },
  "holding": { "max_days": 15 },
  "calendar": { "holiday": "skip", "expiry_day": "allow" },
  "market_regime": ["bull", "sideways"],
  "cost_model": "india_equity",
  "benchmark": "NIFTYBEES",
  "review": { "min_completed_trades": 50, "review_after_days": 30 },
  "metadata": {
    "author": "AI",
    "created_at": "2026-07-31T00:00:00Z",
    "description": "EMA(20/50) bullish cross with RSI oversold filter",
    "reason": "Strong earnings + EMA breakout",
    "notes": ""
  },
  "tags": ["trend", "momentum", "swing"]
}
```

## 30. Full Example — Intraday

```json
{
  "version": "1.2",
  "strategy_id": "3ab21...",
  "strategy_name": "VWAP Reversion",
  "strategy_version": 1,
  "type": "intraday",
  "asset_type": "STOCK",
  "direction": "long",
  "enabled": true,
  "timeframe": "5m",
  "symbols": ["RELIANCE"],
  "entry": {
    "all": [
      { "indicator": "vwap", "operator": "crosses_above" },
      { "indicator": "volume", "operator": "spike_pct", "value": 150 }
    ]
  },
  "exit": {
    "any": [
      { "take_profit": 1.5 },
      { "stop_loss": 0.8 }
    ]
  },
  "exit_priority": ["stop_loss", "take_profit"],
  "confirmation": "close",
  "position_sizing": { "type": "risk_based", "value": 1 },
  "execution": {
    "mode": "paper",
    "broker": "angel",
    "exchange": "NSE",
    "product": "MIS",
    "order_type": "MARKET",
    "entry": "market",
    "slippage_pct": 0.05
  },
  "session": { "entry_start": "09:20", "entry_end": "14:45" },
  "cooldown": { "bars": 5 },
  "reentry": { "allowed": true, "max_reentries": 1 },
  "risk": { "max_daily_loss": 3, "max_positions": 3 },
  "portfolio": { "max_sector_exposure": 25, "max_symbol_exposure": 10 },
  "holding": { "force_square_off": "15:20", "max_open_trade_duration_minutes": 90 },
  "calendar": { "holiday": "skip", "expiry_day": "allow" },
  "market_regime": ["bull", "bear", "sideways"],
  "cost_model": "india_equity",
  "benchmark": "NIFTYBEES",
  "review": { "min_completed_trades": 50, "review_after_days": 30 },
  "metadata": {
    "author": "AI",
    "created_at": "2026-07-31T00:00:00Z",
    "description": "VWAP reversion scalp on volume spike",
    "reason": "Intraday liquidity spike edge",
    "notes": ""
  },
  "tags": ["mean_reversion", "intraday"]
}
```
