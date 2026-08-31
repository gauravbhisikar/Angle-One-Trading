"""NIFTY option-chain positioning read — OI, PCR, max pain, Greeks, and a
plain-language "what does this tell me" summary. No AI/LLM. No buy/sell
signals: this exists to help a human pick a strike manually after they've
already formed a directional view elsewhere (e.g. the Trend tab) — never to
predict NIFTY by itself.

No networking here — app.py fetches the scrip master + quotes + Greeks and
hands this module plain dicts/lists.
"""


def atm_strike(strikes, spot):
    """Nearest listed strike to spot."""
    if not strikes or spot is None:
        return None
    return min(strikes, key=lambda s: abs(s - spot))


def strikes_around_atm(all_strikes, atm, n=5):
    strikes = sorted(all_strikes)
    if atm not in strikes:
        return strikes
    i = strikes.index(atm)
    lo = max(0, i - n)
    hi = min(len(strikes), i + n + 1)
    return strikes[lo:hi]


def compute_pcr(rows):
    total_call_oi = sum(r["ce"]["oi"] for r in rows if r.get("ce"))
    total_put_oi = sum(r["pe"]["oi"] for r in rows if r.get("pe"))
    if total_call_oi <= 0:
        return None
    return round(total_put_oi / total_call_oi, 2)


def compute_max_pain(rows):
    """Classical max-pain: the strike where total option-writer payout
    (summed across every other strike's CE+PE open interest) is smallest."""
    if not rows:
        return None
    strikes = [r["strike"] for r in rows]
    best, best_pain = strikes[0], None
    for candidate in strikes:
        pain = 0.0
        for r in rows:
            s = r["strike"]
            ce_oi = r["ce"]["oi"] if r.get("ce") else 0
            pe_oi = r["pe"]["oi"] if r.get("pe") else 0
            if candidate > s:
                pain += (candidate - s) * ce_oi
            elif candidate < s:
                pain += (s - candidate) * pe_oi
        if best_pain is None or pain < best_pain:
            best_pain, best = pain, candidate
    return best


def oi_interpretation(prev_price, price, prev_oi, oi):
    """Price + OI-change combination -> the standard four-quadrant read.
    Explicitly labeled as an interpretation, not a fact, by the caller.
    Returns None (no read yet) rather than defaulting into a quadrant when
    nothing has actually changed since the baseline — most visibly on the
    very first reading of the day, when baseline == current exactly and
    neither price nor OI has moved."""
    if None in (prev_price, price, prev_oi, oi):
        return None
    if price == prev_price and oi == prev_oi:
        return None
    price_up = price > prev_price
    oi_up = oi > prev_oi
    if price_up and oi_up:
        return "Long buildup"
    if not price_up and oi_up:
        return "Short buildup"
    if price_up and not oi_up:
        return "Short covering"
    if not price_up and not oi_up:
        return "Long unwinding"
    return None


def strike_summary(rows):
    """Highest OI / largest OI add / largest OI unwind per side."""
    def side_summary(side):
        with_oi = [(r["strike"], r[side]) for r in rows if r.get(side)]
        if not with_oi:
            return {"highest_oi": None, "largest_add": None, "largest_unwind": None}
        highest = max(with_oi, key=lambda x: x[1]["oi"])
        add_candidates = [x for x in with_oi if x[1].get("oi_chg") is not None]
        largest_add = max(add_candidates, key=lambda x: x[1]["oi_chg"], default=None)
        largest_unwind = min(add_candidates, key=lambda x: x[1]["oi_chg"], default=None)
        return {
            "highest_oi": highest[0],
            "largest_add": largest_add[0] if largest_add and largest_add[1]["oi_chg"] > 0 else None,
            "largest_unwind": largest_unwind[0] if largest_unwind and largest_unwind[1]["oi_chg"] < 0 else None,
        }
    return {"call": side_summary("ce"), "put": side_summary("pe")}


def levels_from_oi(rows, top_n=3):
    """Resistance = strikes with concentrated call OI (above spot side
    conceptually, but ranked purely by OI here); support = concentrated
    put OI. Kept explicitly separate from the Trend tab's price-action
    support/resistance — this is about where positioning is concentrated,
    not where price has historically reacted."""
    call_ranked = sorted((r for r in rows if r.get("ce")), key=lambda r: r["ce"]["oi"], reverse=True)[:top_n]
    put_ranked = sorted((r for r in rows if r.get("pe")), key=lambda r: r["pe"]["oi"], reverse=True)[:top_n]
    return {
        "resistance": [{"strike": r["strike"], "oi": r["ce"]["oi"]} for r in call_ranked],
        "support": [{"strike": r["strike"], "oi": r["pe"]["oi"]} for r in put_ranked],
    }


def positioning_read(pcr):
    if pcr is None:
        return "Unknown"
    if pcr >= 1.2:
        return "Bullish-leaning"
    if pcr <= 0.8:
        return "Bearish-leaning"
    return "Mixed / neutral"


def build_summary(rows, pcr, max_pain, levels, strikes_info):
    return {
        "pcr": pcr,
        "pcr_read": positioning_read(pcr),
        "max_pain": max_pain,
        "resistance_zone": [z["strike"] for z in levels["resistance"]],
        "support_zone": [z["strike"] for z in levels["support"]],
        "largest_call_add": strikes_info["call"]["largest_add"],
        "largest_put_add": strikes_info["put"]["largest_add"],
        "note": "Option-chain data alone is not a trade signal.",
    }


def strike_shortlist(atm, all_strikes):
    """3 candidates per direction (ITM/ATM/OTM), one strike-step either
    side of ATM — a shortlist to investigate, never a pick. The caller is
    always responsible for choosing manually."""
    strikes = sorted(all_strikes)
    if atm not in strikes:
        return {"bullish": [], "bearish": []}
    i = strikes.index(atm)
    below = strikes[i - 1] if i > 0 else atm
    above = strikes[i + 1] if i < len(strikes) - 1 else atm
    return {
        "bullish": [
            {"strike": below, "side": "CE", "moneyness": "ITM"},
            {"strike": atm, "side": "CE", "moneyness": "ATM"},
            {"strike": above, "side": "CE", "moneyness": "OTM"},
        ],
        "bearish": [
            {"strike": above, "side": "PE", "moneyness": "ITM"},
            {"strike": atm, "side": "PE", "moneyness": "ATM"},
            {"strike": below, "side": "PE", "moneyness": "OTM"},
        ],
    }


def contract_quality(side):
    """Deterministic, explainable quality read from liquidity + Greeks —
    never a recommendation to trade this specific contract, just a
    tradeability check (thin/wide-spread contracts are hard to exit)."""
    if not side or side.get("ltp") is None:
        return {"rating": "unknown", "reasons": []}
    reasons = []
    rating = "good"

    def downgrade(to):
        nonlocal rating
        order = {"good": 0, "warn": 1, "bad": 2}
        if order[to] > order[rating]:
            rating = to

    vol = side.get("volume") or 0
    if vol < 1000:
        reasons.append("very low volume"); downgrade("bad")
    elif vol < 50000:
        reasons.append("low volume"); downgrade("warn")

    bid, ask, ltp = side.get("bid"), side.get("ask"), side.get("ltp")
    if bid is not None and ask is not None and ltp:
        spread_pct = (ask - bid) / ltp * 100
        if spread_pct > 3:
            reasons.append("wide bid/ask spread"); downgrade("bad")
        elif spread_pct > 1:
            reasons.append("moderate spread"); downgrade("warn")

    delta = side.get("delta")
    if delta is not None and abs(delta) < 0.15:
        reasons.append("very low delta"); downgrade("warn")

    iv = side.get("iv")
    if iv is not None and iv > 30:
        reasons.append("elevated IV"); downgrade("warn")

    return {"rating": rating, "reasons": reasons}


def chain_vs_trend(trend, pcr_read):
    """Plain-language comparison of option-chain positioning against the
    Trend tab's current read — never a trade signal, just tells you
    whether the two agree."""
    if not trend or trend == "sideways" or pcr_read in (None, "Unknown", "Mixed / neutral"):
        return "Chain positioning is mixed or trend is sideways — no clear confirmation either way."
    chain_dir = "bullish" if pcr_read == "Bullish-leaning" else "bearish"
    if trend == chain_dir:
        return f"Chain positioning aligns with the {trend} trend."
    return f"Chain does not confirm the {trend} trend yet."
