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
	result, err := backtest.Run(strat, map[models.Timeframe][]models.Candle{models.TF1d: candles}, decimal.NewFromInt(100000), 0)
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

// syntheticWeeklyCandles builds a separate weekly series with its own
// price walk (deliberately not derived from syntheticCandles — the whole
// point of this test is that Run() accepts independently-supplied
// per-timeframe series, not that it aggregates one series into another).
func syntheticWeeklyCandles(weeks int) []models.Candle {
	out := make([]models.Candle, 0, weeks)
	price := 250.0
	seed := int64(11)
	nextRand := func() float64 {
		seed = (seed*1103515245 + 12345) & 0x7fffffff
		return float64(seed) / float64(0x7fffffff)
	}
	start := time.Date(2021, 8, 6, 10, 0, 0, 0, time.UTC)
	for i := 0; i < weeks; i++ {
		date := start.AddDate(0, 0, i*7)
		move := (nextRand() - 0.45) * price * 0.03
		price += move
		if price < 50 {
			price = 50
		}
		p := decimal.NewFromFloat(price).Round(2)
		out = append(out, models.Candle{
			Symbol: "NIFTYBEES", Timeframe: models.TF1w,
			OpenTime: date, CloseTime: date,
			Open: p, High: p, Low: p, Close: p, Volume: 500000, Closed: true,
		})
	}
	return out
}

// multiTFDSL's entry ANDs a daily leaf with a leaf whose Timeframe
// override is "1w" — before Phase 1, this silently never fired (the
// resolver returned "still warming up" forever); the leaf just never
// contributed a trade, with no error anywhere.
const multiTFDSL = `{
  "version": "1.2", "strategy_id": "bt-test-mtf", "strategy_name": "Multi-TF Smoke",
  "strategy_version": 1, "type": "swing", "asset_type": "ETF", "direction": "long", "enabled": true,
  "timeframe": "1d", "symbols": ["NIFTYBEES"],
  "entry": { "all": [
    { "indicator": "close", "operator": ">", "value": 1 },
    { "indicator": "close", "operator": ">", "value": 1, "timeframe": "1w" }
  ] },
  "exit": { "any": [ { "take_profit": 8 }, { "stop_loss": 4 } ] },
  "position_sizing": { "type": "fixed_pct", "value": 20 },
  "execution": { "mode": "paper", "broker": "angel", "exchange": "NSE", "product": "CNC",
    "order_type": "MARKET", "entry": "market", "slippage_pct": 0 },
  "risk": { "max_daily_loss": 10, "max_positions": 1 },
  "holding": { "max_days": 30 },
  "cost_model": "angel_equity", "benchmark": "NIFTYBEES"
}`

func TestBacktestMultiTimeframeRuleProducesTrades(t *testing.T) {
	strat, err := dsl.Parse([]byte(multiTFDSL))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	daily := syntheticCandles(5 * 250)
	weekly := syntheticWeeklyCandles(5*52 + 4)

	result, err := backtest.Run(strat, map[models.Timeframe][]models.Candle{
		models.TF1d: daily,
		models.TF1w: weekly,
	}, decimal.NewFromInt(100000), 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Metrics.TotalTrades == 0 {
		t.Fatal("expected at least one completed trade with the 1w-timeframe rule leaf supplied — this is the exact bug class Phase 1 closes")
	}
}

func TestBacktestMissingTimeframeCandlesErrors(t *testing.T) {
	strat, err := dsl.Parse([]byte(multiTFDSL))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	daily := syntheticCandles(5 * 250)
	// Deliberately omit the 1w candles the entry rule references — must
	// fail loudly now instead of silently backtesting as if that leaf
	// never exists.
	_, err = backtest.Run(strat, map[models.Timeframe][]models.Candle{models.TF1d: daily}, decimal.NewFromInt(100000), 0)
	if err == nil {
		t.Fatal("expected an error when a referenced timeframe has no candles supplied")
	}
}
