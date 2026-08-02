from state import AgentState

# Calibrated to this project's own real backtest scale, not a generic
# textbook bar — swing archetypes get ~9-15 real trades over 5 real years
# on this bundled dataset, intraday gets single digits over the ~60 real
# days Yahoo actually gives (see templates.py). A "min_trades=30" style
# threshold would reject every candidate this system can ever produce, on
# either style, which would be a gate so strict it makes the gate useless.
MIN_TRADES = 5
MIN_SHARPE = 0.0
MIN_PROFIT_FACTOR = 1.0


def _score(metrics: dict) -> float:
    # Same transparent formula as the old wizard's client-side tournament
    # (dashboard.html) — kept identical so ranking behavior doesn't
    # silently change between the manual and agent-driven paths.
    return 0.5 * metrics.get("Sharpe", 0) + 0.3 * metrics.get("CAGR", 0) - 0.2 * metrics.get("Drawdown", 0)


def _gate_reasons(metrics: dict) -> list:
    reasons = []
    trades = metrics.get("TotalTrades", 0)
    sharpe = metrics.get("Sharpe", 0)
    pf = metrics.get("ProfitFactor", 0)
    if trades < MIN_TRADES:
        reasons.append(f"only {trades} trades (need >= {MIN_TRADES})")
    if sharpe <= MIN_SHARPE:
        reasons.append(f"Sharpe {sharpe:.2f} <= 0")
    if pf < MIN_PROFIT_FACTOR:
        reasons.append(f"profit factor {pf:.2f} < {MIN_PROFIT_FACTOR}")
    return reasons


def rank(state: AgentState) -> dict:
    candidates = state["candidates"]
    scored = []

    for c in candidates:
        bt = c.get("backtest")
        if not c.get("valid"):
            c["score"] = None
            c["gate_passed"] = False
            c["gate_reasons"] = ["failed DSL validation"]
            scored.append(c)
            continue
        if not bt:
            c["score"] = None
            c["gate_passed"] = False
            c["gate_reasons"] = ["no backtest result"]
            scored.append(c)
            continue

        metrics = bt.get("metrics", {})
        c["score"] = _score(metrics)
        reasons = _gate_reasons(metrics)
        c["gate_passed"] = not reasons
        c["gate_reasons"] = reasons
        scored.append(c)

    ranked = sorted(scored, key=lambda c: (c["score"] is not None, c["score"] or float("-inf")), reverse=True)

    # Only a candidate that actually cleared quality gates can be "the
    # pick" — a real-but-losing backtest (e.g. VWAP Reversion's 0% win
    # rate, negative CAGR, seen live 2026-08-02) must never be presented
    # as a recommendation. If nothing clears the bar, selected=None and
    # self_review says so plainly rather than picking the least-bad loser.
    passed = [c for c in ranked if c.get("gate_passed")]
    selected = passed[0] if passed else None
    return {"ranked": ranked, "selected": selected}
