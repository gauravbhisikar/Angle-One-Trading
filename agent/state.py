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

    # plan
    plan: dict           # {candidates: [{name, archetype, rationale}], research_needed, research_queries}

    # research (optional)
    research_findings: list

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
