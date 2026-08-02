"""Deterministic final check before a candidate is allowed to become
"selected" — no LLM here. rank.py already rejects on backtest performance
(Sharpe/PF/trade count); this node catches what performance alone can't:
structurally unsafe DSL, risk limits outside policy, and a hard
enforcement of memory avoidance (plan.py only *asks* the LLM to skip a
historically-bad archetype — a system prompt is a request, not a
guarantee, so this is the code-level backstop that actually can't be
talked past)."""
from state import AgentState
from nodes.plan import _avoid_archetypes

MAX_POSITION_PCT = 25
MAX_DAILY_LOSS_PCT = 10


def _safety_reasons(dsl: dict) -> list:
    reasons = []
    exit_block = dsl.get("exit", {}) or {}
    exit_rules = list(exit_block.get("all", []) or []) + list(exit_block.get("any", []) or [])
    has_exit_protection = any("stop_loss" in r or "take_profit" in r for r in exit_rules)
    if not has_exit_protection:
        reasons.append("no stop_loss/take_profit found in exit rules")
    if not (dsl.get("risk") or {}).get("max_daily_loss"):
        reasons.append("no max_daily_loss risk limit set")
    return reasons


def _risk_reasons(dsl: dict) -> list:
    reasons = []
    sizing = dsl.get("position_sizing") or {}
    size = sizing.get("value", 0)
    if size and size > MAX_POSITION_PCT:
        reasons.append(f"position size {size}% exceeds {MAX_POSITION_PCT}% cap")
    daily_loss = (dsl.get("risk") or {}).get("max_daily_loss", 0)
    if daily_loss and daily_loss > MAX_DAILY_LOSS_PCT:
        reasons.append(f"max_daily_loss {daily_loss}% exceeds {MAX_DAILY_LOSS_PCT}% cap")
    return reasons


def guardrails(state: AgentState) -> dict:
    selected = state.get("selected")
    retry_count = state.get("retry_count", 0) + 1  # counts this attempt regardless of outcome

    if not selected:
        # rank.py already found nothing deployable (all candidates failed
        # backtest quality gates) — nothing left to guard, just track the
        # attempt so should_retry can decide whether to try another round.
        return {"retry_count": retry_count, "guardrail_reasons": []}

    reasons = _safety_reasons(selected["dsl"]) + _risk_reasons(selected["dsl"])

    avoid = _avoid_archetypes(state, [selected["archetype"]], state["style"])
    if avoid:
        a = avoid[0]
        failures = a["times_seen"] - round(a["confidence"] * a["times_seen"])
        reasons.append(
            f"archetype has failed {failures}/{a['times_seen']} times historically in this style "
            f"({a['confidence']:.0%} success rate) — hard memory guard, not just a soft suggestion"
        )

    if reasons:
        selected["gate_passed"] = False
        selected["gate_reasons"] = (selected.get("gate_reasons") or []) + reasons
        return {"selected": None, "guardrail_reasons": reasons, "retry_count": retry_count}

    return {"guardrail_reasons": [], "retry_count": retry_count}


def should_retry(state: AgentState) -> str:
    if state.get("selected") is None and state.get("retry_count", 0) <= state.get("max_retries", 2):
        return "retry"
    return "continue"
