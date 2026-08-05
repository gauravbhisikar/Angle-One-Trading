package api

import (
	_ "embed"
	"net/http"
)

//go:embed web/dashboard.html
var dashboardHTML []byte

//go:embed web/login.html
var loginHTML []byte

//go:embed web/nifty_history.json
var niftyHistorySnapshot []byte

// Intraday snapshots — real Yahoo Finance candles, one file per timeframe,
// each as deep as Yahoo's own per-interval retention actually allows (it
// caps intraday history: ~7d for 1m, ~60d for 5m/15m/30m, ~2y for 60m —
// there is no free source for years of 1-5min bars, so this is real data
// honestly scoped to what's actually available, not a synthesized 5-year
// series). Regenerate via the fetch script noted in agent/README.md and
// rebuild to refresh.
//
//go:embed web/nifty_intraday_1m.json
var niftyIntraday1m []byte

//go:embed web/nifty_intraday_5m.json
var niftyIntraday5m []byte

//go:embed web/nifty_intraday_15m.json
var niftyIntraday15m []byte

//go:embed web/nifty_intraday_30m.json
var niftyIntraday30m []byte

//go:embed web/nifty_intraday_1h.json
var niftyIntraday1h []byte

var intradaySnapshots = map[string][]byte{
	"1m": niftyIntraday1m, "5m": niftyIntraday5m, "15m": niftyIntraday15m,
	"30m": niftyIntraday30m, "1h": niftyIntraday1h,
}

// handleDashboard serves the built-in live dashboard. Same-origin static
// asset — no external CDN dependency, works fully offline against this
// same process's API.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The dashboard is a single embedded HTML/JS file rebuilt on every
	// deploy — without an explicit no-store, browsers can keep serving a
	// stale cached copy after a redeploy (no Last-Modified/ETag is sent,
	// so heuristic caching kicks in), which looks exactly like "the
	// deploy didn't take" even though the server is running the new code.
	w.Header().Set("Cache-Control", "no-store")
	w.Write(dashboardHTML)
}

// handleSampleHistory serves a bundled real 5-year NIFTYBEES daily candle
// snapshot (fetched once via connectors/historical, embedded at build
// time) so the dashboard's Strategy Lab can run real POST /backtest calls
// without the engine ever fetching external data itself — that boundary
// (connectors fetch, engine only executes against what it's given,
// ENGINE_SPEC Sec 12.1) stays intact; this is pre-fetched data served
// statically, not a live external call from the engine process. Refresh
// by regenerating web/nifty_history.json via connectors/historical and
// rebuilding.
func (s *Server) handleSampleHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(niftyHistorySnapshot)
}

// handleSampleIntradayHistory serves a bundled real intraday candle
// snapshot for the requested ?timeframe= (1m, 5m, 15m, 30m, 1h — matching
// the DSL's timeframe field), same "pre-fetched, not a live external call
// from the engine" boundary as handleSampleHistory. 404s for an
// unbundled timeframe rather than silently substituting a different one.
func (s *Server) handleSampleIntradayHistory(w http.ResponseWriter, r *http.Request) {
	tf := r.URL.Query().Get("timeframe")
	data, ok := intradaySnapshots[tf]
	if !ok {
		writeError(w, http.StatusNotFound, "no bundled intraday dataset for timeframe "+tf)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
