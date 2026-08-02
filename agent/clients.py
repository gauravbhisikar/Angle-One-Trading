"""Thin HTTP clients for the two Go services the agent talks to.

The agent never imports Go code (can't) and never talks to connectors/
or memory/ directly — everything routes through contextbuilder-server
(context, research, memory reads/writes) or the engine (validate,
backtest, strategy CRUD/lifecycle). Same HTTP-boundary architecture as
the rest of this project.
"""
import os

import httpx
from dotenv import load_dotenv

load_dotenv(os.path.join(os.path.dirname(__file__), "..", ".env"))

ENGINE_URL = os.getenv("ENGINE_URL", "http://localhost:9080")
CONTEXTBUILDER_URL = os.getenv("CONTEXTBUILDER_URL", "http://localhost:9090")

_client = httpx.Client(timeout=30.0)


def build_context(task: str, symbol: str, user_preferences: dict) -> dict:
    r = _client.post(
        f"{CONTEXTBUILDER_URL}/context/build",
        json={"task": task, "symbol": symbol, "user_preferences": user_preferences},
    )
    r.raise_for_status()
    return r.json()


def research(query: str, max_results: int = 5) -> list:
    r = _client.post(
        f"{CONTEXTBUILDER_URL}/research/query",
        json={"query": query, "max_results": max_results},
    )
    r.raise_for_status()
    return r.json().get("findings", [])


def validate_strategy(dsl: dict) -> dict:
    """Returns {"valid": bool, "errors": [...], "warnings": [...]}."""
    r = _client.post(f"{ENGINE_URL}/strategies/validate", json=dsl)
    data = r.json()
    if r.status_code not in (200, 422):
        r.raise_for_status()
    return data


def sample_history() -> list:
    r = _client.get(f"{ENGINE_URL}/backtest/sample-data")
    r.raise_for_status()
    return r.json()


def sample_history_intraday(timeframe: str) -> list:
    """Real Yahoo intraday candles, bundled per-timeframe (1m/5m/15m/30m/1h)
    to whatever depth Yahoo's own retention actually allows — see
    engine/internal/api/dashboard.go. Raises if the timeframe isn't bundled."""
    r = _client.get(f"{ENGINE_URL}/backtest/sample-data-intraday", params={"timeframe": timeframe})
    r.raise_for_status()
    return r.json()


def compute_benchmark_return(candles: list) -> float:
    """Real buy-and-hold return over the exact candle set a backtest just
    ran against — computed here (not by the engine, which never fetches
    or derives benchmark data itself) so it's always an apples-to-apples
    comparison against that specific run's period."""
    if len(candles) < 2:
        return 0.0
    first, last = candles[0]["close"], candles[-1]["close"]
    if not first:
        return 0.0
    return (last - first) / first * 100


def run_backtest(dsl: dict, candles: list, starting_capital: float = 100000, benchmark_return_pct: float = 0.0) -> dict:
    r = _client.post(
        f"{ENGINE_URL}/backtest",
        json={
            "strategy": dsl, "candles": candles, "starting_capital": starting_capital,
            "benchmark_return_pct": benchmark_return_pct,
        },
    )
    r.raise_for_status()
    return r.json()


def create_strategy(dsl: dict) -> dict:
    r = _client.post(f"{ENGINE_URL}/strategies", json=dsl)
    r.raise_for_status()
    return r.json()


def run_strategy(strategy_id: str) -> dict:
    r = _client.post(f"{ENGINE_URL}/strategies/{strategy_id}/run")
    r.raise_for_status()
    return r.json()


def save_strategy_memory(record: dict) -> None:
    r = _client.post(f"{CONTEXTBUILDER_URL}/memory/strategy", json=record)
    r.raise_for_status()


def save_context_memory(snapshot: dict) -> None:
    r = _client.post(f"{CONTEXTBUILDER_URL}/memory/context", json=snapshot)
    r.raise_for_status()


def save_backtest_memory(record: dict) -> None:
    r = _client.post(f"{CONTEXTBUILDER_URL}/memory/backtest", json=record)
    r.raise_for_status()


def save_deployment_memory(record: dict) -> None:
    r = _client.post(f"{CONTEXTBUILDER_URL}/memory/deployment", json=record)
    r.raise_for_status()


def record_lesson(key: str, description: str, success: bool) -> None:
    r = _client.post(
        f"{CONTEXTBUILDER_URL}/memory/lesson",
        json={"key": key, "description": description, "success": success},
    )
    r.raise_for_status()


def get_lessons() -> list:
    r = _client.get(f"{CONTEXTBUILDER_URL}/memory/lessons")
    r.raise_for_status()
    return r.json()
