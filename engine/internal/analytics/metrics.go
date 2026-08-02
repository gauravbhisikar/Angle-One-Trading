// Package analytics generates the Daily Review and AI Review outputs
// (DSL_SPEC Sec 27). It only reads trade history and computes statistics —
// it never modifies a strategy's DSL (DSL_SPEC: "the engine should never
// modify strategy").
package analytics

import (
	"math"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/models"
)

type Metrics struct {
	WinRate         float64
	ProfitFactor    float64
	Sharpe          float64
	Sortino         float64
	Drawdown        float64
	AverageHoldDays float64
	LargestWinner   float64
	LargestLoser    float64
	BenchmarkReturn float64
	StrategyReturn  float64 // total return over the period, not annualized
	CAGR            float64 // annualized — comparable across backtests of different lengths
	TotalTrades     int
}

// Compute derives Sec 27's metrics block from a completed-trade set.
// startingCapital anchors percent returns and the equity-curve drawdown.
func Compute(trades []models.Trade, startingCapital decimal.Decimal, benchmarkReturnPct float64) Metrics {
	var m Metrics
	if len(trades) == 0 || startingCapital.IsZero() {
		return m
	}

	wins, total := 0, 0
	var grossProfit, grossLoss float64
	var largestWin, largestLoss float64
	var holdSum float64
	returns := make([]float64, 0, len(trades))
	equity := startingCapital.InexactFloat64()
	peak := equity
	maxDD := 0.0
	var firstEntry, lastExit time.Time

	capitalF := startingCapital.InexactFloat64()

	for _, t := range trades {
		if t.State != models.TradeClosed && t.State != models.TradeStopped && t.State != models.TradeTargetHit {
			continue // only completed trades count toward these stats
		}
		total++
		if firstEntry.IsZero() || t.EntryTime.Before(firstEntry) {
			firstEntry = t.EntryTime
		}
		if t.ExitTime.After(lastExit) {
			lastExit = t.ExitTime
		}
		pnl := t.PnL.InexactFloat64()
		if pnl > 0 {
			wins++
			grossProfit += pnl
			if pnl > largestWin {
				largestWin = pnl
			}
		} else {
			grossLoss += -pnl
			if pnl < largestLoss {
				largestLoss = pnl
			}
		}
		holdSum += float64(t.HoldingDays)
		if capitalF != 0 {
			returns = append(returns, pnl/capitalF)
		}

		equity += pnl
		if equity > peak {
			peak = equity
		}
		if peak > 0 {
			dd := (peak - equity) / peak * 100
			if dd > maxDD {
				maxDD = dd
			}
		}
	}

	if total == 0 {
		return m
	}

	m.WinRate = float64(wins) / float64(total) * 100
	if grossLoss > 0 {
		m.ProfitFactor = grossProfit / grossLoss
	} else if grossProfit > 0 {
		m.ProfitFactor = grossProfit // no losses at all
	}
	m.AverageHoldDays = holdSum / float64(total)
	m.LargestWinner = largestWin
	m.LargestLoser = largestLoss
	m.Drawdown = maxDD
	m.BenchmarkReturn = benchmarkReturnPct
	m.StrategyReturn = (equity - startingCapital.InexactFloat64()) / startingCapital.InexactFloat64() * 100
	m.TotalTrades = total

	years := lastExit.Sub(firstEntry).Hours() / 24 / 365.25
	if years > 0 && capitalF > 0 && equity > 0 {
		m.CAGR = (math.Pow(equity/capitalF, 1/years) - 1) * 100
	}

	m.Sharpe = sharpeRatio(returns)
	m.Sortino = sortinoRatio(returns)
	return m
}

type EquityPoint struct {
	Time   string  `json:"time"`
	Equity float64 `json:"equity"`
	PnL    float64 `json:"pnl"`
}

// EquityCurve walks completed trades in exit order, accumulating PnL onto
// startingCapital — the series the dashboard charts per strategy.
func EquityCurve(trades []models.Trade, startingCapital decimal.Decimal) []EquityPoint {
	equity := startingCapital.InexactFloat64()
	points := make([]EquityPoint, 0, len(trades)+1)
	points = append(points, EquityPoint{Time: "start", Equity: equity})

	for _, t := range trades {
		if t.State != models.TradeClosed && t.State != models.TradeStopped && t.State != models.TradeTargetHit {
			continue
		}
		pnl := t.PnL.InexactFloat64()
		equity += pnl
		ts := t.ExitTime
		label := ts.Format("2006-01-02 15:04")
		if ts.IsZero() {
			label = t.EntryTime.Format("2006-01-02 15:04")
		}
		points = append(points, EquityPoint{Time: label, Equity: equity, PnL: pnl})
	}
	return points
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func stddev(xs []float64, m float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var sumSq float64
	for _, x := range xs {
		d := x - m
		sumSq += d * d
	}
	return math.Sqrt(sumSq / float64(len(xs)-1))
}

// sharpeRatio/sortinoRatio are per-trade approximations, annualized against
// 252 trading periods — a simplification appropriate for strategy-level
// comparison, not a precise time-series Sharpe.
func sharpeRatio(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	mu := mean(returns)
	sd := stddev(returns, mu)
	if sd == 0 {
		return 0
	}
	return mu / sd * math.Sqrt(252)
}

func sortinoRatio(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	mu := mean(returns)
	var downside []float64
	for _, r := range returns {
		if r < 0 {
			downside = append(downside, r)
		}
	}
	if len(downside) == 0 {
		return 0
	}
	dd := stddev(downside, 0)
	if dd == 0 {
		return 0
	}
	return mu / dd * math.Sqrt(252)
}
