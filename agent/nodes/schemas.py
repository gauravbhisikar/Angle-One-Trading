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

ARCHETYPES = [
    "momentum", "trend_following", "pullback",
    "mean_reversion", "volatility_expansion", "hybrid_momentum",
]

INTRADAY_ARCHETYPES = [
    "vwap_reversion", "vwap_trend", "ema_trend", "supertrend", "donchian_adx",
    "bollinger_reversion", "macd_momentum", "rsi_pullback", "volume_breakout", "ema_vwap_hybrid",
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


class IntradayCandidatePlan(BaseModel):
    archetype: Literal[
        "vwap_reversion", "vwap_trend", "ema_trend", "supertrend", "donchian_adx",
        "bollinger_reversion", "macd_momentum", "rsi_pullback", "volume_breakout", "ema_vwap_hybrid",
    ]
    risk: Literal["conservative", "moderate", "aggressive"]
    rationale: str = Field(description="Why this archetype fits the current intraday market context, one sentence.")


class IntradayPlan(BaseModel):
    candidates: list[IntradayCandidatePlan] = Field(
        description="3-8 intraday archetype candidates to backtest, drawn only from the fixed archetype list. "
                    "Never just one — a real tournament needs multiple candidates to compare. Any archetype the "
                    "avoid-list flags as historically poor for this regime should only be included with a "
                    "specific reason grounded in the current data to override that history.",
        min_length=3, max_length=8,
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
