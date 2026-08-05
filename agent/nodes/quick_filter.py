# Cheap, non-backtest filtering from the raw combinatorial grid down to a
# target count BEFORE anyone pays for a real /backtest call. Everything
# here is in-process, no HTTP, no LLM — milliseconds even over a few
# hundred specs.
from collections import defaultdict

from state import AgentState
from nodes.dimensions import TREND_FILTERS, ENTRY_TRIGGERS, CONFIRMATIONS
from dsl_utils import rule_signature

DEFAULT_TARGET_COUNT = 45

# Style pairs that structurally contradict each other under the DSL's AND
# semantics — a persistent-uptrend gate (trend) ANDed with a pullback/
# oversold-designed trigger (mean_reversion) tends to starve trade count
# in a short (~60-98 day) backtest window. Same failure class as the
# VWAP-crosses bug documented in templates.py: not a crash, just a
# strategy that structurally never fires.
_INCOMPATIBLE_STYLE_PAIRS = [frozenset({"trend", "mean_reversion"})]


def _entry_leaf_signature(spec: dict) -> frozenset:
    """A set-based signature of a spec's effective ENTRY leaves only (trend
    filter + entry trigger + confirmation — no exit/risk). Using a SET
    (not a list/tuple) means a spec whose trend_filter and confirmation
    happen to resolve to the identical rule naturally collapses to the
    same signature as the equivalent spec using only one of them — no
    separate "redundant leaf" special case needed. Also directly
    comparable against dsl_utils.entry_leaf_signature() on a real deployed
    DSL, since both walk down to the same rule_signature() leaves."""
    rules = [ENTRY_TRIGGERS[spec["entry_trigger"]]["rule"]]
    if spec["trend_filter"] != "none":
        rules.append(TREND_FILTERS[spec["trend_filter"]]["rule"])
    if spec["confirmation"] != "none":
        rules.append(CONFIRMATIONS[spec["confirmation"]]["rule"])
    return frozenset(rule_signature(r) for r in rules)


def _spec_signature(spec: dict):
    """_entry_leaf_signature plus the exit/risk choice — used for
    within-batch structural dedup (see _dedupe_structural), where two
    different risk tiers of the same entry are legitimately distinct
    candidates worth backtesting separately."""
    return _entry_leaf_signature(spec) | {("exit", spec["exit_style"]), ("risk", spec["risk_tier"])}


def _dedupe_structural(specs: list) -> list:
    """Two specs collapse to the same signature whenever a trend_filter/
    confirmation resolves to a rule already covered by another leaf (e.g.
    entry_trigger="macd_bullish_cross" + confirmation="macd_positive" are
    literally the same rule — the engine's DSL validator rejects that
    exact duplicate-leaf case outright, confirmed live this session).
    Keep the spec with FEWER entry leaves when several share a signature —
    the simpler one is the one that actually passes validation; a more
    "constrained-looking" duplicate is just wasted structure, not a
    distinct strategy."""
    seen = {}
    for s in specs:
        key = frozenset(_spec_signature(s))
        if key not in seen or _entry_leaf_count(s) < _entry_leaf_count(seen[key]):
            seen[key] = s
    return list(seen.values())


def _spec_styles(spec: dict) -> set:
    styles = {ENTRY_TRIGGERS[spec["entry_trigger"]]["style"]}
    if spec["trend_filter"] != "none":
        styles.add(TREND_FILTERS[spec["trend_filter"]]["style"])
    if spec["confirmation"] != "none":
        styles.add(CONFIRMATIONS[spec["confirmation"]]["style"])
    return styles


def _is_compatible(spec: dict) -> bool:
    styles = _spec_styles(spec)
    return not any(pair.issubset(styles) for pair in _INCOMPATIBLE_STYLE_PAIRS)


def _entry_leaf_count(spec: dict) -> int:
    n = 1  # entry trigger always present
    if spec["trend_filter"] != "none":
        n += 1
    if spec["confirmation"] != "none":
        n += 1
    return n


def _heuristic_score(spec: dict) -> float:
    """Penalizes both under-constrained (1 leaf, too loose to be a real
    signal) and over-constrained (4+ leaves, ANDing that many conditions
    tends toward ~0 trades in a short window) entries. 2 leaves scores
    highest."""
    return -1.0 * (_entry_leaf_count(spec) - 2) ** 2


def _diverse_topn(specs: list, target_count: int) -> list:
    """Buckets by entry_trigger and takes the top scorers PER BUCKET first,
    so the survivors still span every trigger family the LLM selected —
    a flat top-N by heuristic score alone could let one dominant trigger
    family crowd out all the others."""
    if not specs:
        return []
    buckets = defaultdict(list)
    for s in specs:
        buckets[s["entry_trigger"]].append(s)
    for bucket in buckets.values():
        bucket.sort(key=_heuristic_score, reverse=True)

    per_bucket = max(1, -(-target_count // len(buckets)))  # ceil division
    survivors = []
    for bucket in buckets.values():
        survivors.extend(bucket[:per_bucket])

    survivors.sort(key=_heuristic_score, reverse=True)
    return survivors[:target_count]


def _not_already_deployed(specs: list, deployed_entry_signatures) -> list:
    """Drops any spec whose entry-only signature exactly matches a
    currently-deployed strategy's — stops generation from proposing
    something that structurally duplicates what's already live, on top of
    the within-batch dedup above (see gather_context.py for where
    deployed_entry_signatures comes from)."""
    if not deployed_entry_signatures:
        return specs
    deployed = set(deployed_entry_signatures)
    return [s for s in specs if _entry_leaf_signature(s) not in deployed]


def quick_filter(raw_specs: list, target_count: int = DEFAULT_TARGET_COUNT, deployed_entry_signatures=None) -> list:
    deduped = _dedupe_structural(raw_specs)
    compatible = [s for s in deduped if _is_compatible(s)]
    fresh = _not_already_deployed(compatible, deployed_entry_signatures)
    return _diverse_topn(fresh, target_count)


def quick_filter_node(state: AgentState) -> dict:
    """Graph node — filters expand_node's raw specs down to a backtest-able
    count before generate_dsl builds real DSL for each survivor."""
    raw_specs = state.get("raw_specs") or []
    deployed = state.get("deployed_entry_signatures") or []
    return {"candidate_specs": quick_filter(raw_specs, deployed_entry_signatures=deployed)}
