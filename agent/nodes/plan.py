from state import AgentState
from llm import get_llm, invoke_structured
from nodes.schemas import Plan, CandidatePlan, IntradayPlan, IntradayCandidatePlan, ARCHETYPES, INTRADAY_ARCHETYPES

# Same threshold contextbuilder/recommendation_provider.go already uses for
# style-level avoidance (a lesson with <35% success over >=5 observations
# moves from recommended to avoid) — reused here at archetype granularity
# so the agent doesn't keep re-proposing something it has already learned
# fails, without inventing a second, inconsistent confidence bar.
MIN_OBSERVATIONS = 5
MAX_CONFIDENCE_TO_AVOID = 0.35

PLAN_SYSTEM_PROMPT = """You are a quant strategy planner for an Indian NIFTYBEES ETF paper-trading system.
You do not write trading logic yourself — you pick from a fixed menu of known archetypes and choose a risk
tier (and holding period, for swing) for each, based on the given market context. Always propose several
genuinely different candidates — never just one — so they can be backtested and compared in a real tournament;
a single-candidate "plan" defeats the whole point of ranking.

An avoid-list will be given, built from this project's own real trade history (not a guess): an archetype on
it has been tried at least 5 times in this style and succeeded (survived quality gates) less than 35% of the
time. Do not propose an avoid-listed archetype unless something in the CURRENT context gives you a specific,
stated reason to think this time is different — never include it silently as if its history didn't happen.

Only set research_needed=true if something in the context is genuinely unusual or conflicting (e.g. elevated
VIX with no clear driver, signals disagreeing with each other) that a curated news/RBI feed could clarify.
Most requests do not need this — don't research by default."""


def _avoid_archetypes(state: AgentState, archetype_list: list, style: str) -> list:
    dc = state.get("decision_context") or {}
    lessons = dc.get("lessons") or []
    by_key = {l.get("key"): l for l in lessons if isinstance(l, dict) and l.get("key")}
    avoid = []
    for a in archetype_list:
        lesson = by_key.get(f"{a}_{style}")
        if not lesson:
            continue
        if lesson.get("times_seen", 0) >= MIN_OBSERVATIONS and lesson.get("confidence", 1.0) < MAX_CONFIDENCE_TO_AVOID:
            avoid.append({
                "archetype": a,
                "times_seen": lesson["times_seen"],
                "confidence": round(lesson.get("confidence", 0.0), 2),
            })
    return avoid


def _fallback_plan(archetype_list: list, avoid: list, already_tried: set, risk: str = "moderate") -> dict:
    """Deterministic plan used when no LLM key is configured (or the LLM
    call fails after retries) — proves the graph's wiring works without
    claiming to be real reasoning. Skips avoid-listed and already-tried
    archetypes; if that would leave nothing, falls back to the full menu
    rather than an empty plan (an empty candidate list is worse than one
    flagged-risky or repeated option)."""
    avoided_names = {a["archetype"] for a in avoid}
    kept = [a for a in archetype_list if a not in avoided_names and a not in already_tried]
    kept = kept or [a for a in archetype_list if a not in avoided_names] or list(archetype_list)
    return {
        "candidates": [
            {"archetype": a, "risk": risk, "rationale": "template fallback — no LLM configured"}
            for a in kept
        ],
        "research_needed": False,
        "research_queries": [],
    }


def plan(state: AgentState) -> dict:
    style = state["style"]
    archetype_list = INTRADAY_ARCHETYPES if style == "intraday" else ARCHETYPES
    schema = IntradayPlan if style == "intraday" else Plan
    avoid = _avoid_archetypes(state, archetype_list, style)
    already_tried = {c.get("archetype") for c in state.get("candidates", [])}
    is_retry = state.get("retry_count", 0) > 0

    llm = get_llm()
    if llm is None:
        plan_dict = _fallback_plan(archetype_list, avoid, already_tried)
        if style != "intraday":
            for c in plan_dict["candidates"]:
                c["holding_days"] = 20
        return {"plan": plan_dict, "llm_used": False}

    retry_note = (
        f"\n\nThis is retry attempt #{state['retry_count']+1} — every candidate from the previous round(s) "
        f"failed quality gates or a guardrail check ({state.get('guardrail_reasons') or 'see rejection reasons'}). "
        f"Already tried this run, do not repeat unless you have a genuinely different risk/parameter angle: {sorted(already_tried)}."
        if is_retry else ""
    )
    user_msg = (
        f"Market/portfolio/memory context:\n{state['decision_context']}\n\n"
        f"Style requested: {style}\n\n"
        f"Archetype menu (only choose from these): {archetype_list}\n\n"
        f"Avoid-list (real history, see system prompt): {avoid if avoid else 'none — no archetype has enough history to judge yet'}"
        f"{retry_note}"
    )
    result = invoke_structured(llm, schema, [("system", PLAN_SYSTEM_PROMPT), ("user", user_msg)])
    if result is None:
        plan_dict = _fallback_plan(archetype_list, avoid, already_tried)
        if style != "intraday":
            for c in plan_dict["candidates"]:
                c["holding_days"] = 20
        return {
            "plan": plan_dict, "llm_used": False,
            "errors": state.get("errors", []) + ["plan: LLM output failed to parse after retries, used deterministic fallback"],
        }
    return {"plan": result.model_dump(), "llm_used": True}
