# One shared legend for decision_context (built by contextbuilder from
# connectors/, memory/, and the live engine) — every node that hands raw
# decision_context to an LLM appends this so the model knows WHERE each
# field comes from and how to weigh it, instead of guessing from key names
# alone. Keep in sync with contextbuilder/types.go's DecisionContext.
CONTEXT_GLOSSARY = """decision_context field guide (what produced it, how to read it):
- market: feature-store technicals (RSI/EMA/ADX/trend) computed from Angel One/Yahoo price data; breadth and
  fii_net/dii_net from NSE; news_sentiment/news_score from the news+sentiment connectors; overnight/
  overnight_confidence from a cascade (GIFT Nifty -> NIFTY futures basis -> US markets+USD/INR), confidence
  reflects which rung actually answered, not a guess. Empty fields mean that source had no data today, not "zero."
- global_market: the global connector's disclosed risk_mode/confidence composite (see its `basis` field for the
  exact rule) built from real US/Asia/commodity/currency quotes — never an invented score.
- portfolio: LIVE engine process state (running strategy count, cash, PnL) as of this exact request — not memory,
  can differ from strategy_memory's last-known status if something changed since.
- strategy_memory (successful/failed/history/backtests/reviews): this project's own persisted memory database —
  every past agent decision, backtest, and review ever recorded, not a market signal.
- paper_trading.running[]: every strategy's live status plus recent_logs (a few real engine log lines — why it's
  paused/running, auto-cutoff reached, errors) so status is never a bare label. paper_trading.recent_trades/
  recent_logs (deeper) are populated only for the specific strategy being reviewed/optimized.
- lessons / recommendations.avoid: archetype-level win-rate learned from this project's own real trade history —
  a real pattern in past samples, NOT a guarantee it keeps working (no regime-segmented validation exists yet).
- regime / recommendations: rule-based labels with a disclosed `basis` string showing the exact rule that
  produced them — quote the basis, never invent your own justification for the label.
- warnings: a section genuinely failed to load (e.g. a connector unreachable) — treat as missing data, not as a
  real "no signal" reading."""
