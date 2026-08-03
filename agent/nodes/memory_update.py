import json

from state import AgentState
from nodes.lesson_keys import lesson_key
import clients

# memory.StrategyRecord / BacktestRecord / ContextSnapshot / Deployment have
# NO json tags (memory/strategy.go, backtest.go, context.go, deployment.go)
# — Go's encoding/json falls back to case-insensitive matching only, which
# does not bridge snake_case to PascalCase, so these dicts must use the
# exact Go field names verbatim.


def _strategy_record(state: AgentState, c: dict, selected_id: str) -> dict:
    dsl = c["dsl"]
    return {
        "StrategyID": dsl["strategy_id"],
        "Version": dsl.get("strategy_version", 1),
        "ParentStrategyID": "",
        "Name": dsl.get("strategy_name", c["name"]),
        "DSLJSON": json.dumps(dsl),
        "Objective": (state.get("decision_context") or {}).get("user", {}).get("objective", ""),
        "Style": state["style"],
        "Risk": c.get("risk", ""),
        "Status": "backtest",
        "ChangeReason": "" if dsl["strategy_id"] != selected_id else "selected by agent",
    }


def _backtest_record(dsl: dict, bt: dict, score) -> dict:
    m = bt.get("metrics", {})
    return {
        "StrategyID": dsl["strategy_id"],
        "Version": dsl.get("strategy_version", 1),
        "PeriodFrom": "",
        "PeriodTo": "",
        "TotalReturn": str(m.get("StrategyReturn", 0)),
        "CAGR": str(m.get("CAGR", 0)),
        "Drawdown": str(m.get("Drawdown", 0)),
        "Sharpe": str(m.get("Sharpe", 0)),
        "Sortino": str(m.get("Sortino", 0)),
        "ProfitFactor": str(m.get("ProfitFactor", 0)),
        "WinRate": str(m.get("WinRate", 0)),
        "TotalTrades": m.get("TotalTrades", 0),
        "EquityCurveJSON": json.dumps(bt.get("equity_curve", [])),
    }


def _context_snapshot(dc: dict, strategy_id: str, version: int) -> dict:
    market = dc.get("market", {}) if dc else {}
    return {
        "StrategyID": strategy_id,
        "Version": version,
        "MarketRegime": (dc.get("regime", {}) or {}).get("regime", ""),
        "VIX": market.get("vix", ""),
        "FIINet": market.get("fii_net", ""),
        "DIINet": market.get("dii_net", ""),
        "BreadthADRatio": market.get("breadth", ""),
        "NewsSentiment": market.get("news_sentiment", ""),
        "NewsScore": str(market.get("news_score", "")),
        "PCR": "",
        "RSI": market.get("rsi14", ""),
        "Trend": market.get("trend", ""),
        "VolumeRegime": "",
        "Notes": "saved by agent memory_update node",
    }


def memory_update(state: AgentState) -> dict:
    selected = state.get("selected")
    selected_id = (selected.get("dsl", {}) or {}).get("strategy_id") if selected else None
    dc = state.get("decision_context") or {}

    for c in state.get("candidates", []):
        dsl = c.get("dsl", {})
        strategy_id = dsl.get("strategy_id")
        if not strategy_id:
            continue

        try:
            clients.save_strategy_memory(_strategy_record(state, c, selected_id))
        except Exception:
            pass

        bt = c.get("backtest")
        if bt:
            try:
                clients.save_backtest_memory(_backtest_record(dsl, bt, c.get("score")))
            except Exception:
                pass

        try:
            clients.save_context_memory(_context_snapshot(dc, strategy_id, dsl.get("strategy_version", 1)))
        except Exception:
            pass

        # Record a lesson either way — failures are as informative as
        # successes for future planning. "Success" means it actually
        # cleared quality gates (nodes/rank.py), not merely "backtested
        # without error" — a real losing strategy (0% win rate, negative
        # Sharpe) must count as a failure here, or the avoid-list in
        # plan.py would never learn to skip it.
        try:
            clients.record_lesson(
                key=lesson_key(c.get("archetype"), state["style"]),
                description=c.get("rationale", ""),
                success=bool(c.get("gate_passed")),
            )
        except Exception:
            pass

    return {}
