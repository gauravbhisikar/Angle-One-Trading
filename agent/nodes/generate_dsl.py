import time

from state import AgentState
from nodes.templates import SWING_ARCHETYPES, INTRADAY_ARCHETYPES


def generate_dsl(state: AgentState) -> dict:
    style = state["style"]
    plan = state["plan"]
    candidates = []
    archetypes = INTRADAY_ARCHETYPES if style == "intraday" else SWING_ARCHETYPES
    prefix = "agent-intraday" if style == "intraday" else "agent-swing"

    for i, c in enumerate(plan.get("candidates", [])):
        strategy_id = f"{prefix}-{c['archetype']}-{int(time.time())}-{i}"
        builder = archetypes[c["archetype"]]
        if style == "intraday":
            dsl = builder(strategy_id, c["risk"])
        else:
            dsl = builder(strategy_id, c["holding_days"], c["risk"])
        candidates.append({
            "name": dsl["strategy_name"],
            "archetype": c["archetype"],
            "rationale": c["rationale"],
            "risk": c["risk"],
            "dsl": dsl,
        })
    return {"candidates": candidates}
