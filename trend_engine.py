"""Rule-based NIFTY 50 trend/structure analysis — swings, HH/HL/LH/LL,
support/resistance zones, breakout/breakdown, retest, trend + reversal.

No AI/LLM anywhere in this module. No networking, no threading, no JSON/HTTP
awareness beyond plain dict I/O — app.py owns fetching candles and calling
this on a schedule. Output is trend + structure + levels + events only, never
a buy/sell signal or a price prediction.
"""
import math

DEFAULT_CONFIG = {
    "swing_lookback": 3,
    "min_swing_move_pct": 0.15,
    "sr_cluster_pct": 0.25,
    "sr_min_touches": 2,
    "sr_max_zones_shown": 4,
    "breakout_confirm": "close",
    "retest_tolerance_pct": 0.2,
    "retest_confirm_candles": 2,
}


def _fixed_pct_min_move(candles, swing_i, prior_swing_i, config):
    if prior_swing_i is None:
        return True
    a, b = candles[swing_i]["close"], candles[prior_swing_i]["close"]
    if b == 0:
        return False
    return abs(a - b) / abs(b) * 100 >= config["min_swing_move_pct"]


DEFAULT_CONFIG["min_move_fn"] = _fixed_pct_min_move


class RetestWatch:
    def __init__(self, zone, direction, broken_at_i):
        self.zone = zone
        self.direction = direction  # "bullish" | "bearish"
        self.broken_at_i = broken_at_i
        self.status = "watching"  # watching -> touched -> confirmed | failed | expired
        self.touched_at_i = None


class TrendState:
    """Mutable accumulator for one timeframe. Feed closed candles one at a
    time via process_candle(), in strictly ascending ts order."""

    def __init__(self, config=None):
        self.config = dict(DEFAULT_CONFIG if config is None else config)
        self.candles = []  # all closed candles seen so far (for lookback windows)
        self.swings = []  # confirmed swings, ascending by i
        self.structure_sequence = []  # list of "HH"/"HL"/"LH"/"LL"
        self.structure_detail = []  # parallel list: {"label", "i", "ts"} per event, for locating trend-start on a chart
        self.zones = []  # all zones ever formed
        self.trend = "sideways"
        self.possible_reversal = None
        self.retest_watches = []
        self.breakouts = []
        self.breakdowns = []
        self.retests = []
        self.last_processed_ts = None
        self._last_confirmed_high_i = None
        self._last_confirmed_low_i = None


def _is_local_extreme(candles, i, lookback):
    if i - lookback < 0 or i + lookback >= len(candles):
        return None
    window_hi = candles[i]["high"]
    window_lo = candles[i]["low"]
    is_high = all(candles[j]["high"] <= window_hi for j in range(i - lookback, i + lookback + 1) if j != i)
    is_low = all(candles[j]["low"] >= window_lo for j in range(i - lookback, i + lookback + 1) if j != i)
    if is_high and not is_low:
        return "high"
    if is_low and not is_high:
        return "low"
    return None


def _last_swing_of_type(state, kind):
    for s in reversed(state.swings):
        if s["type"] == kind:
            return s
    return None


def _classify_structure(state, new_swing, prior):
    """prior must be the last CONFIRMED swing of this type from BEFORE
    new_swing was added to state.swings — looking it up here instead would
    find new_swing itself (already appended by the caller) and compare it
    to itself, which is never `>`, silently forcing every swing to LH/LL
    and never HH/HL. Caller already computes this correctly for the
    min-move check; reuse that instead of a second, buggy lookup."""
    if prior is None:
        return None
    if new_swing["type"] == "high":
        label = "HH" if new_swing["price"] > prior["price"] else "LH"
    else:
        label = "HL" if new_swing["price"] > prior["price"] else "LL"
    state.structure_sequence.append(label)
    state.structure_detail.append({"label": label, "i": new_swing["i"], "ts": new_swing["ts"]})
    return label


def _cluster_into_zones(state, new_swing, cluster_pct):
    kind = "resistance" if new_swing["type"] == "high" else "support"
    price = new_swing["price"]
    for zone in state.zones:
        mid = zone["mid"]
        if mid == 0:
            continue
        if abs(price - mid) / abs(mid) * 100 <= cluster_pct:
            zone["lo"] = min(zone["lo"], price)
            zone["hi"] = max(zone["hi"], price)
            zone["mid"] = (zone["lo"] + zone["hi"]) / 2
            zone["touches"].append(new_swing)
            return zone
    zone = {"lo": price, "hi": price, "mid": price, "touches": [new_swing],
            "kind": kind, "flip_log": []}
    state.zones.append(zone)
    return zone


def _zone_strength(zone, now_i, config):
    touches = zone["touches"]
    touch_score = len(touches)
    recency_score = sum(1.0 / (1 + max(0, now_i - t["i"]) / 20.0) for t in touches)
    significance_score = sum(abs(t.get("move_pct", 0)) for t in touches) / max(1, len(touches))
    flip_bonus = 2.0 if zone["flip_log"] else 0.0
    return touch_score * 1.0 + recency_score * 2.0 + significance_score * 0.5 + flip_bonus


def _flip_zone_kind(zone, new_kind, at_i):
    if zone["kind"] != new_kind:
        zone["flip_log"].append({"from": zone["kind"], "to": new_kind, "i": at_i})
        zone["kind"] = new_kind


def _nearest_zone(zones, price, kind):
    candidates = [z for z in zones if z["kind"] == kind]
    if not candidates:
        return None
    if kind == "resistance":
        above = [z for z in candidates if z["mid"] >= price]
        pool = above or candidates
        return min(pool, key=lambda z: abs(z["mid"] - price))
    below = [z for z in candidates if z["mid"] <= price]
    pool = below or candidates
    return min(pool, key=lambda z: abs(z["mid"] - price))


def _check_breakout(state, i, candle, config):
    events = []
    for zone in state.zones:
        if zone["kind"] == "resistance" and candle["close"] > zone["hi"]:
            _flip_zone_kind(zone, "support", i)
            ev = {"i": i, "ts": candle["ts"], "price": candle["close"], "zone_mid": zone["mid"], "direction": "bullish"}
            state.breakouts.append(ev)
            events.append(("breakout", ev))
            state.retest_watches.append(RetestWatch(zone, "bullish", i))
        elif zone["kind"] == "support" and candle["close"] < zone["lo"]:
            _flip_zone_kind(zone, "resistance", i)
            ev = {"i": i, "ts": candle["ts"], "price": candle["close"], "zone_mid": zone["mid"], "direction": "bearish"}
            state.breakdowns.append(ev)
            events.append(("breakdown", ev))
            state.retest_watches.append(RetestWatch(zone, "bearish", i))
    return events


def _advance_retest_watches(state, i, candle, config):
    tol = config["retest_tolerance_pct"]
    confirm_n = config["retest_confirm_candles"]
    resolved = []
    for w in list(state.retest_watches):
        zone_mid = w.zone["mid"]
        near = zone_mid != 0 and abs(candle["close"] - zone_mid) / abs(zone_mid) * 100 <= tol
        if w.status == "watching":
            if near:
                w.status = "touched"
                w.touched_at_i = i
        elif w.status == "touched":
            if i - w.touched_at_i >= confirm_n:
                held = (candle["close"] > zone_mid) if w.direction == "bullish" else (candle["close"] < zone_mid)
                if held:
                    w.status = "confirmed"
                    ev = {"i": i, "ts": candle["ts"], "direction": w.direction, "zone_mid": zone_mid, "result": "confirmed"}
                    state.retests.append(ev)
                    resolved.append(("retest", ev))
                else:
                    w.status = "failed"
                    ev = {"i": i, "ts": candle["ts"], "direction": w.direction, "zone_mid": zone_mid, "result": "failed"}
                    state.retests.append(ev)
                    resolved.append(("retest", ev))
    state.retest_watches = [w for w in state.retest_watches if w.status in ("watching", "touched")]
    return resolved


def _update_trend(state):
    tail = state.structure_sequence[-4:]
    bullish = len(tail) >= 2 and all(t in ("HH", "HL") for t in tail) and "HH" in tail and "HL" in tail
    bearish = len(tail) >= 2 and all(t in ("LH", "LL") for t in tail) and "LH" in tail and "LL" in tail
    if bullish:
        state.trend = "bullish"
    elif bearish:
        state.trend = "bearish"
    else:
        state.trend = "sideways"


def _update_reversal(state, i, candle, new_label):
    r = state.possible_reversal
    if state.trend == "bearish":
        if new_label == "HL":
            state.possible_reversal = {"direction": "bullish", "stage": "hl_formed_awaiting_break",
                                        "trigger_level": None}
            lh = _last_swing_of_type(state, "high")
            if lh:
                state.possible_reversal["trigger_level"] = lh["price"]
        elif r and r["stage"] == "hl_formed_awaiting_break" and r["trigger_level"] is not None:
            if candle["close"] > r["trigger_level"]:
                state.possible_reversal["stage"] = "confirmed"
                state.trend = "bullish"
    elif state.trend == "bullish":
        if new_label == "LH":
            state.possible_reversal = {"direction": "bearish", "stage": "lh_formed_awaiting_break",
                                        "trigger_level": None}
            hl = _last_swing_of_type(state, "low")
            if hl:
                state.possible_reversal["trigger_level"] = hl["price"]
        elif r and r["stage"] == "lh_formed_awaiting_break" and r["trigger_level"] is not None:
            if candle["close"] < r["trigger_level"]:
                state.possible_reversal["stage"] = "confirmed"
                state.trend = "bearish"
    else:
        if state.possible_reversal and state.possible_reversal["stage"] == "confirmed":
            state.possible_reversal = None


def _invalidation_level(state):
    if state.trend == "bullish":
        s = _last_swing_of_type(state, "low")
        return s["price"] if s else None
    if state.trend == "bearish":
        s = _last_swing_of_type(state, "high")
        return s["price"] if s else None
    return None


def _trend_start(state):
    """Where the CURRENT trend run began: walks backward through
    structure_detail while events keep matching the current trend's
    allowed pair (HH/HL for bullish, LH/LL for bearish), stopping at the
    first event that breaks the run. Returns the candle index/ts of the
    earliest swing in that unbroken run, or None if trend is sideways or
    there's no structure yet — this is what draws the "trend start"
    marker on the chart."""
    if state.trend not in ("bullish", "bearish") or not state.structure_detail:
        return None
    allowed = ("HH", "HL") if state.trend == "bullish" else ("LH", "LL")
    start = None
    for ev in reversed(state.structure_detail):
        if ev["label"] not in allowed:
            break
        start = ev
    return {"i": start["i"], "ts": start["ts"]} if start else None


def process_candle(state, candle):
    """Feed ONE closed candle. Never looks at candles after `candle` in the
    caller's sequence — this is the no-lookahead guarantee. Returns a small
    dict describing what happened on this candle."""
    state.candles.append(candle)
    i = len(state.candles) - 1
    config = state.config
    result = {"new_swing": None, "structure_event": None, "breakout": None,
              "breakdown": None, "retest": None}

    lookback = config["swing_lookback"]
    check_i = i - lookback
    if check_i >= 0:
        kind = _is_local_extreme(state.candles, check_i, lookback)
        if kind is not None:
            prior = _last_swing_of_type(state, kind)
            prior_i = prior["i"] if prior else None
            move_pct = None
            if prior is not None and prior["price"]:
                move_pct = (state.candles[check_i]["close"] - prior["price"]) / abs(prior["price"]) * 100
            if config["min_move_fn"](state.candles, check_i, prior_i, config):
                swing = {
                    "i": check_i, "ts": state.candles[check_i]["ts"],
                    "price": state.candles[check_i]["high"] if kind == "high" else state.candles[check_i]["low"],
                    "type": kind, "move_pct": move_pct or 0.0,
                    "confirmed_at_i": i, "confirmed_at": candle["ts"],
                    "confirmation_delay": i - check_i,
                }
                state.swings.append(swing)
                result["new_swing"] = swing
                label = _classify_structure(state, swing, prior)
                result["structure_event"] = label
                zone = _cluster_into_zones(state, swing, config["sr_cluster_pct"])
                _update_trend(state)
                if label:
                    _update_reversal(state, i, candle, label)

    bo_events = _check_breakout(state, i, candle, config)
    for kind, ev in bo_events:
        result[kind] = ev

    rt_events = _advance_retest_watches(state, i, candle, config)
    for kind, ev in rt_events:
        result[kind] = ev

    state.last_processed_ts = candle["ts"]
    return result


def snapshot(state):
    """Pure read of accumulated state -> JSON-serializable dict. Never mutates."""
    now_i = len(state.candles) - 1
    qualifying = [z for z in state.zones if len(z["touches"]) >= state.config["sr_min_touches"]]
    # Rank PER KIND, not flat across both — a flat top-N can let one side
    # (e.g. long-established, high-touch-count resistance zones) crowd out
    # every support zone, leaving nearest_support blank even when real,
    # qualifying support zones exist (confirmed bug: with sr_max_zones_shown=4
    # and resistance zones at 18/11/11/10 touches, no newer/lower-touch
    # support zone ever made a flat top-4 cut). Half the budget per side
    # guarantees both get a chance to show when they exist.
    per_side = max(1, state.config["sr_max_zones_shown"] // 2)
    ranked_zones = []
    for kind in ("resistance", "support"):
        side = sorted((z for z in qualifying if z["kind"] == kind),
                       key=lambda z: _zone_strength(z, now_i, state.config), reverse=True)
        ranked_zones.extend(side[:per_side])
    last_price = state.candles[-1]["close"] if state.candles else None
    nearest_resistance = _nearest_zone(ranked_zones, last_price, "resistance") if last_price is not None else None
    nearest_support = _nearest_zone(ranked_zones, last_price, "support") if last_price is not None else None

    def zone_out(z):
        return {"lo": z["lo"], "hi": z["hi"], "mid": z["mid"], "kind": z["kind"],
                "touches": len(z["touches"]), "strength": round(_zone_strength(z, now_i, state.config), 2)}

    return {
        "trend": state.trend,
        "trend_start": _trend_start(state),
        "reversal": state.possible_reversal,
        "structure_sequence": list(state.structure_sequence[-12:]),
        "swings": [dict(s) for s in state.swings[-30:]],
        "zones": [zone_out(z) for z in ranked_zones],
        "breakouts": list(state.breakouts[-10:]),
        "breakdowns": list(state.breakdowns[-10:]),
        "retests": list(state.retests[-10:]),
        "nearest_support": nearest_support["mid"] if nearest_support else None,
        "nearest_resistance": nearest_resistance["mid"] if nearest_resistance else None,
        "invalidation_level": _invalidation_level(state),
        "candles": list(state.candles),
        "config": {k: v for k, v in state.config.items() if k != "min_move_fn"},
    }


def normalize_candle(raw):
    return {
        "ts": float(raw["ts"]) if "ts" in raw else _parse_ts(raw["date"]),
        "date": raw["date"],
        "open": float(raw["open"]), "high": float(raw["high"]),
        "low": float(raw["low"]), "close": float(raw["close"]),
        "volume": float(raw.get("volume", 0.0)),
        "tf": raw.get("tf", ""),
    }


def _parse_ts(date_str):
    import datetime
    s = date_str[:19]
    try:
        dt = datetime.datetime.strptime(s, "%Y-%m-%dT%H:%M:%S")
    except ValueError:
        dt = datetime.datetime.strptime(s[:10], "%Y-%m-%d")
    return dt.replace(tzinfo=datetime.timezone.utc).timestamp()


def normalize_candles(raw_rows, timeframe):
    out = {}
    for r in raw_rows:
        c = normalize_candle(dict(r, tf=timeframe))
        out[c["ts"]] = c  # de-dupe identical timestamps, keep last
    return sorted(out.values(), key=lambda c: c["ts"])


def advance_live_trend(state, new_candles):
    """Feed only candles newer than state.last_processed_ts through
    process_candle, in ascending order. Never reprocesses already-seen
    candles — this is what keeps live updates cheap regardless of how much
    history the state already holds."""
    for candle in new_candles:
        if state.last_processed_ts is None or candle["ts"] > state.last_processed_ts:
            process_candle(state, candle)
