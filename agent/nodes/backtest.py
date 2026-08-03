from langgraph.types import StreamWriter

from state import AgentState
from dsl_utils import required_timeframes
import clients


def _emit(writer: StreamWriter, candidate: dict, status: str, detail: str = "") -> None:
    # No-op unless the caller is actually consuming stream_mode="custom"
    # (api.py's /generate/stream is) — LangGraph injects a real callable
    # either way, so this is safe to call unconditionally.
    writer({
        "type": "backtest_candidate",
        "name": candidate.get("name", candidate.get("archetype", "?")),
        "status": status,  # "running" | "done" | "skipped"
        "detail": detail,
    })


def _fetch_candles(style: str, tf: str, cache: dict) -> list:
    """Fetches one timeframe's bundled sample data, cached across
    candidates in this run — several candidates typically share the same
    default timeframe (and, once cross-timeframe archetypes exist, the
    same secondary timeframe too)."""
    if tf in cache:
        return cache[tf]
    if style == "intraday":
        # Real intraday history, bundled per-timeframe to whatever depth
        # Yahoo's own retention allows (~60 real days for 5m, not a
        # synthesized 5-year series — no free source gives more).
        candles = clients.sample_history_intraday(tf)
    else:
        # Only daily (1d) swing sample data is bundled today — a swing
        # rule referencing any other timeframe will raise here, which is
        # correct: there's no data to honor that request with yet.
        candles = clients.sample_history()
    cache[tf] = candles
    return candles


def backtest(state: AgentState, writer: StreamWriter) -> dict:
    style = state["style"]
    candidates = state["candidates"]
    candles_cache = {}

    for c in candidates:
        if not c.get("valid"):
            c["backtest"] = None
            _emit(writer, c, "skipped", "failed validation")
            continue

        _emit(writer, c, "running")
        try:
            # Every timeframe this candidate's own rules reference (always
            # at least its own declared timeframe; more if any rule leaf
            # overrides it — see dsl_utils.required_timeframes) — fetched
            # so the engine's /backtest can actually evaluate every leaf
            # instead of silently treating an unsupplied timeframe's rule
            # as permanently unresolved.
            tfs = required_timeframes(c["dsl"])
            candles_by_tf = {tf: _fetch_candles(style, tf, candles_cache) for tf in tfs}

            primary_tf = c["dsl"].get("timeframe", "5m" if style == "intraday" else "1d")
            benchmark = clients.compute_benchmark_return(candles_by_tf[primary_tf])
            c["backtest"] = clients.run_backtest(c["dsl"], candles_by_tf, benchmark_return_pct=benchmark)
            m = c["backtest"].get("metrics", {})
            _emit(writer, c, "done", f"{m.get('TotalTrades', 0)} trades, Sharpe {m.get('Sharpe', 0):.2f}")
        except Exception as e:
            c["backtest"] = None
            c["validation_errors"] = c.get("validation_errors", []) + [f"backtest failed: {e}"]
            _emit(writer, c, "done", f"failed: {e}")

    return {"candidates": candidates}
