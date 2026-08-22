#!/usr/bin/env python3
"""
Pre-Market Trading Dashboard — one screen, everything in cards.

Zero-dependency Python (stdlib only). Fetches live data from:
  Finnhub (SPY/QQQ/DIA)
  Twelve Data (USD/INR, Brent)
  Alpha Vantage (USD/INR fallback)
  NSE (India VIX, NIFTY 50)
  Yahoo Finance (Nikkei, Hang Seng, Shanghai, Brent, NIFTY, India VIX fallbacks)
  TradingView (GIFT NIFTY — SSR page scrape)
  Angel One (NIFTY daily OHLC + trend; falls back to Yahoo if the WAF blocks)

Run:  python app.py   (then open http://localhost:9080/)
"""

import base64
import hashlib
import hmac
import json
import os
import re
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from datetime import date, datetime, timedelta, timezone
from http import HTTPStatus
from http.cookiejar import CookieJar
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

REFRESH_SECONDS = 10  # full rebuild (Finnhub/Twelve Data/Alpha
                       # Vantage/NSE/Yahoo/GIFT) — bounded by Twelve Data's
                       # free-tier 8 req/min ceiling (~7.5s floor), not by
                       # Angel One (which the separate fast tick loop
                       # handles on its own faster cadence, see
                       # _angel_tick_loop / TICK_INTERVAL_SECONDS above).
PORT = int(os.environ.get("DASHBOARD_PORT", "9080"))
TZ_IST = timedelta(hours=5, minutes=30)


def _git_commit():
    """Short commit hash this process is actually running, so the UI can
    show a build stamp — the fastest way to tell "did my deploy actually
    take" apart from "browser cached the old page"."""
    try:
        return subprocess.check_output(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=os.path.dirname(os.path.abspath(__file__)),
            stderr=subprocess.DEVNULL, timeout=5,
        ).decode().strip()
    except Exception:
        return "unknown"


BUILD_COMMIT = _git_commit()
BUILD_STARTED = None  # set in main(), IST timestamp string

# --------------------------------------------------------------------------
# .env loading (no dotenv dependency)
# --------------------------------------------------------------------------

def load_dotenv(path=".env"):
    if not os.path.exists(path):
        return
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, _, val = line.partition("=")
            key = key.strip()
            if key and not os.environ.get(key):
                os.environ[key] = val.strip()

load_dotenv()

# --------------------------------------------------------------------------
# HTTP helpers
# --------------------------------------------------------------------------

def _opener(jar=None):
    if jar is None:
        return urllib.request.build_opener()
    return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))


def http_json(url, headers=None, timeout=20, jar=None):
    hdrs = {"User-Agent": UA, "Accept": "application/json"}
    if headers:
        hdrs.update(headers)
    req = urllib.request.Request(url, headers=hdrs)
    with _opener(jar).open(req, timeout=timeout) as resp:
        body = resp.read().decode("utf-8", "replace")
    try:
        return json.loads(body)
    except json.JSONDecodeError:
        raise RuntimeError(f"non-JSON response: {body[:150]!r}") from None


def http_text(url, headers=None, timeout=20, jar=None):
    hdrs = {"User-Agent": UA}
    if headers:
        hdrs.update(headers)
    req = urllib.request.Request(url, headers=hdrs)
    with _opener(jar).open(req, timeout=timeout) as resp:
        return resp.read().decode("utf-8", "replace")


def http_post_json(url, body, headers=None, timeout=20, jar=None):
    hdrs = {"User-Agent": UA, "Accept": "application/json", "Content-Type": "application/json"}
    if headers:
        hdrs.update(headers)
    req = urllib.request.Request(url, data=json.dumps(body).encode(), headers=hdrs, method="POST")
    with _opener(jar).open(req, timeout=timeout) as resp:
        raw = resp.read().decode("utf-8", "replace")
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        raise RuntimeError(f"non-JSON response: {raw[:150]!r}") from None


def fnum(value, digits=2, sep=True):
    try:
        v = float(value)
    except (TypeError, ValueError):
        return "—"
    s = f"{v:,.{digits}f}" if sep else f"{v:.{digits}f}"
    return s


# --------------------------------------------------------------------------
# Data providers — each returns a dict or raises on failure.
# --------------------------------------------------------------------------

def finnhub_quote(symbol):
    key = os.environ["FINNHUB_API_KEY"]
    j = http_json(f"https://finnhub.io/api/v1/quote?symbol={urllib.parse.quote(symbol)}&token={key}")
    return {"price": j.get("c"), "chg": j.get("dp"), "chg_abs": j.get("d"),
            "open": j.get("o"), "prev_close": j.get("pc")}


# --- News (free headlines, keyword rules or optional cheap LLM pass) ------

def fetch_market_news(limit=25):
    key = os.environ["FINNHUB_API_KEY"]
    j = http_json(f"https://finnhub.io/api/v1/news?category=general&token={key}")
    out = []
    for item in j[:limit]:
        headline = item.get("headline") or ""
        if not headline:
            continue
        out.append({"headline": headline, "source": item.get("source", ""),
                     "url": item.get("url", ""), "ts": item.get("datetime")})
    return out


NEWS_POSITIVE_TERMS = (
    "rate cut", "stimulus", "surge", "rally", "record high", "upgrade",
    "buyback", "beats estimates", "strong earnings", "expansion", "deal",
    "agreement", "ceasefire", "resolved", "growth", "profit jumps",
)
NEWS_NEGATIVE_TERMS = (
    "war", "crash", "plunge", "recession", "rate hike", "default",
    "bankruptcy", "sanctions", "ban", "tariff", "shutdown", "layoffs",
    "downgrade", "crisis", "conflict", "attack", "sell-off", "selloff",
    "slump", "misses estimates", "inflation surge",
)
NEWS_HIGH_IMPACT_TERMS = (
    "rbi", "federal reserve", " fed ", "war", "crash", "recession",
    "default", "sanctions", "ceasefire", "rate hike", "rate cut",
    "geopolitical", "election", "tariff", "opec",
)
NEWS_MEDIUM_IMPACT_TERMS = (
    "earnings", "gdp", "cpi", "jobs report", "ipo", "merger",
    "acquisition", "quarterly results", "guidance", "inflation",
)


def keyword_score_headline(headline):
    """Deterministic fallback when no LLM key is configured (or the LLM
    call fails) — same if/else philosophy as every other check on this
    dashboard, just applied per-headline instead of per-number."""
    text = f" {headline.lower()} "
    pos = sum(1 for t in NEWS_POSITIVE_TERMS if t in text)
    neg = sum(1 for t in NEWS_NEGATIVE_TERMS if t in text)
    sentiment = "negative" if neg > pos else ("positive" if pos > neg else "neutral")
    if any(t in text for t in NEWS_HIGH_IMPACT_TERMS):
        impact = "high"
    elif any(t in text for t in NEWS_MEDIUM_IMPACT_TERMS):
        impact = "medium"
    else:
        impact = "low"
    return sentiment, impact


NEWS_LLM_MODEL = "deepseek/deepseek-v4-flash"  # a classification pass over
                                                 # ~25 headlines is cheap,
                                                 # low-stakes work — no need
                                                 # for a pricier model here


def openrouter_classify_news(items):
    """One batched call classifying every headline at once (not one call
    per headline) — keeps this to a single cheap request per news refresh.
    Raises on any failure; caller falls back to keyword_score_headline."""
    api_key = os.environ.get("OPENROUTER_API_KEY", "")
    if not api_key:
        raise RuntimeError("no OPENROUTER_API_KEY configured")
    numbered = "\n".join(f"{i + 1}. {it['headline']}" for i, it in enumerate(items))
    prompt = (
        "You are a financial news classifier for India's NIFTY 50 index. "
        "The headlines below come from a general US/global business feed — "
        "most of them have nothing to do with India or NIFTY 50. Be strict: "
        "impact must reflect relevance and magnitude specifically for "
        "NIFTY 50/Indian equities, not just 'is this market-related news in "
        "general'. Lifestyle pieces, US-only politics/culture, and company "
        "profiles with no India angle are impact:low regardless of tone. "
        "Only global macro (Fed, oil/Hormuz, major geopolitical/war "
        "escalation, US market moves with global risk-off potential) or "
        "anything India-specific (RBI, Indian companies, India-US trade) "
        "can be impact:medium or impact:high.\n\n"
        "For each numbered headline, judge whether it's likely to "
        "materially affect NIFTY 50 today.\n\n"
        "Respond with ONLY a JSON array (no prose, no markdown fences), "
        "one object per headline, in the same order:\n"
        '[{"sentiment":"positive|negative|neutral","impact":"high|medium|low","reason":"<8 words>"}]\n\n'
        f"Headlines:\n{numbered}"
    )
    resp = http_post_json(
        "https://openrouter.ai/api/v1/chat/completions",
        {"model": NEWS_LLM_MODEL, "messages": [{"role": "user", "content": prompt}], "temperature": 0},
        headers={"Authorization": f"Bearer {api_key}"}, timeout=30)
    content = resp["choices"][0]["message"]["content"].strip()
    if content.startswith("```"):
        content = content.split("```")[1]
        if content.startswith("json"):
            content = content[4:]
    parsed = json.loads(content)
    if not isinstance(parsed, list) or len(parsed) != len(items):
        raise RuntimeError(f"classification shape mismatch (got {len(parsed) if isinstance(parsed, list) else type(parsed)})")
    return parsed


def score_news(items):
    """Mutates each item in place with sentiment/impact/reason/method,
    preferring one batched LLM classification pass and falling back to
    plain keyword rules if no key is set or the call fails for any
    reason (bad JSON, rate limit, network) — the feature must never go
    dark just because the LLM path had a bad day."""
    try:
        classified = openrouter_classify_news(items)
        for it, cl in zip(items, classified):
            it["sentiment"] = cl.get("sentiment", "neutral")
            it["impact"] = cl.get("impact", "low")
            it["reason"] = cl.get("reason", "")
            it["method"] = "AI"
    except Exception:
        for it in items:
            sentiment, impact = keyword_score_headline(it["headline"])
            it["sentiment"] = sentiment
            it["impact"] = impact
            it["reason"] = "keyword match"
            it["method"] = "rules"
    return items


def aggregate_news_sentiment(items):
    """Only counts medium/high-impact items — a "low impact / unrelated to
    NIFTY" headline (most of Finnhub's general feed, in practice) shouldn't
    move the overall verdict just because it happened to read negative in
    tone. Trusts the classifier's own impact rating to filter noise."""
    relevant = [i for i in items if i["impact"] != "low"]
    pos = sum(1 for i in relevant if i["sentiment"] == "positive")
    neg = sum(1 for i in relevant if i["sentiment"] == "negative")
    high_impact = [i for i in relevant if i["impact"] == "high"]
    if not relevant:
        return {"overall": "No relevant news", "class": "flat", "positive": 0,
                "negative": 0, "neutral": 0, "high_impact_count": 0,
                "relevant_count": 0, "total_count": len(items)}
    if neg > pos and high_impact:
        overall, cls = "Negative", "down"
    elif pos > neg and high_impact:
        overall, cls = "Positive", "up"
    elif neg > pos:
        overall, cls = "Slightly negative", "down"
    elif pos > neg:
        overall, cls = "Slightly positive", "up"
    else:
        overall, cls = "Mixed / neutral", "flat"
    return {"overall": overall, "class": cls, "positive": pos, "negative": neg,
            "neutral": len(relevant) - pos - neg, "high_impact_count": len(high_impact),
            "relevant_count": len(relevant), "total_count": len(items)}


NEWS_IMPACT_RANK = {"high": 0, "medium": 1, "low": 2}
NEWS_SHOWN_CAP = 20


def build_news():
    # Fetch a bigger pool than we'll show — Finnhub's general feed is
    # mostly US lifestyle/business noise with no India angle, so most of
    # it gets classified impact:low and dropped below.
    items = fetch_market_news(limit=40)
    score_news(items)
    relevant = sorted((it for it in items if it["impact"] != "low"),
                       key=lambda it: NEWS_IMPACT_RANK.get(it["impact"], 2))
    shown = relevant[:NEWS_SHOWN_CAP]
    method = items[0]["method"] if items else "rules"
    return {
        "generated_at": ist_now().strftime("%Y-%m-%d %H:%M:%S"),
        "items": shown,
        "sentiment": aggregate_news_sentiment(shown),
        "method": method,
        "total_fetched": len(items),
    }


def openrouter_expected_trend(checks, news_sentiment):
    """One extra LLM call synthesizing a short pre-market hypothesis across
    every rule-based check plus news sentiment. Explicitly separate from
    those checks — this is the AI's read on top of them, not a replacement;
    the rule-based PRE-MARKET BIAS stays the source of truth. Raises on any
    failure/no key; caller simply omits the AI card rather than faking one
    with keyword rules (unlike score_news, there's no honest deterministic
    substitute for "synthesize all of this into one narrative")."""
    api_key = os.environ.get("OPENROUTER_API_KEY", "")
    if not api_key:
        raise RuntimeError("no OPENROUTER_API_KEY configured")
    lines = [f"- {c['title'].split('· ', 1)[-1]}: {c['verdict']} ({c.get('value', '')})"
             for c in checks if c.get("id") != "chk_nifty"]
    lines.append(
        f"- News Sentiment: {news_sentiment.get('overall')} "
        f"({news_sentiment.get('positive', 0)} positive / {news_sentiment.get('negative', 0)} negative "
        f"/ {news_sentiment.get('high_impact_count', 0)} high-impact, out of "
        f"{news_sentiment.get('relevant_count', 0)} relevant stories)")
    prompt = (
        "You are a pre-market analyst for India's NIFTY 50 index. Based ONLY "
        "on the rule-based checks below — not your own outside market "
        "knowledge — write a short pre-market hypothesis for today.\n\n"
        + "\n".join(lines) + "\n\n"
        "Respond with ONLY JSON, no prose outside it:\n"
        '{"expected_trend":"bullish|bearish|mixed|cautious","confidence":"low|medium|high",'
        '"summary":"<2-3 plain sentences. Must explicitly note this is a '
        'pre-market hypothesis that price action after 9:15 can confirm or '
        'reject, not a prediction of the whole day.>"}'
    )
    resp = http_post_json(
        "https://openrouter.ai/api/v1/chat/completions",
        {"model": NEWS_LLM_MODEL, "messages": [{"role": "user", "content": prompt}], "temperature": 0.2},
        headers={"Authorization": f"Bearer {api_key}"}, timeout=30)
    content = resp["choices"][0]["message"]["content"].strip()
    if content.startswith("```"):
        content = content.split("```")[1]
        if content.startswith("json"):
            content = content[4:]
    trend = json.loads(content)
    trend["method"] = "AI"
    return trend


def build_news_with_trend():
    """build_news() plus the AI trend synthesis, which needs the latest
    market checks alongside the news sentiment it just computed — kept as
    a separate wrapper so build_news() itself stays usable standalone
    (e.g. for local testing) without requiring CACHE to be populated."""
    snap = build_news()
    with CACHE_LOCK:
        market_checks = (CACHE.get("market") or {}).get("checks", [])
    try:
        snap["trend"] = openrouter_expected_trend(market_checks, snap["sentiment"])
    except Exception:
        snap["trend"] = None
    return snap


def twelvedata(symbol):
    key = os.environ["TWELVEDATA_API_KEY"]
    j = http_json(f"https://api.twelvedata.com/time_series?symbol={urllib.parse.quote(symbol)}"
                  f"&interval=1day&outputsize=3&apikey={key}")
    if j.get("status") == "error":
        raise RuntimeError(j.get("message", "twelvedata error"))
    vals = j["values"]
    return {"price": float(vals[0]["close"]), "prev": float(vals[1]["close"])}


def alphavantage_fx():
    key = os.environ["ALPHAVANTAGE_API_KEY"]
    j = http_json("https://www.alphavantage.co/query?function=FX_DAILY&from_symbol=USD"
                  f"&to_symbol=INR&apikey={key}")
    series = j.get("Time Series FX (Daily)")
    if not series:
        raise RuntimeError("alphavantage: no series")
    dates = sorted(series.keys())[-2:]
    cur = float(series[dates[1]]["4. close"])
    prev = float(series[dates[0]]["4. close"])
    return {"price": cur, "prev": prev}


def yahoo_chart(symbol, rng="1mo"):
    url = (f"https://query1.finance.yahoo.com/v8/finance/chart/"
           f"{urllib.parse.quote(symbol, safe='')}?interval=1d&range={rng}")
    j = http_json(url, headers={"User-Agent": UA})
    res = j["chart"]["result"][0]
    ts = res.get("timestamp", [])
    q = res["indicators"]["quote"][0]
    rows = []
    for t, o, h, l, c in zip(ts, q.get("open", []), q.get("high", []),
                             q.get("low", []), q.get("close", [])):
        if c is None:
            continue
        rows.append({"date": datetime.fromtimestamp(t, tz=None),
                     "open": o, "high": h, "low": l, "close": c})
    rows.sort(key=lambda r: r["date"])
    prev = res["meta"].get("chartPreviousClose")
    return {"rows": rows, "prev": prev, "price": rows[-1]["close"] if rows else None}


def nse_vix_and_nifty():
    jar = CookieJar()
    opener = _opener(jar)
    opener.open(urllib.request.Request(
        "https://www.nseindia.com/",
        headers={"User-Agent": UA, "Accept-Language": "en-US,en;q=0.9"}), timeout=20).read()
    time.sleep(1.0)
    j = json.loads(opener.open(urllib.request.Request(
        "https://www.nseindia.com/api/allIndices",
        headers={"User-Agent": UA, "Accept": "application/json, text/plain, */*",
                 "Referer": "https://www.nseindia.com/all-market-data",
                 "Accept-Language": "en-US,en;q=0.9"}), timeout=20).read().decode("utf-8", "replace"))
    vix = nifty = None
    for item in j.get("data", []):
        name = item.get("index", "")
        if "VIX" in name.upper() and vix is None:
            vix = {"price": item.get("last"), "chg": item.get("percentChange"),
                   "chg_abs": item.get("change")}
        if name == "NIFTY 50":
            nifty = {"price": item.get("last"), "chg": item.get("percentChange"),
                     "chg_abs": item.get("change")}
    return {"vix": vix, "nifty": nifty}


# --- TradingView SSR scrape ------------------------------------------------

def _tv_extract(slug):
    html = http_text(f"https://in.tradingview.com/symbols/{slug}/", timeout=20)
    data = {}
    m = re.search(r'"trade":\{"price":([0-9.]+)', html)
    if m:
        data["price"] = float(m.group(1))
    m = re.search(r'"daily_bar_change":(-?[0-9.]+)', html)
    if m:
        data["chg"] = float(m.group(1))
    m = re.search(r'"daily_bar":\{(.*?)\}', html)
    if m:
        block = m.group(1)
        for field in ("open", "high", "low", "close"):
            fm = re.search(rf'"{field}":"([0-9.]+)"', block)
            if fm:
                data[field] = float(fm.group(1))
    if "price" not in data:
        raise RuntimeError("tradingview: no price in page")
    return data


def tradingview_gift():
    return _tv_extract("NSEIX-NIFTY1%21")


def tradingview_nifty():
    return _tv_extract("NSE-NIFTY")


# --- Angel One (SmartAPI) --------------------------------------------------

def _base32_decode(s):
    s = s.strip().upper().replace(" ", "").replace("=", "")
    alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
    bits = ""
    for ch in s:
        i = alphabet.index(ch)
        bits += f"{i:05b}"
    return bytes(int(bits[i:i + 8], 2) for i in range(0, len(bits) - 7, 8))


def get_totp(secret, window=30, digits=6):
    key = _base32_decode(secret)
    counter = int(time.time() // window)
    msg = counter.to_bytes(8, "big")
    h = hmac.new(key, msg, hashlib.sha1).digest()
    offset = h[-1] & 0x0F
    code = ((h[offset] & 0x7F) << 24 | (h[offset + 1] & 0xFF) << 16 |
            (h[offset + 2] & 0xFF) << 8 | (h[offset + 3] & 0xFF))
    return str(code % (10 ** digits)).zfill(digits)


# Angel One session is short-lived (~a few requests/sec rate limit, daily
# token expiry) — re-login every call rather than caching across the
# 60s refresh cycle; simplest thing that can't go stale mid-session.
def _env_any(*names):
    for n in names:
        v = os.environ.get(n)
        if v:
            return v
    raise KeyError(names[0])


_ANGEL_GATE_LOCK = threading.Lock()
_ANGEL_LAST_CALL = [0.0]
ANGEL_MIN_INTERVAL = 1.3  # seconds required between ANY two Angel One API
                          # calls, enforced across every thread. The slow
                          # loop (every REFRESH_SECONDS) and the fast tick
                          # loop (every 2s) each used to space their OWN
                          # calls with a local time.sleep(), but neither knew
                          # about the other — their bursts could still
                          # overlap and trip the ~1 req/sec ceiling. One
                          # shared gate serializes every real HTTP call
                          # regardless of which loop/thread makes it.


def _angel_rate_gate():
    with _ANGEL_GATE_LOCK:
        wait = ANGEL_MIN_INTERVAL - (time.time() - _ANGEL_LAST_CALL[0])
        if wait > 0:
            time.sleep(wait)
        _ANGEL_LAST_CALL[0] = time.time()


ANGEL_SESSION = {"headers": None, "at": 0.0}
ANGEL_SESSION_TTL = 300  # seconds — logging in fresh every fast-tick cycle
                          # would itself trip the rate limit; a session's
                          # JWT stays valid far longer than this anyway.
ANGEL_PREV_CLOSE = {}    # symboltoken -> previous close, refreshed once/day
                          # by the slow loop; the fast tick loop only needs
                          # the live LTP, not a fresh candle fetch every cycle.


def angelone_session():
    now = time.time()
    if ANGEL_SESSION["headers"] and now - ANGEL_SESSION["at"] < ANGEL_SESSION_TTL:
        return ANGEL_SESSION["headers"]
    headers = angelone_login()
    ANGEL_SESSION["headers"] = headers
    ANGEL_SESSION["at"] = now
    return headers


def angelone_login():
    # ANGEL_* matches the Go engine's naming (the real server .env); the
    # ANGLE_ONE_* names are kept as a fallback for older local setups.
    api_key = _env_any("ANGEL_API_KEY", "ANGLE_ONE_API_KEY")
    client = _env_any("ANGEL_CLIENT_ID", "ANGLE_ONE_CLIENT_CODE")
    pin = _env_any("ANGEL_PIN", "ANGLE_ONE_PIN")
    totp_secret = _env_any("ANGEL_TOTP_SECRET", "ANGLE_ONE_TOTP_SECRET")
    base_headers = {"X-PrivateKey": api_key,
                    "X-ClientLocalIP": "127.0.0.1",
                    "X-ClientPublicIP": "127.0.0.1",
                    "X-MACAddress": "00:00:00:00:00:00",
                    "X-UserType": "USER",
                    "X-SourceID": "WEB",
                    # No browser User-Agent here on purpose — claiming to be
                    # Chrome while urllib's TLS handshake doesn't match one
                    # is a classic WAF bot-trigger. The working Go engine
                    # client (connectors/angelone/client.go) never fakes a
                    # UA either; match its plain-script identity instead.
                    "User-Agent": "python-urllib/angelone-premarket-dashboard"}
    _angel_rate_gate()
    login = http_post_json(
        "https://apiconnect.angelbroking.com/rest/auth/angelbroking/user/v1/loginByPassword",
        {"clientcode": client, "password": pin, "totp": get_totp(totp_secret)},
        headers=base_headers, timeout=15)
    token = (login.get("data") or {}).get("jwtToken")
    if not token:
        raise RuntimeError("angelone: login failed")
    return dict(base_headers, Authorization=f"Bearer {token}")


def angelone_candles(headers, exchange, symboltoken, days=30):
    to = date.today()
    fr = to - timedelta(days=days)
    _angel_rate_gate()
    hist = http_post_json(
        "https://apiconnect.angelbroking.com/rest/secure/angelbroking/historical/v1/getCandleData",
        {"exchange": exchange, "symboltoken": symboltoken, "interval": "ONE_DAY",
         "fromdate": fr.strftime("%Y-%m-%d 09:15"), "todate": to.strftime("%Y-%m-%d 15:30")},
        headers=headers, timeout=20)
    rows = hist.get("data") or []
    parsed = []
    for r in rows:
        if isinstance(r, list) and len(r) >= 5:
            parsed.append({"date": r[0][:10], "open": float(r[1]), "high": float(r[2]),
                           "low": float(r[3]), "close": float(r[4])})
    if not parsed:
        raise RuntimeError("angelone: no rows")
    return parsed


def angelone_historical():
    return angelone_candles(angelone_login(), "NSE", "99926000")


def angelone_ltp(headers, exchange, symboltoken):
    """Live last-traded-price for one instrument, via Angel One's own feed —
    preferred over scraping NSE/Yahoo wherever Angel One carries the
    instrument, since it's the same authenticated broker feed the trading
    engine itself trades against.

    Uses the combined market/v1/quote endpoint, not the older
    order/v1/getLtpData — that older endpoint gets rejected by Angel One's
    WAF (confirmed: same login/session, same server, getLtpData returns
    a WAF "Request Rejected" page while market/v1/quote succeeds)."""
    _angel_rate_gate()
    q = http_post_json(
        "https://apiconnect.angelbroking.com/rest/secure/angelbroking/market/v1/quote",
        {"mode": "LTP", "exchangeTokens": {exchange: [symboltoken]}},
        headers=headers, timeout=15)
    fetched = ((q.get("data") or {}).get("fetched")) or []
    if not fetched or fetched[0].get("ltp") is None:
        raise RuntimeError(f"angelone: no ltp in response ({q!r})")
    return float(fetched[0]["ltp"])


def _angelone_rate_limited_retry(fn):
    """Angel One's rate ceiling isn't a hard 1/sec — network jitter can still
    land two calls in the same window even with a fixed sleep between them,
    and one retry sometimes still isn't enough. Backs off harder each
    attempt (2s, 4s) before giving up and letting the caller fall back to
    NSE/Yahoo."""
    last = None
    for backoff in (0, 2, 4):
        if backoff:
            time.sleep(backoff)
        try:
            return fn()
        except urllib.error.HTTPError as e:
            if e.code != 403:
                raise
            last = e
    raise last


def angelone_live_and_prev(headers, exchange, symboltoken):
    """Live LTP + prior trading day's close (for a %-change figure), given
    an already-logged-in session. Caller must share one login (via
    angelone_login()) across every instrument fetched per refresh cycle —
    Angel One's login endpoint rate-limits rapid successive logins (two
    logins seconds apart from the same client got the second one 403'd
    in testing), so logging in once per instrument silently breaks the
    2nd+ one."""
    ltp = _angelone_rate_limited_retry(lambda: angelone_ltp(headers, exchange, symboltoken))
    rows = _angelone_rate_limited_retry(lambda: angelone_candles(headers, exchange, symboltoken, days=10))
    rows.sort(key=lambda r: r["date"])
    today = ist_now().date().isoformat()
    prev_rows = [r for r in rows if r["date"] < today]
    prev_close = (prev_rows[-1] if prev_rows else rows[-1])["close"]
    ANGEL_PREV_CLOSE[symboltoken] = prev_close  # fast tick loop reuses this
    return float(ltp), prev_close


def vix_verdict(vix_chg):
    """Shared with the fast tick loop so a mid-cycle LTP-only refresh
    produces the exact same verdict the slow full-rebuild would have."""
    if vix_chg >= 5:
        return "Volatility expected ↑", "down"
    if vix_chg <= -5:
        return "Calmer market", "up"
    return "Normal", "flat"


# --- Aggregations ----------------------------------------------------------

def ist_now():
    return datetime.now(timezone.utc) + TZ_IST


def market_open():
    now = ist_now()
    if now.weekday() >= 5:
        return False
    t = now.hour * 60 + now.minute
    return 9 * 60 + 15 <= t <= 15 * 60 + 30


def market_info():
    """Returns open/closed state plus the next 09:15 IST open time."""
    now = ist_now()
    wd = now.weekday()
    hhmm = now.hour * 60 + now.minute
    open_t = 9 * 60 + 15
    close_t = 15 * 60 + 30
    if wd >= 5:
        state = "closed"
    elif hhmm < open_t:
        state = "pre-market"
    elif hhmm <= close_t:
        state = "open"
    else:
        state = "closed"

    def next_weekday_915(d):
        while d.weekday() >= 5:
            d += timedelta(days=1)
        return d.replace(hour=9, minute=15, second=0, microsecond=0)

    if state == "open":
        nxt = now
    elif state == "pre-market":
        nxt = datetime.combine(now.date(), datetime.min.time()) + timedelta(hours=9, minutes=15)
    else:
        nxt = next_weekday_915(datetime.combine(now.date() + timedelta(days=1), datetime.min.time()))
    hints = {
        "open": "values are live",
        "pre-market": "these readings set today's opening tone",
        "closed": "last available values",
    }
    return {"state": state, "open": state == "open",
            "state_hint": hints[state],
            "now": now.strftime("%Y-%m-%d %H:%M:%S"),
            "next_open": nxt.strftime("%Y-%m-%d %H:%M")}


def nifty_ohlc_and_trend():
    """Try Angel One first (primary), fall back to Yahoo ^NSEI."""
    rows = None
    source = "Angel One"
    try:
        rows = angelone_historical()
    except Exception:
        pass
    if not rows:
        try:
            y = yahoo_chart("^NSEI", "1mo")
            rows = [{"date": r["date"].strftime("%Y-%m-%d"),
                     "open": r["open"], "high": r["high"],
                     "low": r["low"], "close": r["close"]} for r in y["rows"]]
            source = "Yahoo"
        except Exception:
            return None

    rows.sort(key=lambda r: r["date"])
    today = ist_now().date().isoformat()
    idx = len(rows) - 1
    if rows[idx]["date"] >= today and len(rows) > 1:
        idx -= 1
    yesterday = rows[idx]
    last = rows[min(idx + 1, len(rows) - 1)]

    closes = [r["close"] for r in rows[:idx + 1]]
    if len(closes) < 6:
        closes = [r["close"] for r in rows[:idx + 2]]
    if len(closes) >= 6:
        sma5 = sum(closes[-6:-1]) / 5.0
    else:
        sma5 = sum(closes) / len(closes)
    c = closes[-1]
    prev_c = closes[-2] if len(closes) > 1 else c
    if c > prev_c and c > sma5:
        trend = "Bullish"
    elif c < prev_c and c < sma5:
        trend = "Bearish"
    else:
        trend = "Sideways"
    pct = (c - prev_c) / prev_c * 100 if prev_c else 0.0

    return {"source": source, "yesterday": yesterday, "last_close": last["close"],
            "trend": trend, "trend_pct": pct}


def pick_change(cur, prev):
    if not cur or not prev:
        return None
    return (cur - prev) / prev * 100.0


# --------------------------------------------------------------------------
# Build the full snapshot
# --------------------------------------------------------------------------

def build_market():
    cards = []
    raw = {}          # id -> {"value", "chg", "source", "extra"}
    checks = []
    warnings = []     # user-visible "X failed, fell back to Y" trail — shown as a banner

    def note_fail(label, exc, fell_back_to):
        warnings.append(f"{label} failed ({exc!r}) — using {fell_back_to} instead")

    def add(card):
        cards.append(card)

    def store(cid, value, chg, source, extra=None):
        raw[cid] = {"value": value, "chg": chg, "source": source, "extra": extra or {}}

    def quote_card(cid, title, source, value, chg, detail=None):
        store(cid, value, chg, source)
        add({"id": cid, "title": title, "value": fnum(value), "change": chg,
             "change_text": (f"{chg:+.2f}%" if chg is not None else "—"),
             "detail": detail or "", "source": source,
             "updated": ist_now().strftime("%H:%M:%S")})

    # --- Global indices (Finnhub) ---
    for cid, title, sym in (("sp500", "S&P 500", "SPY"),
                            ("nasdaq", "Nasdaq", "QQQ"),
                            ("dow", "Dow Jones", "DIA")):
        try:
            q = finnhub_quote(sym)
            quote_card(cid, title, "Finnhub", q["price"], q["chg"],
                       f"{sym} · prev close {fnum(q['prev_close'])}")
        except Exception:
            add({"id": cid, "title": title, "value": "—", "change": None,
                 "change_text": "unavailable", "detail": "Finnhub failed", "source": "Finnhub",
                 "updated": ""})

    # --- Asia (Twelve Data preferred, Yahoo fallback) ---
    for cid, title, td_syms, yahoo_sym in (
            ("nikkei", "Nikkei 225", ("NIKKEI", "N225"), "^N225"),
            ("hangseng", "Hang Seng", ("HSI",), "^HSI"),
            ("shanghai", "Shanghai Composite", ("SHANGHAI",), "000001.SS")):
        src = "Yahoo"
        cur = prev = None
        for s in td_syms:
            try:
                t = twelvedata(s)
                cur, prev, src = t["price"], t["prev"], "Twelve Data"
                break
            except Exception:
                continue
        if cur is None:
            try:
                y = yahoo_chart(yahoo_sym, "5d")
                cur = y["price"]
                prev = y["prev"] or (y["rows"][-2]["close"] if len(y["rows"]) > 1 else None)
            except Exception:
                cur = prev = None
        chg = pick_change(cur, prev)
        quote_card(cid, title, src, cur, chg,
                   f"prev close {fnum(prev)}" if prev else "")

    # Angel One: reuse the cached session (angelone_session()) across both
    # this rebuild and the fast tick loop — logging in fresh every cycle
    # gets rate-limited (403) same as any other rapid successive call.
    angel_headers = None
    try:
        angel_headers = angelone_session()
    except Exception:
        pass

    # --- India VIX (Angel One primary, NSE then Yahoo fallback) ---
    try:
        if angel_headers is None:
            raise RuntimeError("angelone: no session (login failed)")
        ltp, prev = angelone_live_and_prev(angel_headers, "NSE", "99926017")
        quote_card("vix", "India VIX", "Angel One", ltp, pick_change(ltp, prev),
                   f"prev close {fnum(prev)}")
    except Exception as e1:
        try:
            ns = nse_vix_and_nifty()
            v = ns["vix"]
            quote_card("vix", "India VIX", "NSE", v["price"], v["chg"],
                       f"absolute change {fnum(v['chg_abs'], 2)}" if v.get("chg_abs") is not None else "")
            note_fail("Angel One (VIX)", e1, "NSE")
        except Exception as e2:
            try:
                y = yahoo_chart("^INDIAVIX", "5d")
                prev = y["prev"] or (y["rows"][-2]["close"] if len(y["rows"]) > 1 else None)
                quote_card("vix", "India VIX", "Yahoo", y["price"],
                           pick_change(y["price"], prev), "fallback source")
                note_fail("Angel One (VIX)", e1, "Yahoo")
                note_fail("NSE (VIX)", e2, "Yahoo")
            except Exception as e3:
                add({"id": "vix", "title": "India VIX", "value": "—", "change": None,
                     "change_text": "unavailable", "detail": "Angel One/NSE/Yahoo all failed", "source": "NSE",
                     "updated": ""})
                warnings.append(f"India VIX unavailable — Angel One {e1!r}, NSE {e2!r}, Yahoo {e3!r}")

    # --- Live NIFTY 50 (Angel One primary, NSE then Yahoo fallback) ---
    try:
        if angel_headers is None:
            raise RuntimeError("angelone: no session (login failed)")
        ltp, prev = angelone_live_and_prev(angel_headers, "NSE", "99926000")
        quote_card("nifty_live", "NIFTY 50", "Angel One", ltp, pick_change(ltp, prev),
                   f"prev close {fnum(prev)}")
    except Exception as e1:
        try:
            ns = nse_vix_and_nifty()
            nl = ns["nifty"]
            if nl and nl.get("price") is not None:
                quote_card("nifty_live", "NIFTY 50", "NSE", nl["price"], nl["chg"],
                           f"absolute change {fnum(nl['chg_abs'], 2)}" if nl.get("chg_abs") is not None else "")
                note_fail("Angel One (NIFTY)", e1, "NSE")
            else:
                raise RuntimeError("nse: no nifty price")
        except Exception as e2:
            try:
                y = yahoo_chart("^NSEI", "5d")
                prev = y["prev"] or (y["rows"][-2]["close"] if len(y["rows"]) > 1 else None)
                quote_card("nifty_live", "NIFTY 50", "Yahoo", y["price"],
                           pick_change(y["price"], prev), "fallback source")
                note_fail("Angel One (NIFTY)", e1, "Yahoo")
                note_fail("NSE (NIFTY)", e2, "Yahoo")
            except Exception as e3:
                add({"id": "nifty_live", "title": "NIFTY 50", "value": "—", "change": None,
                     "change_text": "unavailable", "detail": "Angel One/NSE/Yahoo all failed", "source": "NSE",
                     "updated": ""})
                warnings.append(f"NIFTY 50 unavailable — Angel One {e1!r}, NSE {e2!r}, Yahoo {e3!r}")

    # --- Commodities & FX ---
    try:
        t = twelvedata("USD/INR")
        quote_card("usdinr", "USD/INR", "Twelve Data", t["price"],
                   pick_change(t["price"], t["prev"]), "rate")
    except Exception:
        try:
            av = alphavantage_fx()
            quote_card("usdinr", "USD/INR", "Alpha Vantage", av["price"],
                       pick_change(av["price"], av["prev"]), "rate")
        except Exception:
            try:
                y = yahoo_chart("INR=X", "5d")
                prev = y["prev"] or (y["rows"][-2]["close"] if len(y["rows"]) > 1 else None)
                quote_card("usdinr", "USD/INR", "Yahoo", y["price"],
                           pick_change(y["price"], prev), "rate")
            except Exception:
                add({"id": "usdinr", "title": "USD/INR", "value": "—", "change": None,
                     "change_text": "unavailable", "detail": "", "source": "Twelve Data",
                     "updated": ""})

    for cid, title, td_syms, yahoo_sym in [
            ("brent", "Brent Crude", ("BRENT",), "BZ=F")]:
        src = "Yahoo"
        cur = prev = None
        for s in td_syms:
            try:
                t = twelvedata(s)
                cur, prev, src = t["price"], t["prev"], "Twelve Data"
                break
            except Exception:
                continue
        if cur is None:
            try:
                y = yahoo_chart(yahoo_sym, "5d")
                cur = y["price"]
                prev = y["prev"] or (y["rows"][-2]["close"] if len(y["rows"]) > 1 else None)
            except Exception:
                cur = prev = None
        chg = pick_change(cur, prev)
        quote_card(cid, title, src, cur, chg, f"prev close {fnum(prev)}" if prev else "")

    # --- GIFT NIFTY (TradingView scrape) ---
    try:
        gift = tradingview_gift()
        quote_card("gift", "GIFT NIFTY", "TradingView", gift["price"], gift.get("chg"),
                   f"last {gift.get('open') and fnum(gift['open'])} · high {gift.get('high') and fnum(gift['high'])} · low {gift.get('low') and fnum(gift['low'])}")
    except Exception:
        add({"id": "gift", "title": "GIFT NIFTY", "value": "—", "change": None,
             "change_text": "unavailable", "detail": "TradingView scrape failed", "source": "TradingView",
             "updated": ""})

    # --- NIFTY yesterday OHLC + trend ---
    nifty = nifty_ohlc_and_trend()
    if nifty:
        y = nifty["yesterday"]
        add({"id": "nifty_ohlc", "title": "NIFTY — Yesterday",
             "value": fnum(y["close"]),
             "change": None, "change_text": f"{y['date']}",
             "detail": (f"O {fnum(y['open'])}  H {fnum(y['high'])}  "
                        f"L {fnum(y['low'])}  C {fnum(y['close'])}"),
             "source": nifty["source"], "updated": ""})
        add({"id": "nifty_trend", "title": "NIFTY Trend", "value": nifty["trend"],
             "change": nifty["trend_pct"],
             "change_text": f"{nifty['trend_pct']:+.2f}% vs prev close",
             "detail": "close vs 5-day avg", "source": nifty["source"], "updated": ""})
        store("nifty", y["close"], None, nifty["source"],
              {"ohlc": y, "trend": nifty["trend"], "trend_pct": nifty["trend_pct"]})

    # ============================ PRE-MARKET CHECKS ============================
    def check(cid, title, verdict, vclass, value, detail, source, reason="", extra=None, rule=""):
        checks.append({"id": cid, "title": title, "verdict": verdict,
                       "verdict_class": vclass, "value": value, "detail": detail,
                       "reason": reason, "rule": rule, "source": source, "extra": extra or {}})

    # 1. GIFT NIFTY vs yesterday's NIFTY close (predicts the open)
    gift_v = raw.get("gift", {}).get("value")
    nifty_c = raw.get("nifty", {}).get("value")
    if gift_v is not None and nifty_c is not None:
        premium = gift_v - nifty_c
        if premium >= 100:
            v, vc = "Possible gap-up", "up"
        elif premium >= 20:
            v, vc = "Slight positive", "up"
        elif premium <= -100:
            v, vc = "Possible gap-down", "down"
        elif premium <= -20:
            v, vc = "Slight negative", "down"
        else:
            v, vc = "Flat open", "flat"
        check("chk_gift", "1 · GIFT NIFTY Opening Signal", v, vc,
              f"{fnum(gift_v)} vs NIFTY prev close {fnum(nifty_c)}",
              "GIFT vs yesterday's NIFTY close predicts the gap at 9:15, not the whole day",
              raw["gift"]["source"],
              f"{fnum(gift_v)} − {fnum(nifty_c)} = {premium:+,.0f} pts",
              extra={"premium": premium, "gift": gift_v, "nifty_close": nifty_c},
              rule="≥+100 gap-up · ≥+20 slight+ · ±20 flat · ≤−20 slight− · ≤−100 gap-down")
    else:
        check("chk_gift", "1 · GIFT NIFTY Opening Signal", "Unavailable", "flat",
              "—", "GIFT or NIFTY data missing", "TradingView")

    # 2. US Markets sentiment (prev night % change)
    us = {k: raw.get(k, {}).get("chg") for k in ("sp500", "nasdaq", "dow")}
    up_count = sum(1 for v in us.values() if v is not None and v > 0.05)
    down_count = sum(1 for v in us.values() if v is not None and v < -0.05)
    if up_count == 3:
        v, vc = "Positive", "up"
    elif down_count == 3:
        v, vc = "Negative", "down"
    else:
        v, vc = "Mixed / neutral", "flat"
    ticker_line = "  ".join(
        f"{t} {'▲' if chg > 0 else ('▼' if chg < 0 else '—')} {chg:+.2f}%"
        for (t, chg) in (("S&P", us["sp500"]), ("NASDAQ", us["nasdaq"]), ("DOW", us["dow"]))
        if chg is not None)
    check("chk_us", "2 · US Markets Sentiment", v, vc, ticker_line,
          "global risk mood before India opens", "Finnhub",
          f"{up_count}▲ {down_count}▼ of 3",
          rule="3▲ → Positive · 3▼ → Negative · else Mixed")

    # 3. Asian Markets sentiment
    asia = {k: raw.get(k, {}).get("chg") for k in ("nikkei", "hangseng", "shanghai")}
    a_up = sum(1 for val in asia.values() if val is not None and val > 0.05)
    a_down = sum(1 for val in asia.values() if val is not None and val < -0.05)
    if a_up > a_down:
        v, vc = "Positive", "up"
    elif a_down > a_up:
        v, vc = "Negative", "down"
    else:
        v, vc = "Mixed", "flat"
    ticker_line = "  ".join(
        f"{t} {'▲' if c > 0 else ('▼' if c < 0 else '—')} {c:+.2f}%"
        for (t, c) in (("Nikkei", asia["nikkei"]), ("HangSeng", asia["hangseng"]), ("Shanghai", asia["shanghai"]))
        if c is not None)
    check("chk_asia", "3 · Asian Markets Sentiment", v, vc, ticker_line,
          "Asia is already trading when India opens", "Yahoo / Twelve Data",
          f"{a_up}▲ {a_down}▼ of 3",
          rule="most▲ → Positive · most▼ → Negative · even → Mixed")

    # 4. India VIX vs previous close
    vix_v = raw.get("vix", {}).get("chg")
    if vix_v is not None:
        v, vc = vix_verdict(vix_v)
        check("chk_vix", "4 · India VIX", v, vc,
              f"{fnum(raw['vix']['value'])} ({vix_v:+.2f}%)",
              "VIX tells how big the market may move, not which direction",
              raw["vix"]["source"],
              f"Δ {vix_v:+.2f}%",
              extra={"change": vix_v},
              rule="≥+5% → vol ↑ · ≤−5% → calmer · else normal")
    else:
        check("chk_vix", "4 · India VIX", "Unavailable", "flat", "—", "", "NSE")

    # 5. USD/INR
    fx_v = raw.get("usdinr", {}).get("chg")
    if fx_v is not None:
        if fx_v >= 0.3:
            v, vc = "Rupee weakening", "down"
        elif fx_v <= -0.3:
            v, vc = "Rupee strengthening", "up"
        else:
            v, vc = "Small move — ignore", "flat"
        check("chk_usdinr", "5 · USD/INR", v, vc,
              f"{fnum(raw['usdinr']['value'])} ({fx_v:+.2f}%)",
              "Large currency moves can affect Indian equities and foreign flows",
              raw["usdinr"]["source"],
              f"Δ {fx_v:+.2f}%",
              extra={"change": fx_v},
              rule="≥+0.3% → weakening · ≤−0.3% → strengthening · else ignore")
    else:
        check("chk_usdinr", "5 · USD/INR", "Unavailable", "flat", "—", "", "Twelve Data")

    # 6. Brent Crude
    br_v = raw.get("brent", {}).get("chg")
    if br_v is not None:
        if br_v >= 1:
            v, vc = "Negative macro factor", "down"
        elif br_v <= -1:
            v, vc = "Supportive", "up"
        else:
            v, vc = "Small move — ignore", "flat"
        check("chk_brent", "6 · Brent Crude", v, vc,
              f"{fnum(raw['brent']['value'])} ({br_v:+.2f}%)",
              "India imports most of its crude — big oil moves matter",
              raw["brent"]["source"],
              f"Δ {br_v:+.2f}%",
              extra={"change": br_v},
              rule="≥+1% → negative · ≤−1% → supportive · else ignore")
    else:
        check("chk_brent", "6 · Brent Crude", "Unavailable", "flat", "—", "", "Yahoo")

    # 7. NIFTY yesterday levels → today's support/resistance
    if nifty:
        y = nifty["yesterday"]
        sup = y["low"]
        res = y["high"]
        current = raw.get("gift", {}).get("value")  # GIFT NIFTY — best live
                                                       # proxy for "where is
                                                       # NIFTY right now" pre-market
        check("chk_nifty", "7 · NIFTY Yesterday Levels",
              f"Support {fnum(sup)} / Resistance {fnum(res)}", "flat",
              f"Close {fnum(y['close'])}",
              "today's key price levels",
              nifty["source"],
              f"Low {fnum(sup)} · High {fnum(res)}",
              {"ohlc": {"high": y["high"], "low": y["low"], "close": y["close"], "date": y["date"]},
               "current": current},
              rule="Support = yesterday low · Resistance = yesterday high")
    else:
        check("chk_nifty", "7 · NIFTY Yesterday Levels", "Unavailable", "flat", "—", "", "Yahoo")

    return {
        "generated_at": ist_now().strftime("%Y-%m-%d %H:%M:%S"),
        "generated_epoch": time.time(),
        "market": market_info(),
        "refresh_seconds": REFRESH_SECONDS,
        "checks": checks,
        "cards": cards,
        "warnings": warnings,
        "build": BUILD_COMMIT,
        "build_started": BUILD_STARTED,
    }


# --------------------------------------------------------------------------
# Background refresh cache
# --------------------------------------------------------------------------

CACHE = {}
CACHE_LOCK = threading.Lock()


def _refresh_loop():
    while True:
        try:
            snap = build_market()
            with CACHE_LOCK:
                CACHE["market"] = snap
                CACHE["at"] = time.time()
        except Exception as exc:  # keep the server alive even if a fetch fails
            with CACHE_LOCK:
                CACHE["error"] = str(exc)
        time.sleep(REFRESH_SECONDS)


NEWS_REFRESH_SECONDS = 300  # news doesn't need per-2s freshness, and this
                            # keeps both Finnhub's free-tier quota and (if
                            # configured) LLM classification cost trivial


def _news_loop():
    while True:
        try:
            snap = build_news_with_trend()
            with CACHE_LOCK:
                CACHE["news"] = snap
        except Exception as exc:
            with CACHE_LOCK:
                CACHE["news_error"] = str(exc)
        time.sleep(NEWS_REFRESH_SECONDS)


# Angel One instruments the fast tick loop refreshes: (card id, exchange,
# symboltoken, check id or None if this instrument has no "checks" entry).
ANGEL_TICK_INSTRUMENTS = [
    ("vix", "NSE", "99926017", "chk_vix"),
    ("nifty_live", "NSE", "99926000", None),
]
TICK_INTERVAL_SECONDS = 2


def _angel_tick_loop():
    """Fast-cadence LTP-only refresh for whichever of VIX/NIFTY the slow
    loop (_refresh_loop, every REFRESH_SECONDS) most recently established
    Angel One as the live source for. Reuses one cached session
    (angelone_session()) across every cycle — logging in fresh every 2s
    would itself trip Angel One's rate limit — and only asks for the LTP,
    never re-fetching candle history (that only changes once/day, and the
    slow loop already keeps ANGEL_PREV_CLOSE current). Patches the cached
    snapshot's cards + checks in place so Live-mode polling sees genuinely
    fresh numbers between slow-loop rebuilds instead of a static value.
    """
    while True:
        time.sleep(TICK_INTERVAL_SECONDS)
        try:
            headers = angelone_session()
        except Exception:
            continue

        with CACHE_LOCK:
            snap = CACHE.get("market")
        if not snap:
            continue
        cards_by_id = {c["id"]: c for c in snap.get("cards", [])}
        checks_by_id = {c["id"]: c for c in snap.get("checks", [])}

        for cid, exch, token, check_id in ANGEL_TICK_INSTRUMENTS:
            card = cards_by_id.get(cid)
            prev_close = ANGEL_PREV_CLOSE.get(token)
            if not card or card.get("source") != "Angel One" or prev_close is None:
                continue  # slow loop isn't on Angel One for this one right
                          # now (fallback active) — don't fight its output
            try:
                ltp = _angelone_rate_limited_retry(lambda: angelone_ltp(headers, exch, token))
            except Exception:
                continue
            chg = pick_change(ltp, prev_close)
            now_str = ist_now().strftime("%H:%M:%S")
            with CACHE_LOCK:
                card["value"] = fnum(ltp)
                card["change"] = chg
                card["change_text"] = f"{chg:+.2f}%" if chg is not None else "—"
                card["updated"] = now_str
                if check_id and check_id in checks_by_id:
                    v, vc = vix_verdict(chg) if chg is not None else ("Normal", "flat")
                    chk = checks_by_id[check_id]
                    chk["verdict"], chk["verdict_class"] = v, vc
                    chk["value"] = f"{fnum(ltp)} ({chg:+.2f}%)" if chg is not None else fnum(ltp)
                    chk["reason"] = f"Δ {chg:+.2f}%" if chg is not None else ""


# --------------------------------------------------------------------------
# HTTP server
# --------------------------------------------------------------------------

INDEX_HTML_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "index.html")


def _read_html():
    with open(INDEX_HTML_PATH, encoding="utf-8") as _fh:
        return _fh.read()


def _autoreload_loop():
    """Restarts this process in place the moment app.py (or any file it
    imports from this directory) changes on disk — so a code edit takes
    effect on the next request without anyone having to kill/restart
    manually. index.html doesn't need this (served fresh via _read_html
    on every request already)."""
    watched = os.path.join(os.path.dirname(os.path.abspath(__file__)), "app.py")
    last_mtime = os.path.getmtime(watched)
    while True:
        time.sleep(1)
        try:
            mtime = os.path.getmtime(watched)
        except OSError:
            continue
        if mtime != last_mtime:
            os.execv(sys.executable, [sys.executable] + sys.argv)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _send(self, code, body, ctype):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = urllib.parse.urlparse(self.path).path
        if path == "/" or path == "/index.html":
            self._send(HTTPStatus.OK, _read_html().encode("utf-8"), "text/html; charset=utf-8")
        elif path == "/api/market":
            snap = {}
            with CACHE_LOCK:
                snap = dict(CACHE.get("market") or {})
            if not snap:
                try:
                    snap = build_market()
                    with CACHE_LOCK:
                        CACHE["market"] = snap
                except Exception as exc:
                    snap = {"error": str(exc),
                            "generated_at": ist_now().strftime("%Y-%m-%d %H:%M:%S"),
                            "market": market_info(),
                            "refresh_seconds": REFRESH_SECONDS,
                            "checks": [], "cards": [],
                            "build": BUILD_COMMIT, "build_started": BUILD_STARTED}
            body = json.dumps(snap).encode("utf-8")
            self._send(HTTPStatus.OK, body, "application/json")
        elif path == "/api/news":
            with CACHE_LOCK:
                snap = dict(CACHE.get("news") or {})
            if not snap:
                try:
                    snap = build_news_with_trend()
                    with CACHE_LOCK:
                        CACHE["news"] = snap
                except Exception as exc:
                    snap = {"error": str(exc), "items": [], "trend": None,
                            "sentiment": {"overall": "Unavailable", "class": "flat",
                                          "positive": 0, "negative": 0, "neutral": 0,
                                          "high_impact_count": 0},
                            "generated_at": ist_now().strftime("%Y-%m-%d %H:%M:%S")}
            body = json.dumps(snap).encode("utf-8")
            self._send(HTTPStatus.OK, body, "application/json")
        elif path == "/health":
            self._send(HTTPStatus.OK, b'{"status":"ok"}', "application/json")
        else:
            self._send(HTTPStatus.NOT_FOUND, b"not found", "text/plain")


def main():
    global BUILD_STARTED
    BUILD_STARTED = ist_now().strftime("%Y-%m-%d %H:%M:%S")
    threading.Thread(target=_refresh_loop, daemon=True).start()
    threading.Thread(target=_news_loop, daemon=True).start()
    threading.Thread(target=_angel_tick_loop, daemon=True).start()
    threading.Thread(target=_autoreload_loop, daemon=True).start()
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    print(f"Pre-market dashboard on http://localhost:{PORT} (build {BUILD_COMMIT}, started {BUILD_STARTED} IST)")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nstopped")


if __name__ == "__main__":
    main()