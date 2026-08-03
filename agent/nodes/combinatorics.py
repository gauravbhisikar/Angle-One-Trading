# Pure Python — no LLM, no HTTP. Cartesian-expands a DimensionSelectionPlan
# (nodes/schemas.py) into individually parameterized DSL candidate specs,
# then composes each spec into a full DSL dict. This is what actually
# produces dozens of candidates from a handful of LLM-picked axis values —
# the LLM never sees or produces a candidate count.
import copy
import itertools

from state import AgentState
from nodes.dimensions import TREND_FILTERS, ENTRY_TRIGGERS, CONFIRMATIONS, EXIT_STYLES
from nodes.templates import _intraday_base, _tpsl_intraday


def expand_grid(selection: dict) -> list:
    """Cartesian product over trend_filter x entry_trigger x confirmation x
    exit_style x risk_tier. Returns raw spec dicts, not DSL yet."""
    combos = itertools.product(
        selection.get("trend_filters") or ["none"],
        selection.get("entry_triggers") or [],
        selection.get("confirmations") or ["none"],
        selection.get("exit_styles") or ["tp_sl_only"],
        selection.get("risk_tiers") or ["moderate"],
    )
    return [
        {"trend_filter": tf, "entry_trigger": et, "confirmation": cf, "exit_style": ex, "risk_tier": rt}
        for tf, et, cf, ex, rt in combos
    ]


def spec_name(spec: dict) -> str:
    parts = [spec["entry_trigger"]]
    if spec["trend_filter"] != "none":
        parts.append(spec["trend_filter"])
    if spec["confirmation"] != "none":
        parts.append(spec["confirmation"])
    return "_".join(parts)


def spec_archetype_key(spec: dict) -> str:
    """A stable, opaque string identifying this exact axis combination —
    used everywhere a single named "archetype" string used to be (memory
    lessons, rank.py display, guardrails' avoid-list check), so none of
    that downstream code needs to know combinatorial specs exist."""
    return f"{spec['entry_trigger']}__{spec['trend_filter']}__{spec['confirmation']}__{spec['exit_style']}"


def build_dsl(spec: dict, strategy_id: str, timeframe: str = "5m") -> dict:
    """Composes one full intraday DSL dict from a spec — reuses
    templates.py's existing skeleton (_intraday_base/_tpsl_intraday, the
    same session/holding/position-sizing scaffolding every named archetype
    already used) and dimensions.py's rule fragments. Every fragment is
    deep-copied so candidates never share mutable nested dicts."""
    name = spec_name(spec).replace("_", " ").title() + " (Intraday)"
    d, rp = _intraday_base(strategy_id, name, timeframe, spec["risk_tier"])

    entry_rules = [copy.deepcopy(ENTRY_TRIGGERS[spec["entry_trigger"]]["rule"])]
    tf_rule = TREND_FILTERS[spec["trend_filter"]]["rule"]
    if tf_rule:
        entry_rules.append(copy.deepcopy(tf_rule))
    cf_rule = CONFIRMATIONS[spec["confirmation"]]["rule"]
    if cf_rule:
        entry_rules.append(copy.deepcopy(cf_rule))
    d["entry"] = {"all": entry_rules}

    exit_rules = [copy.deepcopy(r) for r in EXIT_STYLES[spec["exit_style"]]] + _tpsl_intraday(rp)
    d["exit"] = {"any": exit_rules}
    return d


def expand_node(state: AgentState) -> dict:
    """Graph node — only ever reached for intraday (see graph.py's
    routing). Cartesian-expands plan_node's DimensionSelectionPlan into
    raw specs, handed to quick_filter_node next."""
    return {"raw_specs": expand_grid(state["plan"])}
