from langgraph.types import StreamWriter

from state import AgentState
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


def backtest(state: AgentState, writer: StreamWriter) -> dict:
    style = state["style"]
    candidates = state["candidates"]
    intraday_candles_cache = {}

    for c in candidates:
        if not c.get("valid"):
            c["backtest"] = None
            _emit(writer, c, "skipped", "failed validation")
            continue

        _emit(writer, c, "running")
        try:
            if style == "intraday":
                # Real intraday history, bundled per-timeframe to whatever
                # depth Yahoo's own retention allows (~60 real days for 5m,
                # not a synthesized 5-year series — no free source gives
                # more). Cached per timeframe within this run since several
                # candidates typically share the same "5m" default.
                tf = c["dsl"].get("timeframe", "5m")
                if tf not in intraday_candles_cache:
                    intraday_candles_cache[tf] = clients.sample_history_intraday(tf)
                candles = intraday_candles_cache[tf]
            else:
                if "swing" not in intraday_candles_cache:
                    intraday_candles_cache["swing"] = clients.sample_history()
                candles = intraday_candles_cache["swing"]

            benchmark = clients.compute_benchmark_return(candles)
            c["backtest"] = clients.run_backtest(c["dsl"], candles, benchmark_return_pct=benchmark)
            m = c["backtest"].get("metrics", {})
            _emit(writer, c, "done", f"{m.get('TotalTrades', 0)} trades, Sharpe {m.get('Sharpe', 0):.2f}")
        except Exception as e:
            c["backtest"] = None
            c["validation_errors"] = c.get("validation_errors", []) + [f"backtest failed: {e}"]
            _emit(writer, c, "done", f"failed: {e}")

    return {"candidates": candidates}
