"""Shared state that flows through every node in the LangGraph graph.

Kept as a plain TypedDict (not a class with methods) — LangGraph merges
state updates by key, so nodes only return the keys they changed.
"""
from typing import TypedDict, Any, Optional


class Candidate(TypedDict, total=False):
    name: str
    archetype: str
    rationale: str
    dsl: dict
    validation_errors: list
    valid: bool
    backtest: dict  # raw /backtest response (metrics, equity_curve, trades count, etc)
    score: float


class AgentState(TypedDict, total=False):
    # Input
    style: str          # "swing" | "intraday"
    symbol: str
    task: str

    # gather_context
    decision_context: dict

    # plan — swing: {candidates: [{archetype, risk, holding_days, rationale}], research_needed, research_queries}
    # intraday: DimensionSelectionPlan shape (nodes/schemas.py) — trend_filters/
    # entry_triggers/confirmations/exit_styles/risk_tiers, research_needed, research_queries
    plan: dict

    # research (optional)
    research_findings: list

    # expand_node / quick_filter_node — intraday only (nodes/combinatorics.py,
    # nodes/quick_filter.py); swing never populates these
    raw_specs: list[dict]        # expand_grid's cartesian product, pre-filter
    candidate_specs: list[dict]  # quick_filter's survivors, what generate_dsl consumes

    # generate_dsl / validate loop
    candidates: list[Candidate]
    retry_count: int
    max_retries: int

    # backtest + rank
    ranked: list[Candidate]
    selected: Optional[Candidate]

    # guardrails (deterministic, post-rank, pre-self_review)
    guardrail_reasons: list[str]  # non-empty only if a candidate that passed backtest gates got demoted here

    # self_review
    description: str
    rationale: str
    evidence: dict

    # bookkeeping
    llm_used: bool       # False if OPENROUTER_API_KEY missing and template fallback ran instead
    errors: list[str]
