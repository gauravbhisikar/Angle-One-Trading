"""Pydantic schemas for structured LLM output (langchain's
with_structured_output). Keeping the LLM's job narrow and code executing
precisely — the model picks FROM a known archetype menu + tunes risk/
holding, it does not hand-write raw DSL JSON. Free-form DSL generation
from scratch is a real capability gap, not silently claimed — see
agent/BACKLOG.md: against a strict schema, letting an LLM write raw DSL
risks a validate-retry loop that provides no real benefit over a
known-working template, so V1 scopes the LLM's actual judgment to
"which archetype fits this regime, how aggressive," not "invent syntax."
"""
from typing import Literal
from pydantic import BaseModel, Field

from nodes.dimensions import TREND_FILTER_NAMES, ENTRY_TRIGGER_NAMES, CONFIRMATION_NAMES, EXIT_STYLE_NAMES

ARCHETYPES = [
    "momentum", "trend_following", "pullback",
    "mean_reversion", "volatility_expansion", "hybrid_momentum",
]



class CandidatePlan(BaseModel):
    archetype: Literal[
        "momentum", "trend_following", "pullback",
        "mean_reversion", "volatility_expansion", "hybrid_momentum",
    ]
    risk: Literal["conservative", "moderate", "aggressive"]
    holding_days: int = Field(ge=1, le=90)
    rationale: str = Field(description="Why this archetype fits the current market context, one sentence.")


class Plan(BaseModel):
    candidates: list[CandidatePlan] = Field(
        description="2-6 swing archetype candidates to backtest, drawn only from the fixed archetype list. "
                    "Never just one — a real tournament needs multiple candidates to compare, and any archetype "
                    "the avoid-list flags as historically poor for this regime should only be included with a "
                    "specific reason grounded in the current data to override that history.",
        min_length=2, max_length=6,
    )
    research_needed: bool = Field(
        description="True only if the decision context has something unusual/conflicting a curated news/RBI feed could clarify (e.g. high VIX with no obvious cause, an unexplained regime shift). False for an ordinary/clear market read."
    )
    research_queries: list[str] = Field(
        default_factory=list,
        description="1-3 short keyword queries for the curated news/RBI feed search, only if research_needed.",
        max_length=3,
    )


# Replaces the old fixed-archetype IntradayPlan/IntradayCandidatePlan:
# the LLM no longer picks named archetypes for intraday, it picks which
# VALUES on each of 4 axes are worth trying today — nodes/combinatorics.py
# then cartesian-expands the selection into individually parameterized
# DSL candidates (dozens, not single digits). Each list here stays small
# and Literal-typed (same order of magnitude as the old archetype picks)
# — the LLM's structured output never has to emit the actual candidate
# count, that's handled entirely in plain Python downstream.
class DimensionSelectionPlan(BaseModel):
    trend_filters: list[Literal[*TREND_FILTER_NAMES]] = Field(
        description="Which trend-filter values to try today — 'none' is always a valid choice for a pure mean-reversion/breakout approach.",
        min_length=1, max_length=4,
    )
    entry_triggers: list[Literal[*ENTRY_TRIGGER_NAMES]] = Field(
        description="Which entry-trigger values to try today — pick a genuine mix (mean-reversion, breakout, trend-follow), not near-duplicates.",
        min_length=2, max_length=6,
    )
    confirmations: list[Literal[*CONFIRMATION_NAMES]] = Field(
        description="Which confirmation values to try today — 'none' is always a valid choice.",
        min_length=1, max_length=3,
    )
    exit_styles: list[Literal[*EXIT_STYLE_NAMES]] = Field(
        description="Which exit-style values to try today — 'tp_sl_only' is always a valid choice.",
        min_length=1, max_length=3,
    )
    risk_tiers: list[Literal["conservative", "moderate", "aggressive"]] = Field(min_length=1, max_length=3)
    regime_rationale: str = Field(
        description="One or two sentences: which specific piece of today's context (trend, breadth, VIX, FII/DII, "
                    "sentiment, regime) supports these particular axis choices."
    )
    research_needed: bool = Field(
        description="True only if the decision context has something unusual/conflicting a curated news/RBI feed could clarify. False for an ordinary/clear market read."
    )
    research_queries: list[str] = Field(default_factory=list, max_length=3)


class SelfReview(BaseModel):
    description: str = Field(description="1-2 sentences: what this strategy does.")
    rationale: str = Field(
        description="Why this candidate was selected over the others, grounded ONLY in the real backtest numbers "
                    "and market context provided — never invent a statistic not given to you."
    )
    weaknesses: list[str] = Field(default_factory=list, description="Real weaknesses visible in the given numbers, e.g. low trade count, high drawdown.")
