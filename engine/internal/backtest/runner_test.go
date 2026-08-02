package backtest_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/backtest"
	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

const testDSL = `{
  "version": "1.2", "strategy_id": "bt-test-1", "strategy_name": "Backtest Smoke",
  "strategy_version": 1, "type": "swing", "asset_type": "ETF", "direction": "long", "enabled": true,
  "timeframe": "1d", "symbols": ["NIFTYBEES"],
  "entry": { "all": [ { "indicator": "close", "operator": ">", "value": 1 } ] },
  "exit": { "any": [ { "take_profit": 8 }, { "stop_loss": 4 } ] },
  "position_sizing": { "type": "fixed_pct", "value": 20 },
  "execution": { "mode": "paper", "broker": "angel", "exchange": "NSE", "product": "CNC",
    "order_type": "MARKET", "entry": "market", "slippage_pct": 0 },
  "risk": { "max_daily_loss": 10, "max_positions": 1 },
  "holding": { "max_days": 30 },
  "cost_model": "angel_equity", "benchmark": "NIFTYBEES"
}`

// syntheticCandles builds a plausible multi-year daily series: a random
// walk with a slight upward drift, dipping and recovering, so entries and
// exits (both take_profit and stop_loss) actually fire across the run.
func syntheticCandles(days int) []models.Candle {
	out := make([]models.Candle, 0, days)
	price := 250.0
	seed := int64(7)
	nextRand := func() float64 {
		seed = (seed*1103515245 + 12345) & 0x7fffffff
		return float64(seed) / float64(0x7fffffff)
	}
	start := time.Date(2021, 8, 2, 10, 0, 0, 0, time.UTC)
	d := 0
	for len(out) < days {
		date := start.AddDate(0, 0, d)
		d++
		if date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
			continue
		}
		move := (nextRand() - 0.47) * price * 0.02 // slight upward drift
		price += move
		if price < 50 {
			price = 50
		}
		p := decimal.NewFromFloat(price).Round(2)
		out = append(out, models.Candle{
			Symbol: "NIFTYBEES", Timeframe: models.TF1d,
			OpenTime: date, CloseTime: date,
			Open: p, High: p, Low: p, Close: p, Volume: 100000, Closed: true,
		})
	}
	return out
}

func TestBacktestFiveYearRun(t *testing.T) {
	strat, err := dsl.Parse([]byte(testDSL))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	candles := syntheticCandles(5 * 250) // ~5 trading years
	result, err := backtest.Run(strat, candles, decimal.NewFromInt(100000), 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Metrics.TotalTrades == 0 {
		t.Fatal("expected at least one completed trade over 5 years of data")
	}
	if len(result.EquityCurve) < 2 {
		t.Fatal("expected a populated equity curve")
	}
	if result.Metrics.CAGR == 0 {
		t.Log("warning: CAGR is zero — check trade entry/exit time span")
	}

	t.Logf("trades=%d win_rate=%.1f%% profit_factor=%.2f cagr=%.2f%% total_return=%.2f%% max_dd=%.2f%% final_cash=%s open=%d",
		result.Metrics.TotalTrades, result.Metrics.WinRate, result.Metrics.ProfitFactor,
		result.Metrics.CAGR, result.Metrics.StrategyReturn, result.Metrics.Drawdown,
		result.FinalCash, result.OpenPositions)
}
