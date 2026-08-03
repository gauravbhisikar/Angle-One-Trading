from state import AgentState
from llm import get_llm, invoke_structured
from nodes.schemas import Plan, CandidatePlan, ARCHETYPES, DimensionSelectionPlan
from nodes.dimensions import TREND_FILTER_NAMES, ENTRY_TRIGGER_NAMES, CONFIRMATION_NAMES, EXIT_STYLE_NAMES
from nodes.lesson_keys import lesson_key
from context_glossary import CONTEXT_GLOSSARY

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
Most requests do not need this — don't research by default.

Research principles to actually apply, not just recite — every candidate's rationale should reflect these,
not merely name-drop the archetype:
1. Never justify a pick by CAGR/return alone — risk-adjusted (Sharpe, drawdown) matters more than raw return.
2. Prefer robustness over peak backtest performance — a strategy that's merely OK across conditions beats one
   that's spectacular in one narrow case.
3. State which specific piece of the given context (trend, breadth, VIX, FII/DII, sentiment, overnight, regime)
   actually supports this archetype right now — a rationale with no real data point behind it is a guess, say
   so if that's genuinely all you have.
4. Prefer strategies expected to generate multiple trades over the backtest window — a handful of lucky trades
   is not evidence of an edge (this is exactly what nodes/rank.py's min-trades gate and nodes/robustness.py's
   Monte Carlo check exist to catch downstream — plan accordingly, don't fight them).
5. If nothing in the current regime clearly favors an archetype over the others, say that plainly in its
   rationale instead of inventing a story — an honest "this is a reasonable default, not a strong match" is
   more useful than false confidence.
6. Lessons and the avoid-list are a record of what happened in past paper-trading samples under past market
   conditions — they are not a guarantee of future performance. A high-confidence lesson can still stop working
   if the regime shifts (this project has no walk-forward/regime-segmented validation yet — see BACKLOG.md), and
   a low-observation lesson (just above 5) could still be noise. Never phrase a lesson-based choice as 'this is
   proven to work' — phrase it as 'this is what the sample seen so far supports'.
7. Each entry in strategy_memory.successful/failed/history carries a `context` field — the actual regime/VIX/
   FII-DII/sentiment/trend that was present when THAT version was generated. Look across these for a pattern
   (e.g. "this archetype's failures cluster around VIX above 20" or "only succeeded in a bull regime") and use
   it to inform today's pick if today's context resembles one side of that pattern. State it explicitly as an
   observed pattern from N past instances, never as a proven causal rule — a handful of past instances is a
   hypothesis to weigh, not a law, and `context` is missing entirely on older records (say so if you don't have
   enough with `context` present to say anything).

""" + CONTEXT_GLOSSARY


def _avoid_archetypes(state: AgentState, archetype_list: list, style: str) -> list:
    dc = state.get("decision_context") or {}
    lessons = dc.get("lessons") or []
    by_key = {l.get("key"): l for l in lessons if isinstance(l, dict) and l.get("key")}
    avoid = []
    for a in archetype_list:
        lesson = by_key.get(lesson_key(a, style))
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


def _fallback_dimension_selection() -> dict:
    """Deterministic dimension selection used when no LLM key is
    configured (or the call fails) — spans all 4 entry-trigger style
    buckets (mean_reversion, breakout, trend_follow) so the fallback
    tournament still has real diversity, not just proof the graph wires
    correctly. quick_filter.py cartesian-expands this into ~40+
    candidates the same way a real LLM selection would."""
    return {
        "trend_filters": ["none", "ema_cross_20_50"],
        "entry_triggers": ["rsi_pullback_32", "donchian_breakout_up", "macd_bullish_cross", "vwap_reversion_cross"],
        "confirmations": ["none", "volume_spike_150"],
        "exit_styles": ["tp_sl_only", "supertrend_flip"],
        "risk_tiers": ["moderate"],
        "regime_rationale": "template fallback — no LLM configured",
        "research_needed": False,
        "research_queries": [],
    }


INTRADAY_DIMENSION_SYSTEM_PROMPT = """You are a quant strategy researcher for an Indian NIFTYBEES ETF intraday
paper-trading system. You do not write trading logic or invent indicators — you pick which VALUES on each of 4
axes (trend filter, entry trigger, confirmation, exit style) plus which risk tiers are worth trying today.
Downstream code cartesian-expands your selection into dozens of individually parameterized candidates and
backtests all of them in a real tournament — you never see or produce a candidate count, just axis values.

Pick a genuinely diverse set on each axis, not just what you think is "best" — several distinct angles worth
comparing (mean-reversion vs breakout vs trend-follow entries; more than one exit style; more than one risk tier
only if the regime is genuinely ambiguous). 'none' is always a valid trend_filter/confirmation/exit_style choice
— never force a filter or confirmation with no real basis in today's context just to fill the list.

Note: unlike the swing path, there is no per-axis-value avoid-list yet — memory/lessons for intraday are recorded
per exact combination (see nodes/combinatorics.py's spec_archetype_key), not per individual axis value, since a
trigger that fails paired with one trend filter might work paired with another. The hard memory guard still
applies post-hoc to whichever exact combination gets selected (nodes/guardrails.py) — this system prompt just
can't warn you away from a specific axis value in advance the way the swing/named-archetype path can.

Research principles to actually apply, not just recite — every axis choice should reflect these:
1. Never justify a pick by CAGR/return alone — risk-adjusted (Sharpe, drawdown) matters more than raw return.
2. Prefer robustness over peak backtest performance — a combination that's merely OK across conditions beats one
   that's spectacular in one narrow case.
3. State which specific piece of the given context (trend, breadth, VIX, FII/DII, sentiment, overnight, regime)
   actually supports these particular axis choices — a rationale with no real data point behind it is a guess,
   say so if that's genuinely all you have.
4. If nothing in the current regime clearly favors one axis value over another, say that plainly in
   regime_rationale instead of inventing a story.

""" + CONTEXT_GLOSSARY


def _plan_intraday(state: AgentState) -> dict:
    llm = get_llm()
    if llm is None:
        return {"plan": _fallback_dimension_selection(), "llm_used": False}

    is_retry = state.get("retry_count", 0) > 0
    retry_note = (
        f"\n\nThis is retry attempt #{state['retry_count']+1} — every candidate from the previous round(s) "
        f"failed quality gates or a guardrail check ({state.get('guardrail_reasons') or 'see rejection reasons'}). "
        "Pick a genuinely different set of axis values this time, not the same selection again."
        if is_retry else ""
    )
    user_msg = (
        f"Market/portfolio/memory context:\n{state['decision_context']}\n\n"
        f"Axis menu (only choose from these):\n"
        f"trend_filters: {TREND_FILTER_NAMES}\n"
        f"entry_triggers: {ENTRY_TRIGGER_NAMES}\n"
        f"confirmations: {CONFIRMATION_NAMES}\n"
        f"exit_styles: {EXIT_STYLE_NAMES}"
        f"{retry_note}"
    )
    result = invoke_structured(llm, DimensionSelectionPlan, [("system", INTRADAY_DIMENSION_SYSTEM_PROMPT), ("user", user_msg)])
    if result is None:
        return {
            "plan": _fallback_dimension_selection(), "llm_used": False,
            "errors": state.get("errors", []) + ["plan: LLM output failed to parse after retries, used deterministic fallback"],
        }
    return {"plan": result.model_dump(), "llm_used": True}


def plan(state: AgentState) -> dict:
    style = state["style"]
    if style == "intraday":
        return _plan_intraday(state)

    archetype_list = ARCHETYPES
    avoid = _avoid_archetypes(state, archetype_list, style)
    already_tried = {c.get("archetype") for c in state.get("candidates", [])}
    is_retry = state.get("retry_count", 0) > 0

    llm = get_llm()
    if llm is None:
        plan_dict = _fallback_plan(archetype_list, avoid, already_tried)
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
    result = invoke_structured(llm, Plan, [("system", PLAN_SYSTEM_PROMPT), ("user", user_msg)])
    if result is None:
        plan_dict = _fallback_plan(archetype_list, avoid, already_tried)
        for c in plan_dict["candidates"]:
            c["holding_days"] = 20
        return {
            "plan": plan_dict, "llm_used": False,
            "errors": state.get("errors", []) + ["plan: LLM output failed to parse after retries, used deterministic fallback"],
        }
    return {"plan": result.model_dump(), "llm_used": True}
