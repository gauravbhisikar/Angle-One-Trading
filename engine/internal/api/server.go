// Package api exposes the engine over HTTP: create/validate/run/pause/
// resume/stop strategies, and read portfolio/trades/orders/analytics/
// daily-review/logs. Uses the standard library's net/http (Go 1.22+
// method+pattern ServeMux) — no external router needed.
package api

import (
	"encoding/json"
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
}

func NewServer(cfg Config) *Server {
	s := &Server{Config: cfg, ledgers: map[string]*portfolio.Ledger{}, lastStatus: map[string]string{}}
	s.mux = http.NewServeMux()
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

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
	defer s.mu.Unlock()
	s.lastStatus[strategyID] = status
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

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	strat, _, err := s.Strategies.GetLatestVersion(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "strategy not found")
		return
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

	rt, err := s.Engine.RunStrategy(strat, ledger, s.Broker, hooks)
	if err != nil {
		if err == scheduler.ErrConcurrencyLimitReached {
			writeError(w, http.StatusTooManyRequests, "concurrency_limit_reached")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = rt
	now := time.Now().UTC()
	if err := s.Strategies.SetFirstRunAt(id, now); err != nil {
		s.Logs.Insert(id, "error", "failed to record first_run_at: "+err.Error())
	}
	if err := s.Strategies.SetLastRunAt(id, now); err != nil {
		s.Logs.Insert(id, "error", "failed to record last_run_at: "+err.Error())
	}
	s.setStatus(id, "running")
	writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
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
