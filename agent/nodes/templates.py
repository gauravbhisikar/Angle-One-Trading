"""Deterministic DSL archetype templates — ported from the Strategy Lab's
client-side generator (engine/internal/api/web/dashboard.html), which was
live-tested this session against 5 real years of NIFTYBEES data: all 6
produce real trades (Momentum 9, Trend 11, Pullback 11, MeanRev 14,
VolExp 15, Hybrid 12).

Used two ways:
1. As the offline fallback when no LLM key is configured yet (state
   marks llm_used=False so callers know this ran, not real reasoning).
2. As a starting point the LLM path can also draw on/deviate from —
   these are known-working DSL shapes, not just a placeholder.

Each takes (holding_days, risk) where risk is "conservative" | "moderate"
| "aggressive", matching the same three tiers the wizard used to offer.
"""

RISK_PARAMS = {
    "conservative": {"size": 5, "sl": 3, "tp": 6},
    "moderate": {"size": 10, "sl": 5, "tp": 10},
    "aggressive": {"size": 20, "sl": 8, "tp": 16},
}


def _base(strategy_id: str, name: str, holding_days: int, strategy_type: str = "swing") -> dict:
    return {
        "version": "1.2",
        "strategy_id": strategy_id,
        "strategy_name": name,
        "strategy_version": 1,
        "type": strategy_type,
        "asset_type": "ETF",
        "direction": "long",
        "enabled": True,
        "timeframe": "1d",
        "symbols": ["NIFTYBEES"],
        "execution": {
            "mode": "paper", "broker": "angel", "exchange": "NSE", "product": "CNC",
            "order_type": "MARKET", "entry": "market", "slippage_pct": 0.05,
        },
        "risk": {"max_daily_loss": 5, "max_positions": 1},
        "holding": {"max_days": holding_days},
        "cost_model": "angel_equity",
        "benchmark": "NIFTYBEES",
        "metadata": {"author": "agent", "description": f"{name} candidate"},
    }


def _tpsl(rp):
    return [{"take_profit": rp["tp"]}, {"stop_loss": rp["sl"]}]


def momentum(strategy_id: str, holding_days: int, risk: str) -> dict:
    rp = RISK_PARAMS[risk]
    d = _base(strategy_id, "Momentum Breakout", holding_days)
    d["entry"] = {"all": [{"indicator": "ema_cross", "fast": 20, "slow": 50, "operator": "bullish"}]}
    d["exit"] = {"any": [{"indicator": "ema_cross", "operator": "bearish"}] + _tpsl(rp)}
    d["position_sizing"] = {"type": "fixed_pct", "value": rp["size"]}
    return d


def trend_following(strategy_id: str, holding_days: int, risk: str) -> dict:
    rp = RISK_PARAMS[risk]
    d = _base(strategy_id, "Trend Following", holding_days)
    d["entry"] = {"all": [{"indicator": "supertrend", "period": 10, "multiplier": 3, "operator": "bullish"}]}
    d["exit"] = {"any": [{"indicator": "supertrend", "operator": "bearish"}] + _tpsl(rp)}
    d["position_sizing"] = {"type": "fixed_pct", "value": rp["size"]}
    return d


def pullback(strategy_id: str, holding_days: int, risk: str) -> dict:
    rp = RISK_PARAMS[risk]
    d = _base(strategy_id, "Pullback Strategy", holding_days)
    d["entry"] = {"all": [{"indicator": "rsi", "operator": "crosses_above", "value": 32}]}
    d["exit"] = {"any": _tpsl(rp)}
    d["position_sizing"] = {"type": "fixed_pct", "value": rp["size"]}
    return d


def mean_reversion(strategy_id: str, holding_days: int, risk: str) -> dict:
    rp = RISK_PARAMS[risk]
    d = _base(strategy_id, "Mean Reversion", holding_days)
    d["entry"] = {"all": [{"indicator": "bollinger_bands", "period": 20, "std_dev": 2, "operator": "price_below_lower"}]}
    d["exit"] = {"any": [{"indicator": "bollinger_bands", "operator": "price_above_upper"}] + _tpsl(rp)}
    d["position_sizing"] = {"type": "fixed_pct", "value": rp["size"]}
    return d


def volatility_expansion(strategy_id: str, holding_days: int, risk: str) -> dict:
    rp = RISK_PARAMS[risk]
    d = _base(strategy_id, "Volatility Expansion", holding_days)
    d["entry"] = {"all": [
        {"indicator": "donchian_channel", "period": 20, "operator": "breakout_up"},
        {"indicator": "adx", "period": 14, "operator": ">", "value": 20},
    ]}
    d["exit"] = {"any": [{"indicator": "supertrend", "operator": "bearish"}] + _tpsl(rp)}
    d["position_sizing"] = {"type": "fixed_pct", "value": rp["size"]}
    return d


def hybrid_momentum(strategy_id: str, holding_days: int, risk: str) -> dict:
    rp = RISK_PARAMS[risk]
    d = _base(strategy_id, "Hybrid Momentum", holding_days)
    d["entry"] = {"all": [
        {"indicator": "ema_cross", "fast": 12, "slow": 26, "operator": "bullish"},
        {"indicator": "adx", "period": 14, "operator": ">", "value": 18},
    ]}
    d["exit"] = {"any": [{"indicator": "macd", "operator": "bearish"}] + _tpsl(rp)}
    d["position_sizing"] = {"type": "fixed_pct", "value": rp["size"]}
    return d


def _intraday_base(strategy_id: str, name: str, timeframe: str, risk: str) -> tuple:
    """Common intraday skeleton — session window, force square-off, and
    TP/SL scaled down to a quarter of the swing risk tiers (intraday moves
    are smaller). Returns (dsl_dict, risk_params) so callers only need to
    fill in entry/exit."""
    rp = RISK_PARAMS[risk]
    d = _base(strategy_id, name, 0, strategy_type="intraday")
    d["timeframe"] = timeframe
    d["position_sizing"] = {"type": "fixed_pct", "value": rp["size"]}
    d["session"] = {"entry_start": "09:20", "entry_end": "14:45"}
    d["holding"] = {"force_square_off": "15:20"}
    return d, rp


def _tpsl_intraday(rp):
    return [{"take_profit": rp["tp"] / 4}, {"stop_loss": rp["sl"] / 4}]


# "close crosses_above/</>" + compare_to vwap, NEVER bare "vwap crosses_above"
# — the vwap indicator's Update() (engine/internal/indicators/vwap.go) only
# ever returns Value/Prev, never sets Flags["crosses_above"], so a bare
# vwap-crosses rule is unconditionally false (confirmed live: 0 trades over
# 60 real days of 5m data). Routing through compare_to uses the engine's
# generic threshold-crossing logic instead, which does work.
#
# All 10 archetypes below are live-verified non-buggy signals (real,
# non-zero trade counts against real bundled intraday data) — that is NOT
# the same as all of them being profitable. Live-tested 2026-08-02 against
# real 5m NIFTYBEES data (~60 real days): most came back net-losing or
# barely breakeven (e.g. Sharpe -11 to -30, profit factor 0-0.7) — only
# RSI Pullback cleared Sharpe>0 and PF>=1.0. That is real market evidence,
# not a bug: simple single/dual-indicator intraday signals on a short
# recent NIFTYBEES window mostly don't have edge. This is exactly why
# nodes/rank.py applies quality gates instead of presenting any candidate
# as "the pick" by default.

def vwap_reversion_intraday(strategy_id: str, risk: str, timeframe: str = "5m") -> dict:
    d, rp = _intraday_base(strategy_id, "VWAP Reversion (Intraday)", timeframe, risk)
    d["entry"] = {"all": [
        {"indicator": "close", "operator": "crosses_above", "compare_to": {"indicator": "vwap"}},
        {"indicator": "volume", "operator": "spike_pct", "value": 150},
    ]}
    d["exit"] = {"any": _tpsl_intraday(rp)}
    return d


def vwap_trend_intraday(strategy_id: str, risk: str, timeframe: str = "5m") -> dict:
    d, rp = _intraday_base(strategy_id, "VWAP Trend (Intraday)", timeframe, risk)
    d["entry"] = {"all": [
        {"indicator": "close", "operator": ">", "compare_to": {"indicator": "vwap"}},
        {"indicator": "ema_cross", "fast": 20, "slow": 50, "operator": "bullish"},
    ]}
    d["exit"] = {"any": [{"indicator": "close", "operator": "<", "compare_to": {"indicator": "vwap"}}] + _tpsl_intraday(rp)}
    return d


def ema_trend_intraday(strategy_id: str, risk: str, timeframe: str = "5m") -> dict:
    d, rp = _intraday_base(strategy_id, "EMA Trend Following (Intraday)", timeframe, risk)
    d["entry"] = {"all": [{"indicator": "ema_cross", "fast": 20, "slow": 50, "operator": "bullish"}]}
    d["exit"] = {"any": [{"indicator": "ema_cross", "operator": "bearish"}] + _tpsl_intraday(rp)}
    return d


def supertrend_intraday(strategy_id: str, risk: str, timeframe: str = "5m") -> dict:
    d, rp = _intraday_base(strategy_id, "SuperTrend Trend (Intraday)", timeframe, risk)
    d["entry"] = {"all": [{"indicator": "supertrend", "period": 10, "multiplier": 3, "operator": "bullish"}]}
    d["exit"] = {"any": [{"indicator": "supertrend", "operator": "bearish"}] + _tpsl_intraday(rp)}
    return d


def donchian_adx_intraday(strategy_id: str, risk: str, timeframe: str = "5m") -> dict:
    d, rp = _intraday_base(strategy_id, "Donchian Breakout (Intraday)", timeframe, risk)
    d["entry"] = {"all": [
        {"indicator": "donchian_channel", "period": 20, "operator": "breakout_up"},
        {"indicator": "adx", "period": 14, "operator": ">", "value": 20},
    ]}
    d["exit"] = {"any": [{"indicator": "supertrend", "operator": "bearish"}] + _tpsl_intraday(rp)}
    return d


def bollinger_reversion_intraday(strategy_id: str, risk: str, timeframe: str = "5m") -> dict:
    d, rp = _intraday_base(strategy_id, "Bollinger Mean Reversion (Intraday)", timeframe, risk)
    d["entry"] = {"all": [{"indicator": "bollinger_bands", "period": 20, "std_dev": 2, "operator": "price_below_lower"}]}
    d["exit"] = {"any": [{"indicator": "bollinger_bands", "operator": "price_above_upper"}] + _tpsl_intraday(rp)}
    return d


def macd_momentum_intraday(strategy_id: str, risk: str, timeframe: str = "5m") -> dict:
    d, rp = _intraday_base(strategy_id, "MACD Momentum (Intraday)", timeframe, risk)
    d["entry"] = {"all": [{"indicator": "macd", "operator": "bullish"}]}
    d["exit"] = {"any": [{"indicator": "macd", "operator": "bearish"}] + _tpsl_intraday(rp)}
    return d


def rsi_pullback_intraday(strategy_id: str, risk: str, timeframe: str = "5m") -> dict:
    d, rp = _intraday_base(strategy_id, "RSI Pullback (Intraday)", timeframe, risk)
    d["entry"] = {"all": [{"indicator": "rsi", "operator": "crosses_above", "value": 32}]}
    d["exit"] = {"any": _tpsl_intraday(rp)}
    return d


def volume_breakout_intraday(strategy_id: str, risk: str, timeframe: str = "5m") -> dict:
    d, rp = _intraday_base(strategy_id, "Volume Breakout (Intraday)", timeframe, risk)
    d["entry"] = {"all": [
        {"indicator": "donchian_channel", "period": 20, "operator": "breakout_up"},
        {"indicator": "volume", "operator": "spike_pct", "value": 150},
    ]}
    d["exit"] = {"any": [{"indicator": "supertrend", "operator": "bearish"}] + _tpsl_intraday(rp)}
    return d


def ema_vwap_hybrid_intraday(strategy_id: str, risk: str, timeframe: str = "5m") -> dict:
    d, rp = _intraday_base(strategy_id, "EMA + VWAP Hybrid (Intraday)", timeframe, risk)
    d["entry"] = {"all": [
        {"indicator": "ema_cross", "fast": 12, "slow": 26, "operator": "bullish"},
        {"indicator": "close", "operator": ">", "compare_to": {"indicator": "vwap"}},
    ]}
    d["exit"] = {"any": [{"indicator": "macd", "operator": "bearish"}] + _tpsl_intraday(rp)}
    return d


INTRADAY_ARCHETYPES = {
    "vwap_reversion": vwap_reversion_intraday,
    "vwap_trend": vwap_trend_intraday,
    "ema_trend": ema_trend_intraday,
    "supertrend": supertrend_intraday,
    "donchian_adx": donchian_adx_intraday,
    "bollinger_reversion": bollinger_reversion_intraday,
    "macd_momentum": macd_momentum_intraday,
    "rsi_pullback": rsi_pullback_intraday,
    "volume_breakout": volume_breakout_intraday,
    "ema_vwap_hybrid": ema_vwap_hybrid_intraday,
}


SWING_ARCHETYPES = {
    "momentum": momentum,
    "trend_following": trend_following,
    "pullback": pullback,
    "mean_reversion": mean_reversion,
    "volatility_expansion": volatility_expansion,
    "hybrid_momentum": hybrid_momentum,
}
