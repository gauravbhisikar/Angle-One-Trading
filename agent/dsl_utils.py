# Python port of the Go engine's collectLeaves/RequiredTimeframes
# (engine/internal/strategy/runtime.go:113-171) — walks a DSL's entry/exit
# condition trees and reports every timeframe they reference (the
# strategy's own default plus any per-rule override), so a caller can
# fetch/cache candles for each before backtesting a strategy that spans
# more than one timeframe. Mirrors the Go side's exact leaf-detection
# rules (dsl/condition.go's reservedRuleKeys, UnmarshalJSON's all/any/not
# dispatch) so a rule this reports as needed is exactly what the engine
# itself would also subscribe to.
def _walk(cond, default_tf: str, out: set):
    if not cond:
        return
    if "all" in cond:
        for c in cond["all"] or []:
            _walk(c, default_tf, out)
        return
    if "any" in cond:
        for c in cond["any"] or []:
            _walk(c, default_tf, out)
        return
    if "not" in cond:
        _walk(cond["not"], default_tf, out)
        return
    # Leaf rule. take_profit/stop_loss/trailing_sl are exit-only shorthand
    # with no indicator to subscribe to (engine/internal/strategy/
    # runtime.go:135-137's early return).
    if "take_profit" in cond or "stop_loss" in cond or "trailing_sl" in cond:
        return
    out.add(cond.get("timeframe") or default_tf)


def required_timeframes(dsl: dict) -> set:
    """Every timeframe this DSL's entry/exit rules reference, including
    the strategy's own default — always non-empty for a valid strategy."""
    default_tf = dsl.get("timeframe", "1d")
    out = {default_tf}
    _walk(dsl.get("entry"), default_tf, out)
    _walk(dsl.get("exit"), default_tf, out)
    return out


def rule_signature(rule):
    """Hashable, rounded canonical form of one leaf rule fragment —
    collapses float-precision/key-ordering noise, not full semantic
    equivalence. Shared by nodes/quick_filter.py (deduping candidate specs
    against each other) and nodes/gather_context.py (checking a new
    candidate against what's already deployed) — same notion of
    "structurally the same rule" either way."""
    if rule is None:
        return None
    compare_to = None
    if rule.get("compare_to"):
        compare_to = rule["compare_to"].get("indicator")
    extra = tuple(sorted(
        (k, round(v, 4) if isinstance(v, (int, float)) else v)
        for k, v in rule.items()
        if k not in ("indicator", "operator", "value", "compare_to")
    ))
    return (rule.get("indicator"), rule.get("operator"), rule.get("value"), compare_to, extra)


def _collect_entry_leaf_signatures(cond, out: set):
    if not cond:
        return
    if "all" in cond:
        for c in cond["all"] or []:
            _collect_entry_leaf_signatures(c, out)
        return
    if "any" in cond:
        for c in cond["any"] or []:
            _collect_entry_leaf_signatures(c, out)
        return
    if "not" in cond:
        _collect_entry_leaf_signatures(cond["not"], out)
        return
    if "take_profit" in cond or "stop_loss" in cond or "trailing_sl" in cond:
        return  # exit-only shorthand, not a real entry condition
    out.add(rule_signature(cond))


def entry_leaf_signature(dsl: dict) -> frozenset:
    """A structural fingerprint of a DSL's ENTRY condition tree only (not
    exit/risk) — two strategies with the same fingerprint fire on the same
    indicators/operators/thresholds even if named or risk-sized
    differently. Used to stop generation from proposing something that
    structurally duplicates a strategy already deployed (see
    gather_context.py / nodes/quick_filter.py)."""
    out = set()
    _collect_entry_leaf_signatures(dsl.get("entry"), out)
    return frozenset(out)
