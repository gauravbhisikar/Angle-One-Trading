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
    "fake_breakout_window": 3,
    "sr_major_min_touches": 3,  # zones at/above this touch count are "major"; below it (but still >= sr_min_touches) are "minor"
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


class BreakoutWatch:
    """Tracks whether a just-broken level actually HOLDS for
    fake_breakout_window candles, or fails immediately (price closes back
    on the wrong side) — the "break != confirmation" check. Separate from
    RetestWatch, which tests a later, slower return-and-hold behavior;
    this catches an immediate snap-back within a few candles of the break."""
    def __init__(self, zone, direction, broken_at_i, level):
        self.zone = zone
        self.direction = direction  # "bullish" | "bearish"
        self.broken_at_i = broken_at_i
        self.level = level
        self.status = "watching"  # watching -> held | fake


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
        self.breakout_watches = []  # BreakoutWatch objects, resolved within fake_breakout_window candles
        self.fake_breakouts = []  # confirmed fake breakouts/breakdowns (direction field distinguishes)
        self.bos_events = []  # Break of Structure — swing continues the already-established trend
        self.choch_events = []  # Change of Character — confirmed trend-flip trigger (close through the swing that was holding the old trend)
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


_CONTINUATION_LABELS = {"bullish": ("HH", "HL"), "bearish": ("LH", "LL")}
# The label that means "this swing itself broke through the swing that was
# capping/supporting the OLD trend" — e.g. a confirmed HH while trend was
# bearish literally means price closed past the most recent LH (classified
# HH only because it's now higher than that LH). That IS the CHoCH break,
# not just an early warning — unlike an HL forming while bearish (still
# below the LH ceiling), which is only a heads-up (see _update_reversal).
_BREAK_LABELS = {"bullish": "LL", "bearish": "HH"}


def _log_bos(state, trend_before, label, swing):
    """BOS (Break of Structure) = a new swing that continues the trend
    already in place BEFORE this swing formed."""
    if trend_before in _CONTINUATION_LABELS and label in _CONTINUATION_LABELS[trend_before]:
        direction = "bullish" if trend_before == "bullish" else "bearish"
        state.bos_events.append({"i": swing["i"], "ts": swing["ts"], "direction": direction, "label": label})


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
            level = zone["hi"]
            _flip_zone_kind(zone, "support", i)
            ev = {"i": i, "ts": candle["ts"], "price": candle["close"], "zone_mid": zone["mid"], "direction": "bullish"}
            state.breakouts.append(ev)
            events.append(("breakout", ev))
            state.retest_watches.append(RetestWatch(zone, "bullish", i))
            state.breakout_watches.append(BreakoutWatch(zone, "bullish", i, level))
        elif zone["kind"] == "support" and candle["close"] < zone["lo"]:
            level = zone["lo"]
            _flip_zone_kind(zone, "resistance", i)
            ev = {"i": i, "ts": candle["ts"], "price": candle["close"], "zone_mid": zone["mid"], "direction": "bearish"}
            state.breakdowns.append(ev)
            events.append(("breakdown", ev))
            state.retest_watches.append(RetestWatch(zone, "bearish", i))
            state.breakout_watches.append(BreakoutWatch(zone, "bearish", i, level))
    return events


def _advance_breakout_watches(state, i, candle, config):
    """"Break != confirmation" check: a level broken this candle has
    fake_breakout_window candles to actually hold. If price closes back
    on the wrong side before then, it's a fake breakout/breakdown — the
    zone flip is reverted and any RetestWatch spawned by that same break
    is dropped, since there was nothing real to retest."""
    window = config["fake_breakout_window"]
    resolved = []
    for w in state.breakout_watches:
        if w.status != "watching":
            continue
        broke_back = (candle["close"] < w.level) if w.direction == "bullish" else (candle["close"] > w.level)
        if broke_back:
            w.status = "fake"
            ev = {"i": i, "ts": candle["ts"], "direction": w.direction, "level": w.level, "broken_at_i": w.broken_at_i}
            state.fake_breakouts.append(ev)
            resolved.append(("fake_breakout", ev))
            _flip_zone_kind(w.zone, "resistance" if w.direction == "bullish" else "support", i)
            state.retest_watches = [rw for rw in state.retest_watches
                                     if not (rw.zone is w.zone and rw.broken_at_i == w.broken_at_i)]
        elif i - w.broken_at_i >= window:
            w.status = "held"
    state.breakout_watches = [w for w in state.breakout_watches if w.status == "watching"]
    return resolved


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
                # CHoCH fires here (price closed through the LH that was
                # capping the downtrend) — `trend` itself is left for
                # _update_trend to flip organically once HL+HH confirms,
                # matching Lesson 10/12: TREND stays the old value right
                # after a CHoCH, only flips once structure truly confirms.
                state.possible_reversal["stage"] = "confirmed"
                state.choch_events.append({"i": i, "ts": candle["ts"], "direction": "bullish",
                                            "trigger_level": r["trigger_level"], "price": candle["close"]})
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
                state.choch_events.append({"i": i, "ts": candle["ts"], "direction": "bearish",
                                            "trigger_level": r["trigger_level"], "price": candle["close"]})


def _invalidation_level(state):
    if state.trend == "bullish":
        s = _last_swing_of_type(state, "low")
        return s["price"] if s else None
    if state.trend == "bearish":
        s = _last_swing_of_type(state, "high")
        return s["price"] if s else None
    return None


def _structure_signal(state):
    """Deterministic pullback-vs-reversal / BOS-vs-CHoCH summary for a UI
    card. No AI — purely derived from possible_reversal + trend. Statuses:
      trend_intact   - no counter-trend swing forming, trend continuing on BOS
      pullback_watch - a counter-trend swing has formed (potential CHoCH),
                        but price hasn't broken through the trigger level yet
                        -> still just a pullback/bounce until it does
      choch_confirmed - price closed through the trigger level: a CHoCH has
                        fired and structure has shifted, though the new trend
                        needs a further HH/HL (or LH/LL) pair to confirm more
                        strongly
    """
    r = state.possible_reversal
    if r is None:
        if state.trend == "sideways":
            tail = state.structure_sequence[-4:]
            last2 = state.structure_sequence[-2:]
            developing = None
            if set(last2) == {"HH", "HL"}:
                developing = "bullish"
            elif set(last2) == {"LH", "LL"}:
                developing = "bearish"
            if developing:
                # Saying "no established structure" while a real HH+HL (or
                # LH+LL) pair sits right there in the sequence is too
                # absolute — name it as a developing-but-not-yet-confirmed
                # sequence instead (needs 4 in a row, not 2, to flip trend).
                return {"status": "developing", "label": f"Range — developing {developing} sequence",
                        "direction": None, "developing_direction": developing,
                        "detail": f"Recent swings ({' → '.join(tail)}) show a developing {developing} pair, "
                                  f"but it takes 4 confirmed swings in a row to call this a real trend — "
                                  f"still range/sideways until then."}
            if tail:
                # Never say "no trend" with no explanation while a real swing
                # sequence is visible right below it on the UI — that reads as
                # contradictory. Name the actual swings and why they don't
                # qualify (mixed/counter-trend labels in the sequence).
                return {"status": "trend_intact", "label": "No confirmed trend — range/sideways",
                        "direction": None,
                        "detail": f"Recent swings ({' → '.join(tail)}) don't form a clean HH+HL (bullish) "
                                  f"or LH+LL (bearish) run yet — price is contained inside a range, not "
                                  f"trending either way."}
            return {"status": "trend_intact", "label": "No trend yet", "direction": None,
                    "detail": "Not enough confirmed swings yet to read any structure."}
        return {"status": "trend_intact", "label": f"{state.trend.capitalize()} trend intact — BOS",
                "direction": state.trend,
                "detail": "Latest swings continue the existing trend. No counter-trend swing forming."}
    if r["stage"] == "confirmed":
        opposite = "LH" if r["direction"] == "bullish" else "HL"
        need = "HL + HH" if r["direction"] == "bullish" else "LH + LL"
        return {"status": "choch_confirmed", "label": f"{r['direction'].capitalize()} CHoCH confirmed",
                "direction": r["direction"],
                "detail": f"Price closed through the most recent {opposite} at {r['trigger_level']} — "
                          f"structure has shifted. Still waiting for {need} to confirm the new trend more strongly."}
    return {"status": "pullback_watch", "label": f"Possible {r['direction']} CHoCH forming",
            "direction": r["direction"],
            "detail": f"A counter-trend swing has formed. Still just a pullback/bounce unless price closes "
                      f"through {r['trigger_level']}."}


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
              "breakdown": None, "retest": None, "fake_breakout": None}

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
                trend_before = state.trend
                state.swings.append(swing)
                result["new_swing"] = swing
                label = _classify_structure(state, swing, prior)
                result["structure_event"] = label
                zone = _cluster_into_zones(state, swing, config["sr_cluster_pct"])
                _update_trend(state)
                # A confirmed CHoCH resolves once `trend` organically catches
                # up to the direction it pointed to (HL+HH, or LH+LL, fully
                # qualifies via _update_trend above) — at that point it's just
                # a normal established trend again (BOS-labeled from here),
                # not still-pending CHoCH info worth showing.
                if (state.possible_reversal and state.possible_reversal.get("stage") == "confirmed"
                        and state.trend == state.possible_reversal["direction"]):
                    state.possible_reversal = None
                if label:
                    if trend_before in _BREAK_LABELS and label == _BREAK_LABELS[trend_before]:
                        # This swing's own classification IS the break — no
                        # need to wait for a later candle to close through a
                        # trigger level, the break already happened right here.
                        direction = "bearish" if trend_before == "bullish" else "bullish"
                        state.choch_events.append({"i": swing["i"], "ts": swing["ts"], "direction": direction,
                                                    "trigger_level": prior["price"] if prior else None,
                                                    "price": swing["price"]})
                        state.possible_reversal = {"direction": direction, "stage": "confirmed",
                                                    "trigger_level": prior["price"] if prior else None}
                        # Trend itself stays whatever _update_trend just computed from the
                        # tail (usually "sideways" right here) — CHoCH is an early-but-real
                        # signal, not proof of a new trend yet; `trend` only actually flips
                        # once a further HH+HL (or LH+LL) pair organically qualifies, same
                        # as Lesson 10/12's own worked example keeps TREND as the old value
                        # right after a CHoCH fires.
                    else:
                        _log_bos(state, trend_before, label, swing)
                        _update_reversal(state, i, candle, label)

    bo_events = _check_breakout(state, i, candle, config)
    for kind, ev in bo_events:
        result[kind] = ev

    rt_events = _advance_retest_watches(state, i, candle, config)
    for kind, ev in rt_events:
        result[kind] = ev

    fb_events = _advance_breakout_watches(state, i, candle, config)
    for kind, ev in fb_events:
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
        touches = len(z["touches"])
        tier = "major" if touches >= state.config["sr_major_min_touches"] else "minor"
        last_touch = max((t["i"] for t in z["touches"]), default=None)
        return {"lo": z["lo"], "hi": z["hi"], "mid": z["mid"], "kind": z["kind"],
                "touches": touches, "tier": tier,
                "strength": round(_zone_strength(z, now_i, state.config), 2),
                "last_touch_i": last_touch,
                "distance_from_price": (round(z["mid"] - last_price, 2) if last_price is not None else None)}

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
        "fake_breakouts": list(state.fake_breakouts[-10:]),
        "bos_events": list(state.bos_events[-10:]),
        "choch_events": list(state.choch_events[-10:]),
        "structure_signal": _structure_signal(state),
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
    """Angel One's historical-candle date field is an offset-aware ISO
    string in IST, e.g. "2026-09-02T15:15:00+05:30" — NOT UTC. Dropping the
    "+05:30" and treating the bare wall-clock time as UTC (the previous
    implementation) silently added a spurious +5:30 shift on every candle,
    turning a real 15:15 IST close into an epoch that displays as 20:45 —
    confirmed live (dashboard showed "Last closed candle: 08:45 pm" while
    NSE was closed and current time was ~18:06 IST). fromisoformat parses
    the offset correctly so .timestamp() gives the true UTC epoch regardless
    of what offset the source string carried."""
    import datetime
    try:
        dt = datetime.datetime.fromisoformat(date_str)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=datetime.timezone.utc)
    except ValueError:
        dt = datetime.datetime.strptime(date_str[:10], "%Y-%m-%d").replace(tzinfo=datetime.timezone.utc)
    return dt.timestamp()


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


def multi_timeframe_read(trend_1h, trend_15m, trend_5m):
    """Deterministic (no AI) read across all three timeframes, each
    answering a different question — never treats one timeframe as
    "always right":
      1H  = overall market direction (the environment)
      15M = tradable trend/setup (the PRIMARY timeframe for direction)
      5M  = entry timing only — never overrides a 15M trend just because
            it flickers the other way; that's read as a pullback/bounce
            to wait out, not a reversal.
    Returns {"action", "verdict", "detail"} — "verdict"/"detail" are
    plain sentences, "action" is a stable machine-readable tag for the
    UI to color/style consistently. Never a buy/sell instruction — always
    frames CE/PE mentions as "setup", "wait", or "wait for X before Y".
    """
    # 5M is pure entry timing — if it has no direction yet, there is
    # nothing to time an entry off regardless of the bigger picture.
    if trend_5m == "sideways":
        return {"action": "no_entry", "verdict": "No clear entry",
                "detail": "5M (entry timing) is sideways — nothing to time an entry off right now, "
                          "regardless of the 15M/1H read."}

    # 15M is the primary trade-direction timeframe. If IT has no
    # direction, there's no tradable setup — a directional 5M or 1H
    # alone doesn't create one.
    if trend_15m == "sideways":
        return {"action": "wait_no_setup", "verdict": "Wait — no 15M setup",
                "detail": "15M (trading direction/setup) is sideways — no tradable setup yet, "
                          "even though 5M/1H may show a direction."}

    side = "CE" if trend_15m == "bullish" else "PE"

    if trend_5m != trend_15m:
        # 15M sets the direction; 5M disagreeing is read as a counter-move
        # to wait out, never as "the trend reversed."
        return {"action": "wait_counter_move",
                "verdict": f"Wait — 5M counter-move against 15M {trend_15m}",
                "detail": f"15M is {trend_15m} (the setup), but 5M is currently {trend_5m} — read this "
                          f"as a short-term pullback/bounce inside the 15M trend, not a reversal. "
                          f"Wait for 5M to resume {trend_15m} before considering {side}."}

    # 15M and 5M agree on direction — now factor in 1H as context.
    if trend_1h == trend_15m:
        return {"action": "ce_setup" if side == "CE" else "pe_setup",
                "verdict": f"Best {side} setup — all three timeframes aligned",
                "detail": f"1H, 15M, and 5M are all {trend_15m} — the strongest alignment this read "
                          f"can show. Still your call to size/time the actual entry."}
    if trend_1h == "sideways":
        return {"action": "partial_ce" if side == "CE" else "partial_pe",
                "verdict": f"{side} setup forming — no 1H confirmation yet",
                "detail": f"15M and 5M both {trend_15m}, but 1H (overall market direction) is still "
                          f"sideways — the bigger picture hasn't confirmed this yet."}
    # 1H openly disagrees with 15M+5M (the only remaining case here).
    # "Pullback" describes a dip WITHIN a bullish 1H trend; "bounce" a rally
    # WITHIN a bearish 1H trend — keyed off trend_1h, not off which side
    # 15M/5M happen to point (a 15M-bearish move during a 1H uptrend is a
    # pullback regardless of the fact that it's currently pointing toward PE).
    move_name = "pullback" if trend_1h == "bullish" else "bounce"
    tag = "wait_pullback" if move_name == "pullback" else "wait_bounce"
    return {"action": tag,
            "verdict": f"Caution — 1H {trend_1h} conflicts with 15M/5M {trend_15m}",
            "detail": f"15M and 5M both point {trend_15m}, but 1H (the bigger environment) is "
                      f"{trend_1h} — this reads as a likely {move_name} inside a {trend_1h} 1H trend, "
                      f"not a confirmed {trend_15m} move. "
                      f"Don't jump straight to {side} without waiting for 1H to align too."}


def trade_setup_state(mtf, trend_15m, breakouts_15m, breakdowns_15m, retests_15m,
                       fake_breakouts_15m, choch_events_15m, now_i, data_status="live", window=6):
    """Deterministic WAIT / WATCHING / SETUP FORMING / STRUCTURE CONFIRMED /
    INVALIDATED / DATA UNRELIABLE — where you are in the manual decision
    funnel (1H context -> 15M direction -> 5M timing -> confirmation),
    never a buy/sell instruction. Built entirely from multi_timeframe_read()'s
    output plus recent 15M breakout/retest/fake-breakout/CHoCH events — no
    new inputs, no AI."""
    if data_status == "stale":
        # A confident-looking trade state built on stale candle data is
        # worse than no state at all — this must outrank every other check.
        return {"status": "data_unreliable", "label": "DATA UNRELIABLE — WAIT",
                "why": "Candle data hasn't updated recently — trade-state evaluation is paused until "
                       "fresh data arrives, regardless of what the last-known structure looked like.",
                "watch_for": "Fresh live candle data before trusting any direction read."}
    if not mtf:
        return {"status": "wait", "label": "WAIT", "why": "No read available yet.", "watch_for": ""}
    action = mtf["action"]

    if action in ("no_entry", "wait_no_setup"):
        return {"status": "wait", "label": "WAIT", "why": mtf["detail"],
                "watch_for": "15M to establish a clear HH+HL or LH+LL direction, "
                             "with 5M able to time an entry off it."}

    if action == "wait_counter_move":
        return {"status": "watching", "label": "WATCHING", "why": mtf["detail"],
                "watch_for": f"5M to resume {trend_15m} before this becomes a real setup."}

    if action in ("wait_pullback", "wait_bounce"):
        return {"status": "watching", "label": "WATCHING", "why": mtf["detail"],
                "watch_for": "1H to align with 15M/5M, or a fresh 1H structural shift."}

    # Remaining actions (ce_setup/pe_setup/partial_ce/partial_pe) all mean
    # 15M and 5M already agree on direction — check whether a real (not
    # fake) breakout/retest recently backed that up before calling it
    # "confirmed" rather than just "forming".
    direction = "bullish" if trend_15m == "bullish" else "bearish"

    def recent(events):
        for ev in reversed(events or []):
            if ev.get("i", -10**9) >= now_i - window and ev.get("direction") == direction:
                return ev
        return None

    fake = recent(fake_breakouts_15m)
    breakout_ev = recent(breakouts_15m if direction == "bullish" else breakdowns_15m)
    retest_ev = None
    for ev in reversed(retests_15m or []):
        if ev.get("i", -10**9) >= now_i - window and ev.get("direction") == direction and ev.get("result") == "confirmed":
            retest_ev = ev
            break

    if fake and not breakout_ev and not retest_ev:
        return {"status": "setup_forming", "label": "SETUP FORMING",
                "why": f"15M and 5M both {direction}, but the nearest {direction} breakout was a fake "
                       f"one (level {fake['level']} broke, then failed to hold) — no real confirmation yet.",
                "watch_for": "A fresh break + close + hold/retest before treating this as confirmed."}

    confirming_ev = retest_ev or breakout_ev
    if confirming_ev:
        # A structure was confirmed, but has anything broken it SINCE that
        # confirmation? A CHoCH in the opposite direction, fired after the
        # candle that confirmed this setup, means the old confirmed read is
        # no longer trustworthy — must be surfaced loudly, not silently left
        # showing a now-stale "CONFIRMED" badge.
        opposite = "bearish" if direction == "bullish" else "bullish"
        invalidating_choch = next(
            (c for c in reversed(choch_events_15m or [])
             if c.get("direction") == opposite and c.get("i", -10**9) > confirming_ev["i"]), None)
        if invalidating_choch:
            return {"status": "invalidated", "label": "SETUP INVALIDATED",
                    "why": f"Structure was confirmed {direction}, but a {opposite} CHoCH fired afterward "
                           f"at {invalidating_choch['price']} (broke through {invalidating_choch['trigger_level']}) "
                           f"— don't act on the previous {direction} read anymore.",
                    "watch_for": f"A fresh {direction} confirmation, or the market establishing the new "
                                 f"{opposite} direction instead."}
        confirm_kind = "retest" if retest_ev else ("breakout" if direction == "bullish" else "breakdown")
        confirm_price = confirming_ev.get("zone_mid", confirming_ev.get("price"))
        return {"status": "structure_confirmed", "label": "STRUCTURE CONFIRMED",
                "why": f"15M and 5M both {direction}, backed by a real {direction} {confirm_kind} "
                       f"at {confirm_price}.",
                "watch_for": "Manually reviewing the option chain is now reasonable — still your decision."}

    return {"status": "setup_forming", "label": "SETUP FORMING", "why": mtf["detail"],
            "watch_for": f"A real (close-confirmed, held) {direction} breakout/breakdown or retest "
                         f"near the nearest zone before treating this as confirmed."}


def primary_trend_label(trend, structure_signal):
    """Tiered classification the raw trend/sideways label can't express on
    its own — distinguishes a genuinely directionless range from one where
    a real (but not yet 4-in-a-row confirmed) HH+HL or LH+LL pair is
    emerging. Never invents a trend early just to produce a trade — the
    "developing" tier is explicitly still a form of RANGE, not a trend."""
    if trend in ("bullish", "bearish"):
        return {"tier": trend, "label": trend.upper()}
    if structure_signal and structure_signal.get("status") == "developing":
        d = structure_signal.get("developing_direction")
        return {"tier": f"developing_{d}", "label": f"DEVELOPING {d.upper()}"}
    return {"tier": "no_trend", "label": "NO TREND"}


def directional_bias(trend, primary_trend):
    """The directional lean implied by primary_trend_label — NEUTRAL
    whenever there isn't one yet, never guessed from noise."""
    if trend in ("bullish", "bearish"):
        return trend
    tier = primary_trend["tier"]
    if tier.startswith("developing_"):
        return tier.split("_", 1)[1]
    return "neutral"


def market_state(trend, current_price, support, resistance):
    """Single top-line "where are we" read for the primary (15M) timeframe —
    names the current price's position relative to the nearest confirmed
    support/resistance, or the trend if one is established. Never a
    prediction, just an orientation statement."""
    if trend in ("bullish", "bearish"):
        return {"state": trend, "label": trend.upper(),
                "detail": f"15M structure is {trend} — the baseline read is to favor this direction, "
                          f"not fight it."}
    if current_price is not None and support is not None and resistance is not None:
        return {"state": "range", "label": "RANGE / WAIT",
                "detail": f"Price ({current_price}) is between support ({support}) and resistance "
                          f"({resistance}) — no confirmed direction yet."}
    return {"state": "range", "label": "RANGE / WAIT", "detail": "No confirmed directional structure yet."}


def watch_conditions(trend, support, resistance, invalidation):
    """Explicit bullish/bearish conditions that would change the current
    read — never a signal, just names the exact levels/events to watch
    before the setup state can progress past WATCHING."""
    if trend == "sideways":
        bullish_cond = f"15M closes above resistance ({resistance})" if resistance is not None \
            else "15M closes above the nearest resistance zone"
        bearish_cond = f"15M closes below support ({support})" if support is not None \
            else "15M closes below the nearest support zone"
        return {
            "bullish": {"condition": bullish_cond,
                        "steps": ["Structure confirmation (BOS / retest holds)", "5M confirmation",
                                  "Then manually review CE contracts"]},
            "bearish": {"condition": bearish_cond,
                        "steps": ["Structure confirmation (BOS / retest holds)", "5M confirmation",
                                  "Then manually review PE contracts"]},
        }
    if trend == "bullish":
        inv_cond = f"15M closes below the invalidation level ({invalidation})" if invalidation is not None \
            else "15M closes below the most recent HL"
        return {
            "continuation": {"condition": "15M keeps printing HH + HL (BOS)",
                              "steps": ["5M confirmation", "Then manually review CE contracts"]},
            "invalidation": {"condition": inv_cond,
                              "steps": ["Would break the bullish structure (bearish CHoCH watch)",
                                        "Wait for LH + LL before treating this as bearish"]},
        }
    if trend == "bearish":
        inv_cond = f"15M closes above the invalidation level ({invalidation})" if invalidation is not None \
            else "15M closes above the most recent LH"
        return {
            "continuation": {"condition": "15M keeps printing LH + LL (BOS)",
                              "steps": ["5M confirmation", "Then manually review PE contracts"]},
            "invalidation": {"condition": inv_cond,
                              "steps": ["Would break the bearish structure (bullish CHoCH watch)",
                                        "Wait for HL + HH before treating this as bullish"]},
        }
    return None


def risk_levels(direction, current_price, support, resistance, invalidation):
    """Deterministic PRICE-LEVEL risk reference — entry/stop/target/R:R
    built from levels already computed elsewhere. Never position sizing
    (that needs the user's own capital/risk tolerance, which this system
    doesn't have and shouldn't guess) — just the price levels a manual risk
    plan would be built from. Only meaningful once direction is bullish or
    bearish; NEUTRAL/no-direction returns None."""
    if direction not in ("bullish", "bearish") or current_price is None:
        return None
    if direction == "bullish":
        stop = invalidation if invalidation is not None else support
        target = resistance
    else:
        stop = invalidation if invalidation is not None else resistance
        target = support
    if stop is None or target is None:
        return {"entry_ref": current_price, "stop_level": stop, "target_level": target,
                "risk_points": None, "reward_points": None, "risk_reward": None}
    risk_points = abs(current_price - stop)
    reward_points = abs(target - current_price)
    rr = round(reward_points / risk_points, 2) if risk_points else None
    return {"entry_ref": current_price, "stop_level": stop, "target_level": target,
            "risk_points": round(risk_points, 2), "reward_points": round(reward_points, 2),
            "risk_reward": rr}
