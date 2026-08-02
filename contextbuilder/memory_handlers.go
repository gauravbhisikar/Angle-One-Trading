package contextbuilder

import (
	"encoding/json"
	"net/http"
	"time"

	"memory"
)

// Memory-write endpoints. contextbuilder-server is the sole HTTP gateway
// a Python agent has into the Go-only memory module (it can't import Go
// packages directly) — same reasoning as the read-side providers already
// wrapping connectors/engine. Wired into the same *Server so an agent
// talks to one process for both "read context" and "write what happened."
func (s *Server) registerMemoryRoutes(mgr *memory.Manager) {
	s.mux.HandleFunc("POST /memory/strategy", s.handleSaveStrategy(mgr))
	s.mux.HandleFunc("POST /memory/context", s.handleSaveContext(mgr))
	s.mux.HandleFunc("POST /memory/backtest", s.handleSaveBacktest(mgr))
	s.mux.HandleFunc("POST /memory/deployment", s.handleSaveDeployment(mgr))
	s.mux.HandleFunc("POST /memory/lesson", s.handleRecordLesson(mgr))
	s.mux.HandleFunc("GET /memory/lessons", s.handleGetLessons(mgr))
}

func (s *Server) handleSaveStrategy(mgr *memory.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var rec memory.StrategyRecord
		if err := json.Unmarshal(body, &rec); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if err := mgr.SaveStrategy(r.Context(), rec); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
	}
}

func (s *Server) handleSaveContext(mgr *memory.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var snap memory.ContextSnapshot
		if err := json.Unmarshal(body, &snap); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if err := mgr.SaveContext(r.Context(), snap); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
	}
}

func (s *Server) handleSaveBacktest(mgr *memory.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var rec memory.BacktestRecord
		if err := json.Unmarshal(body, &rec); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if err := mgr.SaveBacktest(r.Context(), rec); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
	}
}

func (s *Server) handleSaveDeployment(mgr *memory.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var dep memory.Deployment
		if err := json.Unmarshal(body, &dep); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if dep.StartedAt.IsZero() {
			dep.StartedAt = time.Now().UTC()
		}
		if err := mgr.SaveDeployment(r.Context(), dep); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
	}
}

type recordLessonRequest struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Success     bool   `json:"success"`
}

func (s *Server) handleRecordLesson(mgr *memory.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var req recordLessonRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if err := mgr.RecordLesson(r.Context(), req.Key, req.Description, req.Success); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
	}
}

func (s *Server) handleGetLessons(mgr *memory.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lessons, err := mgr.GetLessons(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, lessons)
	}
}
