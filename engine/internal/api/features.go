package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/featurestore"
)

// featurestoreCandle mirrors backtestCandle (backtest.go) — same
// caller-supplies-candles contract, same "engine never fetches external
// data itself" boundary.
type featurestoreCandle struct {
	Time   string  `json:"time"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume int64   `json:"volume"`
}

type featuresComputeRequest struct {
	Symbol  string               `json:"symbol"`
	Candles []featurestoreCandle `json:"candles"`
}

// handleFeaturesCompute computes technical features (RSI/EMA/MACD/ADX/
// ATR/Bollinger %B, EMA/MACD cross flags) from caller-supplied candles,
// persists them, and returns the computed rows.
func (s *Server) handleFeaturesCompute(w http.ResponseWriter, r *http.Request) {
	if s.FeatureStore == nil {
		writeError(w, http.StatusServiceUnavailable, "feature store not configured")
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req featuresComputeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Symbol == "" || len(req.Candles) == 0 {
		writeError(w, http.StatusBadRequest, "symbol and candles are required")
		return
	}

	candles := make([]featurestore.Candle, 0, len(req.Candles))
	for i, c := range req.Candles {
		t, err := time.Parse(time.RFC3339, c.Time)
		if err != nil {
			writeError(w, http.StatusBadRequest, "candle["+itoa(i)+"].time: "+err.Error())
			return
		}
		candles = append(candles, featurestore.Candle{
			Date: t, Open: decimal.NewFromFloat(c.Open), High: decimal.NewFromFloat(c.High),
			Low: decimal.NewFromFloat(c.Low), Close: decimal.NewFromFloat(c.Close), Volume: c.Volume,
		})
	}

	rows, err := featurestore.ComputeTechnical(req.Symbol, candles)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := s.FeatureStore.SaveRows(r.Context(), rows); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleFeaturesQuery(w http.ResponseWriter, r *http.Request) {
	if s.FeatureStore == nil {
		writeError(w, http.StatusServiceUnavailable, "feature store not configured")
		return
	}
	symbol := r.PathValue("symbol")
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" {
		from = "2000-01-01"
	}
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}
	rows, err := s.FeatureStore.Query(r.Context(), symbol, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

type featuresMacroRequest struct {
	Date           string `json:"date"`
	VIX            string `json:"vix"`
	FIINet         string `json:"fii_net"`
	DIINet         string `json:"dii_net"`
	BreadthADRatio string `json:"breadth_ad_ratio"`
	NewsSentiment  string `json:"news_sentiment"`
	NewsScore      string `json:"news_score"`
}

// handleFeaturesMacro merges macro/sentiment context into a symbol's
// feature row for one day — sparse by design, see featurestore package doc.
func (s *Server) handleFeaturesMacro(w http.ResponseWriter, r *http.Request) {
	if s.FeatureStore == nil {
		writeError(w, http.StatusServiceUnavailable, "feature store not configured")
		return
	}
	symbol := r.PathValue("symbol")
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req featuresMacroRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Date == "" {
		req.Date = time.Now().Format("2006-01-02")
	}
	err = s.FeatureStore.UpsertMacro(r.Context(), featurestore.MacroSnapshot{
		Symbol: symbol, Date: req.Date, VIX: req.VIX, FIINet: req.FIINet, DIINet: req.DIINet,
		BreadthADRatio: req.BreadthADRatio, NewsSentiment: req.NewsSentiment, NewsScore: req.NewsScore,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}
