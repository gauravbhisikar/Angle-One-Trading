from state import AgentState
from llm import get_llm, invoke_structured
from nodes.schemas import SelfReview
from context_glossary import CONTEXT_GLOSSARY

SELF_REVIEW_SYSTEM_PROMPT = """You are explaining a trading strategy tournament's outcome to the user who will
decide whether to deploy it to paper trading. Ground every claim ONLY in the real numbers and context given
below — never invent a statistic, trade count, or market fact not present in the input. If the evidence
checklist shows something was NOT consulted (false), do not claim it was.

If a candidate is selected: it already cleared quality gates (trades >= 5, Sharpe > 0, profit factor >= 1.0)
— you do not need to re-justify that it's "good," just explain why it beat the other survivors. Note the real
trade count honestly — intraday backtests run against a shorter real dataset (Yahoo's intraday retention caps
out around 60 real days for 5-minute data, not 5 years like the daily swing backtest), so a low trade count
there is expected, not a red flag to hide.

The selected candidate also carries three extra real checks — mention what they actually found, do not skip
them: (1) benchmark comparison (metrics.BenchmarkReturn vs metrics.StrategyReturn — over long swing periods
buy-and-hold usually wins on raw return since a timing strategy is only in the market part of the time; that
is normal and expected, explain it that way rather than treating underperformance vs a static benchmark as a
failure by itself), (2) monte_carlo (real_order_percentile >= 95 means this exact backtest got a suspiciously
favorable trade sequence — flag that plainly if present), (3) stability (Sharpe swinging across sign under a
small parameter nudge means the edge may be fragile/overfit to this exact parameter choice — flag that plainly
if present). confidence.score (0-100) and confidence.breakdown are a disclosed formula — quote the number and
what it's made of, never invent a different confidence figure of your own.

If NO candidate is selected (selected is null): every candidate failed quality gates. Say plainly "No
deployable strategy found for the requested objective under current conditions" and summarize, using the real
gate_reasons on each ranked candidate, why they failed (e.g. negative Sharpe, profit factor below 1, too few
trades) — do not soften this into a recommendation, and do not pick the "least bad" loser and present it as
if it were fine.

If decision context lessons or the avoid-list influenced which archetype was even proposed, note in weaknesses
that this project has no walk-forward/regime-segmented validation yet — a lesson built from past paper-trading
samples describes what happened under past conditions, not a guarantee this candidate keeps working if the
regime shifts. State that plainly rather than implying past success is proof of a durable edge.

""" + CONTEXT_GLOSSARY


def _evidence_checklist(state: AgentState) -> dict:
    dc = state.get("decision_context") or {}
    market = dc.get("market", {}) if isinstance(dc, dict) else {}
    return {
        "trend": bool(market.get("trend")),
        "breadth": bool(market.get("breadth")),
        "vix": bool(market.get("vix")),
        "fii_dii": bool(market.get("fii_net") or market.get("dii_net")),
        "news_sentiment": bool(market.get("news_sentiment")),
        "research_conducted": bool(state.get("research_findings")),
        "lessons_consulted": len(dc.get("lessons", []) or []),
    }


def _evidence_summary(evidence: dict) -> str:
    bool_keys = [k for k, v in evidence.items() if k != "lessons_consulted" and v]
    parts = [k.replace("_", " ") for k in bool_keys]
    if evidence.get("lessons_consulted"):
        parts.append(f"{evidence['lessons_consulted']} past lessons")
    return ", ".join(parts) if parts else "none"


def _rejection_summary(state: AgentState) -> str:
    ranked = state.get("ranked") or []
    parts = []
    for c in ranked:
        reasons = c.get("gate_reasons") or []
        if reasons:
            parts.append(f"{c.get('name', c.get('archetype', '?'))}: {', '.join(reasons)}")
    return "; ".join(parts) if parts else "no candidates were backtested"


def _fallback_review(state: AgentState, evidence: dict) -> SelfReview:
    selected = state.get("selected")
    if not selected:
        return SelfReview(
            description="No deployable strategy found for the requested objective under current conditions.",
            rationale=(
                f"Every candidate failed quality gates (trades >= 5, Sharpe > 0, profit factor >= 1.0). "
                f"Rejections: {_rejection_summary(state)}. "
                f"(No LLM configured — this is a template summary of the real numbers, not generated reasoning.)"
            ),
            weaknesses=["No candidate cleared quality gates this run."],
        )
    bt = (selected.get("backtest") or {}).get("metrics")
    if bt is None:
        metrics_line = "No backtest available for this candidate."
    else:
        metrics_line = (
            f"Selected by score = 0.5*Sharpe + 0.3*CAGR - 0.2*Drawdown "
            f"(Sharpe={bt.get('Sharpe', 0):.2f}, CAGR={bt.get('CAGR', 0):.2f}%, "
            f"Drawdown={bt.get('Drawdown', 0):.2f}%, trades={bt.get('TotalTrades', 0)}) among the ranked candidates. "
            f"Buy-and-hold benchmark over the same period: {bt.get('BenchmarkReturn', 0):.2f}% "
            f"vs strategy's {bt.get('StrategyReturn', 0):.2f}% (a timing strategy trading only a handful "
            f"of times is normally behind a static benchmark on raw return — that alone isn't a red flag)."
        )
    mc = selected.get("monte_carlo") or {}
    stab = selected.get("stability") or {}
    conf = selected.get("confidence") or {}
    extra = []
    if mc.get("ran"):
        extra.append(f"Monte Carlo: real drawdown {mc['real_max_drawdown_pct']}% vs median shuffled {mc['median_shuffled_drawdown_pct']}%"
                     + (f" — {mc['flag']}" if mc.get("flag") else " (typical, not a lucky sequence)"))
    if stab.get("ran"):
        extra.append(f"Parameter stability ({stab['indicator_perturbed']}): Sharpe {stab['min_sharpe']} to {stab['max_sharpe']} across nearby parameters"
                     + (f" — {stab['flag']}" if stab.get("flag") else " (holds up)"))
    if conf:
        extra.append(f"Confidence {conf['score']}/100 ({conf['basis']})")

    return SelfReview(
        description=f"{selected['name']} ({selected['archetype']}) — {selected.get('rationale', '')}",
        rationale=(
            f"{metrics_line} {' '.join(extra)} Evidence consulted: {_evidence_summary(evidence)}. "
            f"(No LLM configured — this is a template summary of the real numbers, not generated reasoning.)"
        ),
        weaknesses=["Auto-generated summary, no LLM available to critique further."],
    )


def self_review(state: AgentState) -> dict:
    # This node explains and persists a decision that's already made — a
    # transient LLM/network hiccup here must never cost the user the
    # candidates, backtest, and DSL the earlier nodes already produced.
    # Any unexpected failure degrades to the deterministic template rather
    # than propagating and killing the whole run.
    evidence = {}
    try:
        evidence = _evidence_checklist(state)
        llm = get_llm()

        if llm is not None:
            selected = state.get("selected")
            review = invoke_structured(llm, SelfReview, [
                ("system", SELF_REVIEW_SYSTEM_PROMPT),
                ("user",
                 f"Selected candidate: {selected}\n\n"
                 f"All ranked candidates: {state.get('ranked')}\n\n"
                 f"Decision context: {state.get('decision_context')}\n\n"
                 f"Research findings: {state.get('research_findings')}\n\n"
                 f"Evidence checklist (what was actually consulted, code-derived not self-reported): {evidence}"),
            ])
            if review is not None:
                return {"description": review.description, "rationale": review.rationale, "evidence": evidence}

        review = _fallback_review(state, evidence)
        return {
            "description": review.description,
            "rationale": review.rationale,
            "evidence": evidence,
            "errors": state.get("errors", []) + ["self_review: LLM output unavailable, used deterministic fallback"],
        }
    except Exception as e:
        review = _fallback_review(state, evidence)
        return {
            "description": review.description,
            "rationale": review.rationale,
            "evidence": evidence,
            "errors": state.get("errors", []) + [f"self_review: unexpected error ({e}), used deterministic fallback"],
        }
