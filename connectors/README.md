# Connectors

Standalone Go module (`connectors`, no dependency on `engine/`). Every data point an AI proposal layer would need to generate a NIFTYBEES DSL strategy — gathered here BEFORE any strategy runs, per the intended workflow: **data first, then strategy generation, then engine execution.** All free. Maximum data pulled from Angel One (matches what the trading engine itself executes against); external free sources fill the handful of gaps Angel One doesn't cover.

Live-tested in this build (2026-08-01) — confirmed working, not just compiling:

| Connector | Package | Data | Source | Status |
|---|---|---|---|---|
| Price + quote | `yahoo` | NIFTYBEES OHLCV/quote, NIFTY 50, India VIX | Yahoo Finance chart API (free, no auth) | ✅ live-verified |
| Global cues | `yahoo` | Dow, Nasdaq, Crude WTI, USD/INR | Yahoo Finance chart API | ✅ live-verified (same mechanism as above) |
| NAV | `amfi` | NIFTYBEES official daily NAV | AMFI `NAVAll.txt` (free, no auth, published daily by AMFI itself) | ✅ live-verified |
| Instrument master | `angelone` | Lot size, tick size, expiry list, every NIFTY option/future token | Angel One public scrip master JSON (free, no login) | ✅ live-verified — found NIFTYBEES (token 10576, tick ₹0.01, lot 1) and 18 live NIFTY option expiries |
| Index tokens (spot) | `angelone` | NIFTY 50 and INDIA VIX index instrument lookup | Angel One scrip master | ✅ live-verified — NIFTY 50 token 99926000, INDIA VIX token 99926017 |
| Login + authenticated session | `angelone` (`Login`) | TOTP+PIN login → JWT/feed token | Angel One SmartAPI `loginByPassword` | ✅ live-verified (2026-08-02) with real user credentials — and a real bug fixed getting here: SmartAPI rejects every authenticated call (login included) with `HTTP 400 "Required header 'X-MACaddress' is missing"` unless `X-ClientLocalIP`/`X-ClientPublicIP`/`X-MACAddress` are set; this client only sent `X-PrivateKey`/`X-UserType`/`X-SourceID`. Fixed in both this module's client and the engine's separate `internal/marketdata/angelone` copy. SmartAPI doesn't appear to verify these values match real network info — a placeholder is the documented/commonly-used approach |
| Price (broker-matched) | `angelone` | NIFTYBEES/NIFTY/VIX quotes + historical candles | Angel One SmartAPI quote/historical endpoints | ✅ live-verified (2026-08-02) — real authenticated NIFTYBEES quote pulled (LTP ₹277.42) after the header fix above |
| Option chain OI / PCR / max pain | `angelone` | Full NIFTY option chain OI+LTP per strike, derived PCR and max pain | Angel One SmartAPI quote endpoint (scrip master + authenticated quotes) | Login path confirmed working (see above); this specific call not yet exercised live |
| Option Greeks + IV | `angelone` (`greeks.go`) | Delta/Gamma/Theta/Vega/**Implied Volatility** per strike, one call | Angel One's dedicated Option Greeks endpoint (`POST .../marketData/v1/optionGreek`) — confirmed real via Angel One's own SmartAPI forum announcement, not the plain quote endpoint (which has no IV field) | Login path confirmed working (see above); this specific call not yet exercised live |
| NIFTY futures + basis | `angelone` (`futures.go`) | Future LTP, OI, and computed basis/premium vs spot | Angel One quote endpoint (future + spot NIFTY 50 index token) | Login path confirmed working (see above); this specific call not yet exercised live. (Previously only had instrument lookup; fetch+basis calc added) |
| News | `news` | Market headlines | Economic Times Markets RSS, Moneycontrol RSS (free) | ✅ live-verified. Business Standard RSS tested and dropped (403) |
| Web page reader | `webreader` | Any URL → clean Markdown (news articles, AMC fund pages, anything without a structured API) | Jina AI Reader (`r.jina.ai/<url>`, free, no key) | ✅ mechanism confirmed working, but unreliable on the free/anonymous tier: shared IP pool gets rate-limited on popular domains (investing.com, Wikipedia, Google all 403'd in testing) independent of this code, and it can't execute JS (SPA pages like Groww come back empty). Best for less-hammered server-rendered pages |
| RBI policy | `rbi` | Reactive policy announcements | RBI press-release RSS (free) | ✅ endpoint live-verified reachable; see package doc — forward MPC calendar has no stable free API, must be updated manually ~twice a year |
| Holidays | `nse` | NSE trading holiday calendar | nseindia.com frontend API | ✅ live-verified (2026-08-02 re-test) — 20 real 2026 holiday entries returned. NSE's blocking is time/IP-variable: earlier sessions saw 403 from this same sandbox, this retest got 200. Treat as best-effort regardless of a working retest — has cookie warm-up built in (`httpx.WarmUpNSE`) |
| Corporate actions (dividends) | `nse` | NIFTYBEES dividend/corp-action history | nseindia.com frontend API | ✅ live-verified, and a real bug fixed getting here: the endpoint path was wrong (`corporate-actions` doesn't exist, correct path is `corporates-corporateActions`) and the purpose field was mapped from the wrong JSON key (`purpose` doesn't exist, real key is `subject`). Both fixed and confirmed against NIFTYBEES's real 2019 face-value-split record |
| Corporate announcements (general) | `nse` (`FetchAnnouncements`) | Board meetings, earnings, mergers, trading-window closures — broader than just dividends | nseindia.com frontend API | ✅ live-verified against RELIANCE (3,326 real entries) — and a second real bug fixed here: the response has no `subject` field at all (would've always come back empty); the category text is actually in `desc`, detail text in `attchmntText`. NIFTYBEES itself correctly returns `[]` (an ETF has no board meetings/earnings — not a bug) |
| FII/DII flow | `nse` | Daily institutional cash flow | nseindia.com frontend API | ✅ live-verified (2026-08-02 re-test) — real DII/FII net flow figures for the latest trading day returned and field-matched correctly |
| NIFTY 50 TRI (benchmark) | `tri` + `store` | Total Return Index history, cached locally | CSV export from niftyindices.com dropped into a watch folder, auto-imported | ✅ live-tested (import, dedupe-on-reimport, retention) — see `tri` package doc for why this is CSV-drop rather than live-scrape |
| Overnight/pre-market signal | `overnight` | GIFT Nifty → (SGX, discontinued) → NIFTY futures basis → US markets+USD/INR composite → none, first available rung wins, with a confidence score per rung | Cascades the sources above; GIFT Nifty slot is pluggable (`GiftProvider`), not hardcoded unsupported | ✅ live-tested falling through to the US-markets rung (no GIFT/futures configured) |
| Local cache + retention | `store` | Generic (source, key, date) → value snapshot store, per-source keep-days policy, `Prune()` auto-deletes stale rows | SQLite, separate file from engine's `trading.db` | ✅ live-tested: prunes old `news` rows, keeps `nifty_tri`/`nav` forever |
| Historical backtest data | `historical` | 5 years of NIFTYBEES daily OHLCV, `SyncHistory`/`Refresh`/`GetHistory`/`LatestCandle` | Yahoo Finance chart API, cached via `store` | ✅ live-tested: synced 1,238 real daily candles, `Refresh()` confirmed idempotent (upserts overlapping days without duplicating) |
| News sentiment scoring | `sentiment` | Bullish/neutral/bearish + score per headline, aggregate over a batch | Finance-tuned lexicon scorer (own word list, NOT the published VADER lexicon reproduced from memory — see package doc) applied to `news` headlines | ✅ live-tested on 50 real headlines (17 bullish/26 neutral/7 bearish that day) and on hand-written positive/negative/negated/neutral cases |
| Market breadth (advance/decline, 52w highs/lows) | `nse` (`FetchMarketBreadth`) | Advances, declines, unchanged, advance/decline ratio, count of stocks near 52-week high/low, across ~2,000 listed stocks | nseindia.com pre-open snapshot endpoint | ✅ live-tested and reachable — unlike NSE's other endpoints (holidays/corp-actions/FII-DII, all 403'd in this sandbox), this one returned real data (1,319 advances / 448 declines / 29 new highs) even without the cookie warm-up |
| Global market context | `global` | S&P 500, US VIX, Dow, Nasdaq, Nikkei 225, Hang Seng, Shanghai Composite, Gold, Silver, Crude WTI, USD/INR, Dollar Index (DXY), reduced to a disclosed `risk_mode`/`confidence` composite + per-pillar bullish/bearish/neutral labels (not raw quotes an LLM would have to interpret itself) — plus market-session open/closed booleans and curated global-event headlines (keyword-filtered from `news`/`rbi`, no AI-generated "impact" score, see `events.go`) | Yahoo Finance chart API (new tickers) | ✅ live-verified (2026-08-02) — all 8 new tickers returned real prices; full round-trip confirmed through `contextbuilder`'s `global_market` section against the running engine |

## Checked and deliberately NOT added

- **iNAV (live intraday NAV)** — no free reliable source exists. Only official path is NSE's paid real-time market-data feed. AMC websites publish it inconsistently. EOD NAV (AMFI) is what's covered.
- **GIFT Nifty as a live free API** — Angel One's scrip master has zero NSE-IX/GIFT City instruments (checked directly). Upstox's Global Instruments API is the one real option, but means onboarding an entirely new broker (new account, new API key) for a single data point — not free-of-friction, so not added yet. Free web scraping attempts (Groww, investing.com) failed: Groww is JS-rendered (empty via Jina), investing.com rate-limited Jina's anonymous tier. Handled instead via `overnight`'s pluggable cascade (see table above) rather than a hardcoded "unsupported."
- **Reddit sentiment** — checked live: both `reddit.com/r/*.json` and `old.reddit.com/r/*.json` returned HTTP 403 to anonymous requests from this environment. Reddit locked down anonymous JSON access in 2023; it's not a "use Jina Reader" workaround away, it's gone without an OAuth app registration. Not added.
- **Google Trends** — checked live: the commonly-referenced `trends.google.com/trends/api/dailytrends` endpoint returned 404 (deprecated/moved). The unofficial workarounds that do still work require a multi-step session-token handshake that's notoriously fragile and undocumented — not worth building against for a "free and reliable" requirement.

See `BACKLOG.md` for the full sequenced list of what's deferred and why (multiple web readers beyond Jina/PlainHTTP, a backup broker, paid feeds, folder reorganization) — nothing there is a silent gap, each has a stated reason it's not built yet.

## Why this split

Angel One covers price, VIX, option chain, Greeks/IV, futures, and the full instrument master (lot size/tick size/expiry) — everything with a real broker API. What Angel One genuinely does not have: fund NAV (AMFI), news, RBI policy, NSE-only administrative data (holidays, corp actions/announcements, FII/DII), and arbitrary web pages (Jina). Those get their own free sources rather than forcing them through a broker API that was never going to have them.

## What's NOT wired into the engine yet

This module only *fetches* data. The trading engine's DSL/indicator registry (`engine/internal/dsl`, `engine/internal/indicators`) has no `pcr`, `max_pain`, `fii_flow`, or news-sentiment indicator type yet, and DSL's `calendar` block only knows `holiday`/`expiry_day` — nothing for "block entry before RBI policy day." Wiring option-chain/news/macro data into an actual strategy condition requires extending the DSL spec and indicator registry — this connectors module is the data layer that extension would consume, not a replacement for it.

## Usage pattern

Every connector takes a `context.Context` and an `*http.Client` (use `httpx.New()` — it carries the cookie jar the NSE connectors need) rather than owning global state, so a future orchestrator (`agent/`) can fetch everything in parallel and assemble one snapshot before generating a strategy.

```go
client := httpx.New()
ctx := context.Background()

candles, quote, _ := yahoo.FetchCandles(ctx, client, yahoo.SymbolNiftyBees, "1d", "3mo")
nav, _ := amfi.FetchNiftyBeesNAV(ctx, client)
instruments, _ := angelone.FetchScripMaster(ctx, client)
headlines, _ := news.FetchAll(ctx, client)
page, _ := webreader.Read(ctx, client, "https://example.com/some-article")
```

Authenticated Angel One calls (candles, option chain, Greeks, futures) need a logged-in `*angelone.Client`:

```go
ao := angelone.NewClient(apiKey, clientCode, pin, totpSecret)
ao.Login(ctx) // touches your real broker session — call explicitly, never automatic

greeks, _ := ao.GetOptionGreeks(ctx, "NIFTY", "28AUG2026")
futures, _ := ao.FetchNiftyFutures(ctx, instruments, "28AUG2026", nifty50Token)
```

## Backtest handoff to the engine

`historical` gives you candles; the engine's `POST /backtest` runs them through the exact same strategy runtime used for live/paper trading (see `engine/internal/backtest`). This is the "agent gives DSL, gets a result to learn from" loop:

```go
st, _ := store.Open(ctx, "cache.db")
historical.SyncHistory(ctx, client, st, historical.DefaultSymbol, historical.DefaultYahooTicker, 5)
candles, _ := historical.GetHistory(ctx, st, historical.DefaultSymbol, fiveYearsAgo, time.Now())
// build the engine's POST /backtest JSON body: {"strategy": <DSL>, "candles": [{"time":...,"open":...,...}], "starting_capital": 100000}
```

Live-tested end to end (2026-08-01): real EMA(20/50) crossover strategy against 5 real years of NIFTYBEES data — 9 completed trades, 66.7% win rate, profit factor 3.22, CAGR 1.09%, max drawdown 1.68%, entry/exit prices matching actual historical levels. Full response includes trades, metrics (win rate, profit factor, Sharpe/Sortino, CAGR, drawdown, best/worst trade), an equity curve, and an `ai_review` block in the exact DSL_SPEC Sec 27 shape the agent already consumes from live trading — backtest and live learning use the same schema.

Run the live smoke tests (hits real endpoints, ~10s):

```
cd connectors
go test ./... -v
```
