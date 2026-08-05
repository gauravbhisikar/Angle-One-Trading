// Package api exposes the engine over HTTP: create/validate/run/pause/
// resume/stop strategies, and read portfolio/trades/orders/analytics/
// daily-review/logs. Uses the standard library's net/http (Go 1.22+
// method+pattern ServeMux) — no external router needed.
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/analytics"
	"tradingengine/internal/dsl"
	"tradingengine/internal/execution"
	"tradingengine/internal/featurestore"
	"tradingengine/internal/models"
	"tradingengine/internal/portfolio"
	"tradingengine/internal/scheduler"
	"tradingengine/internal/storage"
	"tradingengine/internal/strategy"
)

// Config carries NewServer's dependencies as a plain value type (no
// mutex/map) so it can be passed and copied freely; Server itself holds
// the runtime state (mutex-guarded ledger map, mux).
type Config struct {
	Engine       *scheduler.Engine
	Strategies   *storage.StrategyRepo
	Orders       *storage.OrderRepo
	Trades       *storage.TradeRepo
	Logs         *storage.LogRepo
	Reviews      *storage.ReviewRepo
	Predicted    *storage.PredictedMetricsRepo
	Broker       execution.BrokerAdapter
	FeatureStore *featurestore.Store

	DefaultStartingCapital decimal.Decimal

	// PriceLookup is the exact same last-candle-close lookup the paper
	// broker fills orders against (see cmd/engine/main.go) — surfaced
	// here too so the dashboard can show a real "live price" for an open
	// position instead of just the entry price, without a second,
	// possibly-inconsistent price source.
	PriceLookup func(symbol string) (decimal.Decimal, bool)

	// BuildCommit is the git short commit hash baked in at build time
	// (deploy.sh's -ldflags) — exposed via GET /version and shown in the
	// dashboard footer so a redeploy is verifiable by looking at the page.
	BuildCommit string

	// FeedMode is "live" or "mock" — whichever price feed cmd/engine/main.go
	// actually ended up constructing (USE_ANGEL_LIVE can be set but the
	// engine still falls back to mock on a boot-time login/connect
	// failure). Exposed via GET /version for the same reason as
	// BuildCommit: whether the system is trading against real prices should
	// be visible at a glance, not something to infer from behavior.
	FeedMode string

	// LoginUsername/LoginPassword/LoginKey gate every non-loopback request
	// behind a login form (see authMiddleware) — the dashboard now runs on
	// a public IP against a real broker feed, with no auth otherwise. Any
	// of the three left empty disables the gate entirely (today's open
	// behavior) — intentional so a fresh local clone isn't locked out by
	// default; the real deployment gets real values set in its own .env.
	LoginUsername string
	LoginPassword string
	LoginKey      string
}

type Server struct {
	Config
	mux *http.ServeMux

	mu      sync.Mutex
	ledgers map[string]*portfolio.Ledger

	// lastStatus survives after StopStrategy tears down the runtime (which
	// frees its concurrency slot, ENGINE_SPEC Sec 0.6) — without this, a
	// stopped strategy would look identical to one that never ran.
	lastStatus map[string]string

	// sessions maps an opaque session token (the cookie value) to its
	// expiry. In-memory only, single-process — this is a single-user
	// system, no need for real session infrastructure; a restart simply
	// logs everyone out, same as any other in-memory state here.
	sessions map[string]time.Time
}

func NewServer(cfg Config) *Server {
	s := &Server{
		Config: cfg, ledgers: map[string]*portfolio.Ledger{}, lastStatus: map[string]string{},
		sessions: map[string]time.Time{},
	}
	s.mux = http.NewServeMux()
	s.routes()
	return s
}

// Handler wraps the router in authMiddleware — every route (including
// ones registered by routes() below) goes through the login gate, except
// requests from loopback (see authMiddleware) and POST /login itself.
func (s *Server) Handler() http.Handler { return s.authMiddleware(s.mux) }

// loginConfigured reports whether the login gate is actually active — all
// three of username/password/key must be set. Left unconfigured (e.g. a
// fresh local clone with no .env) means the gate is a no-op passthrough,
// exactly today's behavior.
func (s *Server) loginConfigured() bool {
	return s.LoginUsername != "" && s.LoginPassword != "" && s.LoginKey != ""
}

const sessionCookieName = "engine_session"
const sessionTTL = 7 * 24 * time.Hour

// isLoopback reports whether the request arrived over the loopback
// interface — used to let agent/contextbuilder's existing
// ENGINE_URL=http://localhost:9080 calls keep working with zero changes on
// the Python side. The login gate exists for the *public* IP a browser
// hits, not for the engine's own internal callers on the same machine.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// authMiddleware is a no-op passthrough when the login gate isn't
// configured (loginConfigured() false) or the caller is on loopback.
// Otherwise: POST /login is always reachable; anything else needs a valid
// session cookie, or gets the login page (GET, browser navigation) or a
// 401 JSON body (everything else — the dashboard's own XHR calls before
// its cookie exists).
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.loginConfigured() || isLoopback(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/login" && r.Method == http.MethodPost {
			s.handleLogin(w, r)
			return
		}
		if s.hasValidSession(r) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			w.Write(loginHTML)
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized")
	})
}

func (s *Server) hasValidSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.sessions[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(s.sessions, c.Value)
		return false
	}
	return true
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Key      string `json:"key"`
}

// handleLogin is intentionally reachable without a session (it's how one
// gets created) but is otherwise not registered on s.mux — authMiddleware
// intercepts POST /login before the router ever sees it, so it works
// regardless of what routes() does or doesn't register.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// subtle.ConstantTimeCompare avoids a timing side-channel on credential
	// comparison; all three must match, and constant-time comparison of the
	// full request happens regardless of which field first differs.
	ok := subtle.ConstantTimeCompare([]byte(req.Username), []byte(s.LoginUsername)) == 1
	ok = subtle.ConstantTimeCompare([]byte(req.Password), []byte(s.LoginPassword)) == 1 && ok
	ok = subtle.ConstantTimeCompare([]byte(req.Key), []byte(s.LoginKey)) == 1 && ok
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)

	s.mu.Lock()
	s.sessions[token] = time.Now().Add(sessionTTL)
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /", s.handleDashboard)
	s.mux.HandleFunc("GET /backtest/sample-data", s.handleSampleHistory)
	s.mux.HandleFunc("GET /backtest/sample-data-intraday", s.handleSampleIntradayHistory)
	s.mux.HandleFunc("GET /market/status", s.handleMarketStatus)
	s.mux.HandleFunc("GET /market/price/{symbol}", s.handleMarketPrice)
	s.mux.HandleFunc("GET /version", s.handleVersion)
	s.mux.HandleFunc("GET /system/logs", s.handleSystemLogs)
	s.mux.HandleFunc("GET /strategies", s.handleListStrategies)
	s.mux.HandleFunc("GET /strategies/{id}/equity-curve", s.handleEquityCurve)
	s.mux.HandleFunc("POST /backtest", s.handleBacktest)
	s.mux.HandleFunc("POST /features/compute", s.handleFeaturesCompute)
	s.mux.HandleFunc("GET /features/{symbol}", s.handleFeaturesQuery)
	s.mux.HandleFunc("POST /features/{symbol}/macro", s.handleFeaturesMacro)
	s.mux.HandleFunc("POST /strategies", s.handleCreateStrategy)
	s.mux.HandleFunc("POST /strategies/validate", s.handleValidate)
	s.mux.HandleFunc("POST /strategies/{id}/run", s.handleRun)
	s.mux.HandleFunc("POST /strategies/{id}/pause", s.handlePause)
	s.mux.HandleFunc("POST /strategies/{id}/resume", s.handleResume)
	s.mux.HandleFunc("POST /strategies/{id}/stop", s.handleStop)
	s.mux.HandleFunc("DELETE /strategies/{id}", s.handleDelete)
	s.mux.HandleFunc("GET /strategies/{id}/portfolio", s.handlePortfolio)
	s.mux.HandleFunc("GET /strategies/{id}/trades", s.handleTrades)
	s.mux.HandleFunc("GET /strategies/{id}/orders", s.handleOrders)
	s.mux.HandleFunc("GET /strategies/{id}/analytics", s.handleAnalytics)
	s.mux.HandleFunc("POST /strategies/{id}/predicted-metrics", s.handleSavePredictedMetrics)
	s.mux.HandleFunc("GET /strategies/{id}/predicted-metrics", s.handleGetPredictedMetrics)
	s.mux.HandleFunc("GET /strategies/{id}/daily-review", s.handleDailyReview)
	s.mux.HandleFunc("GET /strategies/{id}/daily-reviews", s.handleListDailyReviews)
	s.mux.HandleFunc("GET /strategies/{id}/logs", s.handleLogs)
	s.mux.HandleFunc("GET /strategies/{id}/live-indicators", s.handleLiveIndicators)
	s.mux.HandleFunc("GET /strategies/{id}/dsl", s.handleGetDSL)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleCreateStrategy(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	strat, err := dsl.Parse(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result := dsl.Validate(strat)
	if !result.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{"errors": result.Errors, "warnings": result.Warnings})
		return
	}

	if err := s.Strategies.SaveVersion(strat, body); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"strategy_id": strat.StrategyID, "strategy_version": strat.StrategyVersion, "warnings": result.Warnings,
	})
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	strat, err := dsl.Parse(body)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{"errors": []dsl.ValidationError{{Path: "$", Reason: err.Error()}}})
		return
	}
	result := dsl.Validate(strat)
	status := http.StatusOK
	if !result.Valid() {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]interface{}{"valid": result.Valid(), "errors": result.Errors, "warnings": result.Warnings})
}

func (s *Server) getLedger(strategyID string) *portfolio.Ledger {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.ledgers[strategyID]
	if !ok {
		l = portfolio.NewLedger(s.DefaultStartingCapital)
		s.ledgers[strategyID] = l
	}
	return l
}

// peekLedger reads an existing ledger without creating one — used by
// read-only endpoints (the strategy list) so simply listing strategies
// never has the side effect of allocating capital to one that hasn't run.
func (s *Server) peekLedger(strategyID string) (*portfolio.Ledger, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.ledgers[strategyID]
	return l, ok
}

func (s *Server) setStatus(strategyID, status string) {
	s.mu.Lock()
	s.lastStatus[strategyID] = status
	s.mu.Unlock()
	// Persisted so AutoResumeAll can restore this after a process restart —
	// scheduler.Engine's runtime map (and this in-memory lastStatus map)
	// don't survive a redeploy, but the DB does.
	if err := s.Strategies.SetDesiredStatus(strategyID, status); err != nil {
		s.Logs.Insert(strategyID, "error", "failed to persist desired_status: "+err.Error())
	}
}

// statusOf prefers the live runtime's state (authoritative while it
// exists) and falls back to the last known status recorded before
// StopStrategy tore the runtime down, then "not_started" if neither.
func (s *Server) statusOf(strategyID string) string {
	if rt, ok := s.Engine.Get(strategyID); ok {
		return rt.State().String()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.lastStatus[strategyID]; ok {
		return st
	}
	return "not_started"
}

// startStrategy is handleRun's actual work, factored out so AutoResumeAll
// (called once at boot, no HTTP request in play) can reuse the exact same
// ledger/hooks/RunStrategy wiring instead of duplicating it.
func (s *Server) startStrategy(id string) error {
	strat, _, err := s.Strategies.GetLatestVersion(id)
	if err != nil {
		return fmt.Errorf("strategy not found: %w", err)
	}

	ledger := s.getLedger(id)
	hooks := strategy.Hooks{
		OnOrder: func(o models.Order) {
			if err := s.Orders.Insert(o); err != nil {
				s.Logs.Insert(id, "error", "failed to persist order: "+err.Error())
			}
		},
		OnTrade: func(t models.Trade) {
			if err := s.Trades.Upsert(t); err != nil {
				s.Logs.Insert(id, "error", "failed to persist trade: "+err.Error())
			}
		},
		OnLog: func(strategyID, level, message string) {
			s.Logs.Insert(strategyID, level, message)
		},
	}

	if _, err := s.Engine.RunStrategy(strat, ledger, s.Broker, hooks); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := s.Strategies.SetFirstRunAt(id, now); err != nil {
		s.Logs.Insert(id, "error", "failed to record first_run_at: "+err.Error())
	}
	if err := s.Strategies.SetLastRunAt(id, now); err != nil {
		s.Logs.Insert(id, "error", "failed to record last_run_at: "+err.Error())
	}
	return nil
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.startStrategy(id); err != nil {
		if err == scheduler.ErrConcurrencyLimitReached {
			writeError(w, http.StatusTooManyRequests, "concurrency_limit_reached")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.setStatus(id, "running")
	writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
}

// AutoResumeAll restores whatever was actually running/paused before the
// process last stopped — called once at boot, before the HTTP port is
// reachable (see cmd/engine/main.go). scheduler.Engine's runtime map is
// pure in-memory, so without this every redeploy would silently drop every
// strategy back to not_started until someone noticed and clicked Run.
// A strategy explicitly stopped (or never started) is left alone — only
// "running"/"paused" are restored, since auto-starting a deliberately
// stopped strategy would override a real decision, not recover from one.
func (s *Server) AutoResumeAll() {
	ids, err := s.Strategies.ListStrategyIDs()
	if err != nil {
		s.Logs.Insert("system", "error", "AutoResumeAll: failed to list strategies: "+err.Error())
		return
	}
	for _, id := range ids {
		desired, err := s.Strategies.GetDesiredStatus(id)
		if err != nil || (desired != "running" && desired != "paused") {
			continue
		}
		if err := s.startStrategy(id); err != nil {
			s.Logs.Insert(id, "error", "AutoResumeAll: failed to restart: "+err.Error())
			continue
		}
		if desired == "paused" {
			if err := s.Engine.PauseStrategy(id); err != nil {
				s.Logs.Insert(id, "error", "AutoResumeAll: failed to re-pause: "+err.Error())
				continue
			}
		}
		s.Logs.Insert(id, "info", "AutoResumeAll: restored to "+desired+" after process restart")
	}
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Engine.PauseStrategy(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.setStatus(id, "paused")
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Engine.ResumeStrategy(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.setStatus(id, "running")
	writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Engine.StopStrategy(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.setStatus(id, "stopped")
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// handleDelete permanently removes a strategy — every version, order,
// trade, log, and review row, plus its in-memory ledger/status. Stops the
// runtime first if it happens to be running, same as handleStop, so a
// live goroutine can't write to storage mid-delete. Irreversible: no
// undo, unlike pause/resume — used to clear out test/demo strategies the
// dashboard has no other way to remove.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	_ = s.Engine.StopStrategy(id) // ignore "not running" — nothing to tear down is fine

	if err := s.Orders.DeleteByStrategy(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Trades.DeleteByStrategy(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Logs.DeleteByStrategy(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Reviews.DeleteByStrategy(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Predicted.DeleteByStrategy(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Strategies.DeleteStrategy(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.mu.Lock()
	delete(s.ledgers, id)
	delete(s.lastStatus, id)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handlePortfolio(w http.ResponseWriter, r *http.Request) {
	ledger := s.getLedger(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]interface{}{"cash": ledger.Cash().String()})
}

func (s *Server) handleTrades(w http.ResponseWriter, r *http.Request) {
	strat, _, err := s.Strategies.GetLatestVersion(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "strategy not found")
		return
	}
	trades, err := s.Trades.ListByStrategy(strat.StrategyID, strat.StrategyVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, trades)
}

func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	strat, _, err := s.Strategies.GetLatestVersion(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "strategy not found")
		return
	}
	orders, err := s.Orders.ListByStrategy(strat.StrategyID, strat.StrategyVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, orders)
}

func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	strat, _, err := s.Strategies.GetLatestVersion(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "strategy not found")
		return
	}
	trades, err := s.Trades.ListByStrategy(strat.StrategyID, strat.StrategyVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	m := analytics.Compute(trades, s.DefaultStartingCapital, 0)
	writeJSON(w, http.StatusOK, m)
}

type predictedMetricsRequest struct {
	CAGR           float64 `json:"cagr"`
	StrategyReturn float64 `json:"strategy_return"`
	Sharpe         float64 `json:"sharpe"`
	Sortino        float64 `json:"sortino"`
	Drawdown       float64 `json:"drawdown"`
	WinRate        float64 `json:"win_rate"`
	ProfitFactor   float64 `json:"profit_factor"`
	TotalTrades    int     `json:"total_trades"`
	Source         string  `json:"source"`
	Description    string  `json:"description"`
	Rationale      string  `json:"rationale"`
	ConfidenceJSON string  `json:"confidence_json"`
}

// handleSavePredictedMetrics records what a backtest predicted at deploy
// time — the Strategy Lab's Deploy button calls this right after
// /strategies/{id}/run, so the paper-trading view can later show
// predicted-vs-actual without re-running the backtest.
func (s *Server) handleSavePredictedMetrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req predictedMetricsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	err = s.Predicted.Save(storage.PredictedMetrics{
		StrategyID: id, CAGR: req.CAGR, StrategyReturn: req.StrategyReturn, Sharpe: req.Sharpe, Sortino: req.Sortino,
		Drawdown: req.Drawdown, WinRate: req.WinRate, ProfitFactor: req.ProfitFactor,
		TotalTrades: req.TotalTrades, Source: req.Source,
		Description: req.Description, Rationale: req.Rationale, ConfidenceJSON: req.ConfidenceJSON,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) handleGetPredictedMetrics(w http.ResponseWriter, r *http.Request) {
	m, ok, err := s.Predicted.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no predicted metrics recorded for this strategy")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleDailyReview(w http.ResponseWriter, r *http.Request) {
	strat, _, err := s.Strategies.GetLatestVersion(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "strategy not found")
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	allTrades, err := s.Trades.ListByStrategy(strat.StrategyID, strat.StrategyVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Only trades that EXITED on this date count toward its stats — a
	// daily review reporting the strategy's entire lifetime win rate on
	// every date would defeat the whole point of "how did it do that day."
	dayTrades := make([]models.Trade, 0, len(allTrades))
	openCount := 0
	for _, t := range allTrades {
		if t.State == models.TradeActive || t.State == models.TradeOpen {
			openCount++
			continue
		}
		if !t.ExitTime.IsZero() && t.ExitTime.UTC().Format("2006-01-02") == date {
			dayTrades = append(dayTrades, t)
		}
	}
	review := analytics.GenerateDailyReview(strat.StrategyID, strat.StrategyVersion, date, dayTrades, openCount, s.DefaultStartingCapital, 0, "")
	raw, _ := json.Marshal(review)
	_ = s.Reviews.SaveDailyReview(strat.StrategyID, strat.StrategyVersion, date, string(raw))
	writeJSON(w, http.StatusOK, review)
}

func (s *Server) handleListDailyReviews(w http.ResponseWriter, r *http.Request) {
	strat, _, err := s.Strategies.GetLatestVersion(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "strategy not found")
		return
	}
	rows, err := s.Reviews.ListDailyReviews(strat.StrategyID, strat.StrategyVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		out = append(out, json.RawMessage(row.JSON))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Logs.ListByStrategy(r.PathValue("id"), 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleGetDSL returns the strategy's exact stored DSL JSON, byte-for-byte
// (the same raw document SaveVersion persisted) — for "what is this
// strategy actually doing" questions that a plain-English description
// can't fully answer.
func (s *Server) handleGetDSL(w http.ResponseWriter, r *http.Request) {
	strat, raw, err := s.Strategies.GetLatestVersion(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "strategy not found")
		return
	}
	_ = strat
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

type liveIndicatorsResponse struct {
	Running    bool                     `json:"running"` // false if the engine has no live runtime for this strategy right now (stopped, or not loaded this process)
	Indicators []strategy.LiveIndicator `json:"indicators"`
}

// handleLiveIndicators answers "is this strategy actually watching, and
// what is it seeing right now" with real current values — not a
// description, the live indicator cache's actual contents. Only
// meaningful while the engine has a live Runtime for this strategy
// (running or paused); a stopped/never-loaded strategy has nothing to
// report since there's no in-memory state to read.
func (s *Server) handleLiveIndicators(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rt, ok := s.Engine.Get(id)
	if !ok {
		writeJSON(w, http.StatusOK, liveIndicatorsResponse{Running: false, Indicators: nil})
		return
	}
	writeJSON(w, http.StatusOK, liveIndicatorsResponse{Running: true, Indicators: rt.LiveIndicators()})
}
