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
