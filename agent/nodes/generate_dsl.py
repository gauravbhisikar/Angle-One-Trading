import time

from state import AgentState
from nodes.templates import SWING_ARCHETYPES
from nodes.combinatorics import build_dsl, spec_archetype_key


def _generate_intraday(state: AgentState) -> dict:
    """Consumes candidate_specs — the post-quick_filter survivors of the
    combinatorial expansion (nodes/combinatorics.py, nodes/quick_filter.py)
    — one full DSL dict per spec. "archetype" here is the composite spec
    key (spec_archetype_key), not a named template; every downstream node
    that reads c["archetype"] (rank.py's display, memory_update.py's
    lesson key, guardrails.py's avoid-list check) treats it as an opaque
    string either way, so nothing else needs to change."""
    specs = state.get("candidate_specs") or []
    prefix = "agent-intraday"
    candidates = []
    for i, spec in enumerate(specs):
        archetype_key = spec_archetype_key(spec)
        strategy_id = f"{prefix}-{i}-{int(time.time())}"
        dsl = build_dsl(spec, strategy_id, timeframe="5m")
        candidates.append({
            "name": dsl["strategy_name"],
            "archetype": archetype_key,
            "rationale": (
                f"Combinatorial candidate: entry={spec['entry_trigger']}, trend_filter={spec['trend_filter']}, "
                f"confirmation={spec['confirmation']}, exit={spec['exit_style']}, risk={spec['risk_tier']}. "
                f"{state.get('plan', {}).get('regime_rationale', '')}"
            ),
            "risk": spec["risk_tier"],
            "dsl": dsl,
        })
    return {"candidates": candidates}


def generate_dsl(state: AgentState) -> dict:
    if state["style"] == "intraday":
        return _generate_intraday(state)

    plan = state["plan"]
    candidates = []
    prefix = "agent-swing"
    for i, c in enumerate(plan.get("candidates", [])):
        strategy_id = f"{prefix}-{c['archetype']}-{int(time.time())}-{i}"
        builder = SWING_ARCHETYPES[c["archetype"]]
        dsl = builder(strategy_id, c["holding_days"], c["risk"])
        candidates.append({
            "name": dsl["strategy_name"],
            "archetype": c["archetype"],
            "rationale": c["rationale"],
            "risk": c["risk"],
            "dsl": dsl,
        })
    return {"candidates": candidates}
