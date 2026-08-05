from state import AgentState
import clients
import dsl_utils
from nodes.templates import SWING_ARCHETYPES


def _deployed_signatures_and_archetypes() -> tuple:
    """Fingerprints every currently-deployed strategy's entry logic so
    generation can avoid proposing a structural duplicate (see
    nodes/quick_filter.py for intraday, nodes/plan.py's _avoid_archetypes
    for swing). Best-effort — clients.list_deployed_dsls() itself already
    swallows per-strategy/engine-unreachable errors, so this never blocks
    generation over a transient engine hiccup."""
    deployed_dsls = clients.list_deployed_dsls()
    signatures = {dsl_utils.entry_leaf_signature(d) for d in deployed_dsls}

    # Swing archetypes are fixed, unparameterized templates (risk tier only
    # changes position sizing/TP-SL, never the entry condition itself) —
    # probing each constructor once with placeholder args gives its entry
    # signature straight from templates.py, the single source of truth,
    # rather than a second hardcoded copy of each archetype's entry rule.
    archetype_names = set()
    for name, build in SWING_ARCHETYPES.items():
        probe_dsl = build("_dup_probe", 20, "moderate")
        if dsl_utils.entry_leaf_signature(probe_dsl) in signatures:
            archetype_names.add(name)

    return signatures, archetype_names


def gather_context(state: AgentState) -> dict:
    dc = clients.build_context(
        task=state.get("task", "build_strategy"),
        symbol=state.get("symbol", "NIFTYBEES"),
        user_preferences={"style": state["style"]},
    )
    deployed_signatures, deployed_archetypes = _deployed_signatures_and_archetypes()
    return {
        "decision_context": dc,
        "deployed_entry_signatures": deployed_signatures,
        "deployed_archetype_names": deployed_archetypes,
    }
