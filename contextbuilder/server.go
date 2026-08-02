package contextbuilder

import (
	"encoding/json"
	"io"
	"net/http"

	"memory"
)

// Server exposes Builder.Build and Research over HTTP — the boundary a
// Python (LangGraph) agent talks across, since it can't import Go
// packages directly. Same "processes talk HTTP" pattern as everything
// else in this project (the engine's own API).
type Server struct {
	builder    *Builder
	httpClient *http.Client
	mux        *http.ServeMux
}

func NewServer(builder *Builder, mgr *memory.Manager) *Server {
	s := &Server{builder: builder, httpClient: NewHTTPClient(), mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /context/build", s.handleBuildContext)
	s.mux.HandleFunc("POST /research/query", s.handleResearch)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.registerMemoryRoutes(mgr)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleBuildContext(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req BuildRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Task == "" {
		req.Task = TaskBuildStrategy
	}
	if req.Symbol == "" {
		req.Symbol = "NIFTYBEES"
	}

	dc, err := s.builder.Build(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dc)
}

type researchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func (s *Server) handleResearch(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req researchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.MaxResults <= 0 {
		req.MaxResults = 5
	}

	findings, err := Research(r.Context(), s.httpClient, req.Query, req.MaxResults)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"findings": findings})
}
