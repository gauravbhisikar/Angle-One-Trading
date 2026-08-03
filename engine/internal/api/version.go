package api

import "net/http"

// handleVersion answers "is this actually the new build" without SSH —
// load the dashboard, check the footer (or curl this endpoint) against
// the commit you just pushed. See cmd/engine/main.go's buildCommit.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"commit": s.BuildCommit})
}
