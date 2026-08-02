package analytics

import (
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/models"
)

// DailyReview is generated after market close (~15:30) — pure analytics,
// never strategy modification (DSL_SPEC Sec 27 preamble).
type DailyReview struct {
	StrategyID      string    `json:"strategy_id"`
	StrategyVersion int       `json:"strategy_version"`
	Date            string    `json:"date"`
	WinRate         float64   `json:"win_rate"`
	OpenTrades      int       `json:"open_trades"`
	PnL             string    `json:"pnl"`
	Drawdown        float64   `json:"drawdown"`
	Sharpe          float64   `json:"sharpe"`
	ProfitFactor    float64   `json:"profit_factor"`
	LargestWinner   float64   `json:"largest_winner"`
	LargestLoser    float64   `json:"largest_loser"`
	AverageHoldDays float64   `json:"average_holding_time_days"`
	MarketRegime    string    `json:"market_conditions"`
	GeneratedAt     time.Time `json:"generated_at"`
}

func GenerateDailyReview(strategyID string, version int, date string, trades []models.Trade, openCount int, startingCapital decimal.Decimal, benchmarkReturnPct float64, regime string) DailyReview {
	m := Compute(trades, startingCapital, benchmarkReturnPct)

	totalPnL := decimal.Zero
	for _, t := range trades {
		totalPnL = totalPnL.Add(t.PnL)
	}

	return DailyReview{
		StrategyID: strategyID, StrategyVersion: version, Date: date,
		WinRate: m.WinRate, OpenTrades: openCount, PnL: totalPnL.String(),
		Drawdown: m.Drawdown, Sharpe: m.Sharpe, ProfitFactor: m.ProfitFactor,
		LargestWinner: m.LargestWinner, LargestLoser: m.LargestLoser,
		AverageHoldDays: m.AverageHoldDays, MarketRegime: regime,
		GeneratedAt: time.Now().UTC(),
	}
}

// AIReview matches DSL_SPEC Sec 27 exactly — the only trade data the AI
// ever consumes, never raw order/trade rows.
type AIReview struct {
	StrategyID      string          `json:"strategy_id"`
	StrategyVersion int             `json:"strategy_version"`
	MarketType      string          `json:"market_type"`
	Period          Period          `json:"period"`
	Summary         string          `json:"summary"`
	CompletedTrades int             `json:"completed_trades"`
	OpenPositions   int             `json:"open_positions"`
	MissedEntries   int             `json:"missed_entries"`
	FalseEntries    int             `json:"false_entries"`
	Metrics         AIReviewMetrics `json:"metrics"`
	Mistakes        []string        `json:"mistakes"`
	GoodDecisions   []string        `json:"good_decisions"`
	Recommendations []string        `json:"recommendations"`
}

type Period struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type AIReviewMetrics struct {
	WinRate         float64 `json:"win_rate"`
	ProfitFactor    float64 `json:"profit_factor"`
	Sharpe          float64 `json:"sharpe"`
	Sortino         float64 `json:"sortino"`
	Drawdown        float64 `json:"drawdown"`
	AverageHoldDays float64 `json:"average_hold_days"`
	LargestWinner   float64 `json:"largest_winner"`
	LargestLoser    float64 `json:"largest_loser"`
	BenchmarkReturn float64 `json:"benchmark_return"`
	StrategyReturn  float64 `json:"strategy_return"`
	CAGR            float64 `json:"cagr"`
	TotalTrades     int     `json:"total_trades"`
}

// ReviewGate implements DSL_SPEC Sec 20: an AI Review must not be
// generated (i.e. the AI must not be given grounds to judge a strategy)
// until both thresholds are met — insufficient sample size before that.
func ReviewGate(completedTrades, minCompletedTrades int, daysRunning, reviewAfterDays int) bool {
	if minCompletedTrades > 0 && completedTrades < minCompletedTrades {
		return false
	}
	if reviewAfterDays > 0 && daysRunning < reviewAfterDays {
		return false
	}
	return true
}

func GenerateAIReview(strategyID string, version int, marketType string, from, to time.Time, trades []models.Trade, openPositions int, startingCapital decimal.Decimal, benchmarkReturnPct float64) AIReview {
	m := Compute(trades, startingCapital, benchmarkReturnPct)
	completed := 0
	for _, t := range trades {
		if t.State == models.TradeClosed || t.State == models.TradeStopped || t.State == models.TradeTargetHit {
			completed++
		}
	}

	return AIReview{
		StrategyID: strategyID, StrategyVersion: version, MarketType: marketType,
		Period:          Period{From: from.Format("2006-01-02"), To: to.Format("2006-01-02")},
		Summary:         summarize(m, completed),
		CompletedTrades: completed, OpenPositions: openPositions,
		Metrics: AIReviewMetrics{
			WinRate: m.WinRate, ProfitFactor: m.ProfitFactor, Sharpe: m.Sharpe, Sortino: m.Sortino,
			Drawdown: m.Drawdown, AverageHoldDays: m.AverageHoldDays, LargestWinner: m.LargestWinner,
			LargestLoser: m.LargestLoser, BenchmarkReturn: m.BenchmarkReturn, StrategyReturn: m.StrategyReturn,
			CAGR: m.CAGR, TotalTrades: m.TotalTrades,
		},
	}
}

func summarize(m Metrics, completed int) string {
	if completed == 0 {
		return "No completed trades in this period."
	}
	if m.StrategyReturn > m.BenchmarkReturn {
		return "Strategy outperformed its benchmark this period."
	}
	return "Strategy underperformed its benchmark this period."
}
