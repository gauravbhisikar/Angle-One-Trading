# Plain-data catalog for combinatorial intraday generation — no LLM, no
# HTTP. Each entry is a DSL rule fragment (or None for "no rule") tagged
# with a `style` used by quick_filter.py's compatibility check: ANDing a
# "trend"-tagged filter with a "mean_reversion"-tagged trigger under the
# DSL's AND semantics structurally starves trade count in a short window
# (the same failure class as the VWAP-crosses bug documented in
# templates.py) — quick_filter rejects that combination before it ever
# reaches a real backtest.
#
# Every fragment here reuses indicators/operators already confirmed
# working in templates.py's live-tested archetypes (or, for
# stochastic_rsi, confirmed via registry_meta.go + its Go source this
# session — same crosses_above/value pattern as the already-working rsi
# trigger). Adding a genuinely new indicator (opening range, RVOL, etc.)
# is future work, not in this catalog yet.

TREND_FILTERS = {
    "none": {"rule": None, "style": "neutral"},
    "ema_cross_20_50": {
        "rule": {"indicator": "ema_cross", "fast": 20, "slow": 50, "operator": "bullish"},
        "style": "trend",
    },
    "supertrend_10_3": {
        "rule": {"indicator": "supertrend", "period": 10, "multiplier": 3, "operator": "bullish"},
        "style": "trend",
    },
    "adx_above_20": {
        "rule": {"indicator": "adx", "period": 14, "operator": ">", "value": 20},
        "style": "trend",
    },
    "vwap_uptrend": {
        "rule": {"indicator": "close", "operator": ">", "compare_to": {"indicator": "vwap"}},
        "style": "trend",
    },
}

ENTRY_TRIGGERS = {
    "rsi_pullback_32": {
        "rule": {"indicator": "rsi", "operator": "crosses_above", "value": 32},
        "style": "mean_reversion",
    },
    "bollinger_lower_reentry": {
        "rule": {"indicator": "bollinger_bands", "period": 20, "std_dev": 2, "operator": "price_below_lower"},
        "style": "mean_reversion",
    },
    "donchian_breakout_up": {
        "rule": {"indicator": "donchian_channel", "period": 20, "operator": "breakout_up"},
        "style": "breakout",
    },
    "macd_bullish_cross": {
        "rule": {"indicator": "macd", "operator": "bullish"},
        "style": "trend_follow",
    },
    "vwap_reversion_cross": {
        # NEVER bare "vwap crosses_above" — vwap.go's Update() never sets
        # crossing flags, this only works routed through compare_to
        # (confirmed live this session: 0 trades over 60 real days without it).
        "rule": {"indicator": "close", "operator": "crosses_above", "compare_to": {"indicator": "vwap"}},
        "style": "mean_reversion",
    },
    "volume_spike_breakout": {
        "rule": {"indicator": "volume", "operator": "spike_pct", "value": 150},
        "style": "breakout",
    },
    "stochastic_rsi_oversold": {
        "rule": {"indicator": "stochastic_rsi", "period": 14, "operator": "crosses_above", "value": 20},
        "style": "mean_reversion",
    },
}

CONFIRMATIONS = {
    "none": {"rule": None, "style": "neutral"},
    "adx_above_20": {
        "rule": {"indicator": "adx", "period": 14, "operator": ">", "value": 20},
        "style": "trend",
    },
    "volume_spike_150": {
        "rule": {"indicator": "volume", "operator": "spike_pct", "value": 150},
        "style": "breakout",
    },
    "macd_positive": {
        "rule": {"indicator": "macd", "operator": "bullish"},
        "style": "trend_follow",
    },
}

# Appended to the exit "any" list alongside the risk tier's take_profit/
# stop_loss (still always present — see templates._tpsl_intraday). Empty
# list means the exit is TP/SL-only.
EXIT_STYLES = {
    "tp_sl_only": [],
    "supertrend_flip": [{"indicator": "supertrend", "operator": "bearish"}],
    "macd_bearish": [{"indicator": "macd", "operator": "bearish"}],
    "vwap_cross_down": [{"indicator": "close", "operator": "<", "compare_to": {"indicator": "vwap"}}],
}

TREND_FILTER_NAMES = list(TREND_FILTERS)
ENTRY_TRIGGER_NAMES = list(ENTRY_TRIGGERS)
CONFIRMATION_NAMES = list(CONFIRMATIONS)
EXIT_STYLE_NAMES = list(EXIT_STYLES)
