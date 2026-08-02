# Backlog

Deferred items — not gaps hidden, gaps sequenced. Each one has a reason it's not done now, not just a priority label.

## P0 — done this round

- ~~TRI daily importer + cache~~ → `tri/` + `store/` (CSV-drop automated, see package docs for why live-scrape wasn't viable)
- ~~GIFT Nifty provider abstraction~~ → `overnight/` cascade, pluggable `GiftProvider` slot
- ~~Automated retention/cleanup~~ → `store.Prune()`, per-source `KeepDays` policy, live-tested

## P1 — next, when there's a reason to reach for them

- **Multiple web readers** — `webreader.Reader` interface exists with 2 implementations (Jina, PlainHTTP) and `FetchWithFallback`. Not yet added: Firecrawl/Crawl4AI (need API keys, contradicts "free" requirement — add only if a free tier appears or the user accepts a paid key).
- **Backup broker API (Upstox)** — only matters the day Angel One's rate limits or an outage actually bites, or GIFT Nifty becomes a real requirement (Upstox is the free path to it, per GPT's research). Don't add speculative broker integration before there's a concrete need — it's a new account, new credentials, new failure surface for zero present benefit.
- **Scheduled Prune() + AutoImport() calls** — both exist and are tested, but nothing calls them on a timer yet. Wire into `agent/`'s orchestrator (or a small cron) once that exists, rather than inventing a scheduler here for a module with no long-running process today.

## P2 — real cost, no current justification

- **Paid NSE real-time feed (for live iNAV)** — the only way to get genuinely live iNAV. Not justified unless the strategy actually needs intraday ETF premium/discount vs NAV, which nothing built so far does.
- **market-data/ vs research/ folder split** — organizational only (Angel One/AMFI/NSE vs Jina/news/filings), no functional gap behind it. Deferred rather than done now because reorganizing already-tested import paths for a naming preference risks breaking something for zero behavior change. Revisit if/when this module's package count grows enough that the flat layout actually gets confusing (it's 10 packages right now — not there yet).
- **Reddit/GitHub/X/YouTube-transcript connectors** (the "Agent Reach" category) — no free API for any of these without a key (Reddit/X both gate behind developer accounts now). `webreader.Reader` already covers "read an arbitrary URL"; a Reddit-specific wrapper adds little beyond that until there's a concrete need to parse Reddit's specific data shape (threads, upvotes, comment trees).

## P1 addendum — sentiment round

- ~~News sentiment scoring~~ → `sentiment/` (own finance lexicon, live-tested on real headlines)
- ~~Market breadth / advance-decline~~ → `nse.FetchMarketBreadth` (found NSE's pre-open endpoint actually works from this sandbox, unlike its siblings — real find, not in the original ask)
- **Reddit sentiment, Google Trends** — checked live, both rejected (403 / 404, see README "Checked and deliberately NOT added"). Not a "come back later" item — Reddit's anonymous JSON access is gone at the platform level, not a transient block.
- **Market Regime Engine** (combine VIX + FII/DII + breadth + news sentiment + trend into one scored "Bull Trend, 89% confidence, recommended strategies: ✅ Momentum ⚠️ Avoid Mean Reversion" output) — deliberately NOT built as a connector. This is a scoring/weighting decision (what weight does breadth get vs VIX vs sentiment?) with no validated model behind it yet — building fixed weights now would be guessing, the same premature-abstraction trap avoided earlier with the generic cross-connector confidence type. This is agent-layer work: connectors fetch the raw signals (all of which now exist — VIX, FII/DII, breadth, PCR, sentiment, overnight cascade), the agent (`agent/README.md`) is where combining them into a regime call and a strategy recommendation belongs, once there's a concrete method (backtested weights, or the AI reasoning over the raw signals directly) rather than an invented formula.

## Explicitly not planned

- **NIFTY TRI live scrape** — 2 real attempts against niftyindices.com's private WebMethod, both rejected with an undocumented payload shape. Not chasing further; it's an internal endpoint that can change without notice even if reverse-engineered. CSV-drop (`tri/`) is the durable answer, not a stopgap.
- **Confidence scoring beyond the overnight cascade** — `overnight.Signal.Confidence` is a real, if simple, model (fixed per-rung trust level: GIFT 0.95, futures-basis 0.7, US-markets-proxy 0.5). Generalizing this into a cross-cutting "confidence" type for every connector (news sentiment, PCR reading, etc.) is speculative until there's a second concrete signal that actually needs to be combined/compared against another — building the abstraction before the second use case tends to guess the wrong shape.
