package contextbuilder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// engineClient talks to the trading engine's own HTTP API — the only way
// to reach it, since engine/internal/* isn't importable from outside the
// tradingengine module (Go's internal/ visibility) and portfolio/feature
// state is live process state anyway, not something to duplicate.
type engineClient struct {
	baseURL string
	http    *http.Client
}

func newEngineClient(baseURL string) *engineClient {
	return &engineClient{baseURL: baseURL, http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *engineClient) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("engine unreachable at %s%s: %w", c.baseURL, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("engine %s -> HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// engineStrategySummary mirrors api.StrategySummary's JSON shape
// (engine/internal/api/dashboard_data.go) — duplicated here rather than
// imported for the same internal/ visibility reason as engineClient itself.
type engineStrategySummary struct {
	StrategyID      string `json:"strategy_id"`
	StrategyName    string `json:"strategy_name"`
	StrategyVersion int    `json:"strategy_version"`
	Type            string `json:"type"`
	Status          string `json:"status"`
	Cash            string `json:"cash"`
	PnL             string `json:"pnl"`
	CompletedTrades int    `json:"completed_trades"`
}

func (c *engineClient) listStrategies(ctx context.Context) ([]engineStrategySummary, error) {
	var out []engineStrategySummary
	err := c.get(ctx, "/strategies", &out)
	return out, err
}

type engineTrade struct {
	Symbol      string
	EntryTime   string
	ExitTime    string
	PnL         string
	State       string
	CloseReason string
}

func (c *engineClient) trades(ctx context.Context, strategyID string) ([]engineTrade, error) {
	var out []engineTrade
	err := c.get(ctx, "/strategies/"+strategyID+"/trades", &out)
	return out, err
}

// engineFeatureRow mirrors featurestore.FullRow's JSON shape
// (engine/internal/featurestore/store.go) — same duplication reasoning.
type engineFeatureRow struct {
	Symbol         string
	Date           string
	Close          string
	RSI14          string
	EMA20          string
	EMA50          string
	ADX14          string
	VIX            string
	FIINet         string
	DIINet         string
	BreadthADRatio string
	NewsSentiment  string
	NewsScore      string
}

func (c *engineClient) latestFeatures(ctx context.Context, symbol string) (engineFeatureRow, bool, error) {
	to := time.Now().Format("2006-01-02")
	from := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	var rows []engineFeatureRow
	if err := c.get(ctx, "/features/"+symbol+"?from="+from+"&to="+to, &rows); err != nil {
		return engineFeatureRow{}, false, err
	}
	if len(rows) == 0 {
		return engineFeatureRow{}, false, nil
	}
	return rows[len(rows)-1], true, nil
}
