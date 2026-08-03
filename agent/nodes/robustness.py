"""Two cheap, data-only checks on the SELECTED candidate — no new engine
calls for Monte Carlo (it's pure math on trades the backtest already
returned), a bounded few extra /backtest calls for parameter stability.
Both run once, on the winner only, not on every candidate — the point is
"how much should I trust this specific pick," not a second tournament."""
import random

import clients
from state import AgentState
from nodes.lesson_keys import lesson_key

MONTE_CARLO_SHUFFLES = 200

# Perturbation deltas per DSL indicator type — only touches params the
# archetype's entry/exit rules actually reference, and only within a
# realistic tuning range (not enough to change the strategy's character).
PERTURBATIONS = {
    "ema_cross": [(-2, -2), (2, 2), (-4, 4)],   # (fast_delta, slow_delta)
    "supertrend": [(-2, -0.5), (2, 0.5), (-2, 0.5)],  # (period_delta, multiplier_delta)
    "donchian_channel": [(-5,), (5,), (10,)],   # (period_delta,)
    "bollinger_bands": [(-5,), (5,), (5,)],     # (period_delta,) — std_dev left alone, less standard to tune
    "rsi": [(-5,), (5,), (8,)],                 # (value_delta,) on the crosses_above threshold
}


def _shuffle_monte_carlo(trades: list, starting_capital: float) -> dict:
    """Real trade PnLs, reshuffled in random order N times. Total return is
    order-invariant (same numbers, different sequence) — what varies is the
    PATH: max drawdown and how early/late losses land. A strategy whose
    real (reported) drawdown is much better than most shuffles got there by
    a lucky sequence, not a structural edge."""
    pnls = [float(t.get("PnL", 0)) for t in trades if t.get("State") in ("CLOSED", "STOPPED", "TARGET_HIT")]
    if len(pnls) < 3:
        return {"ran": False, "reason": f"only {len(pnls)} closed trades, too few to shuffle meaningfully"}

    def max_drawdown_pct(order):
        equity = starting_capital
        peak = starting_capital
        max_dd = 0.0
        for pnl in order:
            equity += pnl
            peak = max(peak, equity)
            if peak > 0:
                max_dd = max(max_dd, (peak - equity) / peak * 100)
        return max_dd

    real_dd = max_drawdown_pct(pnls)
    shuffled_dds = []
    rng = random.Random(42)  # fixed seed — same trades always reshuffle the same way, reproducible not cherry-picked
    for _ in range(MONTE_CARLO_SHUFFLES):
        order = pnls[:]
        rng.shuffle(order)
        shuffled_dds.append(max_drawdown_pct(order))

    shuffled_dds.sort()
    worse_count = sum(1 for dd in shuffled_dds if dd > real_dd)
    percentile = round(100 * (1 - worse_count / len(shuffled_dds)))  # what % of shuffles were AS GOOD as the real order

    return {
        "ran": True,
        "real_max_drawdown_pct": round(real_dd, 2),
        "median_shuffled_drawdown_pct": round(shuffled_dds[len(shuffled_dds) // 2], 2),
        "worst_shuffled_drawdown_pct": round(shuffled_dds[-1], 2),
        "real_order_percentile": percentile,  # 100 = real order was the best-case drawdown of all orderings (suspicious/lucky); ~50 = typical
        "flag": "order-dependent (lucky sequencing)" if percentile >= 95 else None,
    }


def _find_perturbable_rule(dsl: dict) -> tuple:
    for block_name in ("entry", "exit"):
        block = dsl.get(block_name, {})
        for key in ("all", "any"):
            for rule in block.get(key, []) or []:
                ind = rule.get("indicator")
                if ind in PERTURBATIONS:
                    return block_name, key, rule, ind
    return None, None, None, None


def _perturbed_dsl(dsl: dict, block_name: str, key: str, target_rule: dict, indicator: str, deltas: tuple) -> dict:
    import copy
    d = copy.deepcopy(dsl)
    for rule in d.get(block_name, {}).get(key, []) or []:
        if rule.get("indicator") != indicator:
            continue
        if indicator == "ema_cross":
            rule["fast"] = max(2, rule.get("fast", 20) + deltas[0])
            rule["slow"] = max(rule["fast"] + 1, rule.get("slow", 50) + deltas[1])
        elif indicator == "supertrend":
            rule["period"] = max(2, rule.get("period", 10) + deltas[0])
            rule["multiplier"] = max(0.5, rule.get("multiplier", 3) + deltas[1])
        elif indicator == "donchian_channel":
            rule["period"] = max(2, rule.get("period", 20) + deltas[0])
        elif indicator == "bollinger_bands":
            rule["period"] = max(2, rule.get("period", 20) + deltas[0])
        elif indicator == "rsi" and "value" in rule:
            rule["value"] = max(1, min(99, rule.get("value", 32) + deltas[0]))
        break
    return d


def parameter_stability(dsl: dict, candles: list, benchmark: float, starting_capital: float = 100000) -> dict:
    """Reruns the winning candidate 3 more times with nearby parameter
    values. A real edge should survive small tuning changes; if Sharpe
    swings from strongly positive to negative on a ±10-20% parameter
    nudge, that's fragility/overfitting to this exact parameter choice,
    not a robust signal."""
    block_name, key, rule, indicator = _find_perturbable_rule(dsl)
    if not indicator:
        return {"ran": False, "reason": "no perturbable indicator found in entry/exit rules"}

    primary_tf = dsl.get("timeframe", "1d")
    sharpes = []
    for deltas in PERTURBATIONS[indicator]:
        variant = _perturbed_dsl(dsl, block_name, key, rule, indicator, deltas)
        variant["strategy_id"] = variant["strategy_id"] + "-perturb-" + "-".join(str(x) for x in deltas)
        try:
            result = clients.run_backtest(variant, {primary_tf: candles}, starting_capital, benchmark)
            sharpes.append(result.get("metrics", {}).get("Sharpe", 0))
        except Exception:
            continue

    if not sharpes:
        return {"ran": False, "reason": "all perturbation backtests failed"}

    return {
        "ran": True,
        "indicator_perturbed": indicator,
        "perturbed_sharpes": [round(s, 2) for s in sharpes],
        "min_sharpe": round(min(sharpes), 2),
        "max_sharpe": round(max(sharpes), 2),
        "flag": "unstable — Sharpe flips sign under small parameter changes" if min(sharpes) < 0 <= max(sharpes) or max(sharpes) < 0 else None,
    }


# Disclosed formula, not a fabricated single number — same principle this
# project already applies to RegimeContext/RecommendationContext (Go side):
# every point traces to something real and shown, never a vibe.
#   30 pts — cleared backtest quality gates (rank.py) at all
#   15 pts — Monte Carlo: real trade order wasn't a lucky-drawdown outlier
#   15 pts — parameter stability: Sharpe doesn't flip sign on a small nudge
#   15 pts — memory: this archetype's own real historical success rate in
#            this style (no history yet = half-credit, neutral — neither
#            penalized nor credited for something unproven)
#   25 pts — real paper/live trading evidence. Always 0 at generation time
#            (the strategy hasn't been deployed yet — there IS no live
#            evidence to have), which caps every pre-deployment score at
#            75/100 no matter how clean the backtest is. A strategy should
#            never show 100% confidence before it has actually traded.
# Benchmark comparison is deliberately NOT scored here — see rank.py's
# comment on why "beat buy-and-hold" would reject every timing strategy
# this project can produce. It's disclosed in the rationale instead.
MAX_PRE_DEPLOYMENT_SCORE = 75  # 100 - the 25 reserved for live evidence


def compute_confidence(gate_passed: bool, monte_carlo: dict, stability: dict, memory_confidence) -> dict:
    backtest_pts = 30 if gate_passed else 0

    mc_pts = 0
    if monte_carlo.get("ran"):
        mc_pts = 15 if not monte_carlo.get("flag") else 7

    stab_pts = 0
    if stability.get("ran"):
        stab_pts = 15 if not stability.get("flag") else 7

    if memory_confidence is None:
        mem_pts = 7
    else:
        mem_pts = round(memory_confidence * 15)

    total = backtest_pts + mc_pts + stab_pts + mem_pts
    return {
        "score": total,
        "max_pre_deployment_score": MAX_PRE_DEPLOYMENT_SCORE,
        "breakdown": {
            "backtest_gates": backtest_pts, "monte_carlo": mc_pts,
            "parameter_stability": stab_pts, "memory_track_record": mem_pts,
            "paper_live_evidence": 0,
        },
        "basis": (
            "30 backtest gates + 15 Monte Carlo (order not lucky) + 15 parameter stability + 15 memory track "
            "record (7 if no history yet) + 25 paper/live trading evidence (always 0 before deployment — "
            f"max possible before this strategy has ever traded live is {MAX_PRE_DEPLOYMENT_SCORE}/100)"
        ),
    }


def assess(state: AgentState) -> dict:
    """Runs Monte Carlo + parameter stability + confidence scoring on
    whatever guardrails.py selected — a no-op (all zeros/None, confidence
    reflects only the memory factor) when nothing survived, since there's
    nothing to stress-test."""
    selected = state.get("selected")
    if not selected or not selected.get("backtest"):
        return {}

    bt = selected["backtest"]
    style = state["style"]
    dsl = selected["dsl"]
    starting_capital = 100000
    benchmark = bt.get("metrics", {}).get("BenchmarkReturn", 0)

    monte_carlo = _shuffle_monte_carlo(bt.get("trades") or [], starting_capital)

    tf = dsl.get("timeframe", "1d")
    try:
        candles = clients.sample_history_intraday(tf) if style == "intraday" else clients.sample_history()
    except Exception:
        candles = []
    stability = parameter_stability(dsl, candles, benchmark, starting_capital) if candles else {"ran": False, "reason": "could not refetch candles for perturbation runs"}

    dc = state.get("decision_context") or {}
    lessons = dc.get("lessons") or []
    key = lesson_key(selected["archetype"], style)
    lesson = next((l for l in lessons if isinstance(l, dict) and l.get("key") == key), None)
    memory_confidence = lesson.get("confidence") if lesson else None

    confidence = compute_confidence(bool(selected.get("gate_passed")), monte_carlo, stability, memory_confidence)

    selected["monte_carlo"] = monte_carlo
    selected["stability"] = stability
    selected["confidence"] = confidence
    return {"selected": selected}
