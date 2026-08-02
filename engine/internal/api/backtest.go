package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/analytics"
	"tradingengine/internal/backtest"
	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

// This is the connector-to-engine handoff DSL_SPEC's workflow describes:
// an agent fetches historical candles (connectors/historical) and POSTs
// them alongside a draft DSL — no strategy needs to exist in storage
// first. The engine backtests deterministically and hands back an
// AIReview-shaped result (DSL_SPEC Sec 27) — the same schema the agent
// consumes after live/paper trading, so "learn from backtest" and "learn
// from live trading" are the same downstream shape.
type backtestCandle struct {
	Time   string  `json:"time"` // RFC3339
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume int64   `json:"volume"`
}

type backtestRequest struct {
	Strategy           json.RawMessage  `json:"strategy"`
	Candles            []backtestCandle `json:"candles"`
	StartingCapital    float64          `json:"starting_capital"`
	BenchmarkReturnPct float64          `json:"benchmark_return_pct"` // e.g. NIFTYBEES buy-and-hold over the same period — caller computes this from the same candles, engine never fetches its own benchmark data
}

type backtestResponse struct {
	Trades        []models.Trade          `json:"trades"`
	Metrics       analytics.Metrics       `json:"metrics"`
	EquityCurve   []analytics.EquityPoint `json:"equity_curve"`
	OpenPositions int                     `json:"open_positions"`
	FinalCash     string                  `json:"final_cash"`
	Logs          []string                `json:"logs"`
	AIReview      analytics.AIReview      `json:"ai_review"`
}

func (s *Server) handleBacktest(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req backtestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	strat, err := dsl.Parse(req.Strategy)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid strategy DSL: "+err.Error())
		return
	}
	if result := dsl.Validate(strat); !result.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{"errors": result.Errors, "warnings": result.Warnings})
		return
	}
	if len(req.Candles) == 0 {
		writeError(w, http.StatusBadRequest, "no candles provided")
		return
	}
	if len(strat.Symbols) == 0 {
		writeError(w, http.StatusBadRequest, "strategy has no symbols")
		return
	}

	candles := make([]models.Candle, 0, len(req.Candles))
	for i, c := range req.Candles {
		t, err := time.Parse(time.RFC3339, c.Time)
		if err != nil {
			writeError(w, http.StatusBadRequest, "candle["+itoa(i)+"].time: "+err.Error())
			return
		}
		candles = append(candles, models.Candle{
			Symbol: strat.Symbols[0], Timeframe: strat.Timeframe,
			OpenTime: t, CloseTime: t,
			Open: decimal.NewFromFloat(c.Open), High: decimal.NewFromFloat(c.High),
			Low: decimal.NewFromFloat(c.Low), Close: decimal.NewFromFloat(c.Close),
			Volume: c.Volume, Closed: true,
		})
	}

	startingCapital := s.DefaultStartingCapital
	if req.StartingCapital > 0 {
		startingCapital = decimal.NewFromFloat(req.StartingCapital)
	}

	result, err := backtest.Run(strat, candles, startingCapital, req.BenchmarkReturnPct)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	firstTime := candles[0].OpenTime
	lastTime := candles[len(candles)-1].OpenTime
	review := analytics.GenerateAIReview(strat.StrategyID, strat.StrategyVersion, "", firstTime, lastTime,
		result.Trades, result.OpenPositions, startingCapital, req.BenchmarkReturnPct)

	writeJSON(w, http.StatusOK, backtestResponse{
		Trades: result.Trades, Metrics: result.Metrics, EquityCurve: result.EquityCurve,
		OpenPositions: result.OpenPositions, FinalCash: result.FinalCash.String(), Logs: result.Logs,
		AIReview: review,
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
