package api

import (
	"net/http"

	"tradingengine/internal/marketsession"
)

// handleSystemLogs returns engine-wide events (market open/close,
// auto-pause/resume) — the same LogRepo per-strategy logs already use,
// queried under the reserved marketsession.SystemLogStrategyID sentinel,
// so an AI agent has one consistent API for both per-strategy and
// system-wide history instead of a separate table/endpoint shape.
func (s *Server) handleSystemLogs(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Logs.ListByStrategy(marketsession.SystemLogStrategyID, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}
