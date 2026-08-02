// Package backtest answers "would this strategy have worked over
// historical data?" by running the DSL through the exact same runtime
// used for live/paper trading (internal/strategy) — same condition
// evaluator, same indicator cache, same holding/exit-priority/risk/cost
// rules, same order/trade lifecycle. This is deliberate: a backtest that
// reimplements strategy logic separately can silently drift from what
// actually executes live, and the whole point of a backtest is to be the
// source of truth for "does this strategy work," not an approximation of
// the real engine.
//
// The only thing that differs from live trading is the Feed: candles are
// replayed from a pre-fetched historical slice instead of a live broker
// tick stream, processed as fast as possible rather than paced to
// wall-clock time. Everything downstream of "a candle closed" is
// identical code to the live path.
package backtest

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"tradingengine/internal/analytics"
	"tradingengine/internal/dsl"
	"tradingengine/internal/execution"
	"tradingengine/internal/indicators"
	"tradingengine/internal/models"
	"tradingengine/internal/portfolio"
	"tradingengine/internal/portfolio/cost"
	"tradingengine/internal/risk"
	"tradingengine/internal/strategy"
)

type Result struct {
	Trades        []models.Trade
	Metrics       analytics.Metrics
	EquityCurve   []analytics.EquityPoint
	OpenPositions int
	FinalCash     decimal.Decimal
	Logs          []string
}

// Run backtests strat against candles (must already be sorted oldest
// first, one symbol, timeframe matching strat.Timeframe — v1 scope is
// single-symbol, matching the engine's current NIFTYBEES-only design).
// Fill simulation is FillBasic (instant fill at candle close +
// slippage) — the same realistic-but-simple model the live paper broker
// uses in its default mode.
func Run(strat *dsl.Strategy, candles []models.Candle, startingCapital decimal.Decimal, benchmarkReturnPct float64) (Result, error) {
	if len(candles) == 0 {
		return Result{}, fmt.Errorf("backtest: no candles provided")
	}
	if len(strat.Symbols) == 0 {
		return Result{}, fmt.Errorf("backtest: strategy has no symbols")
	}
	if result := dsl.Validate(strat); !result.Valid() {
		return Result{}, fmt.Errorf("backtest: strategy failed validation: %v", result.Errors)
	}
	symbol := strat.Symbols[0]

	costModel, err := cost.Get(strat.CostModel)
	if err != nil {
		return Result{}, fmt.Errorf("backtest: %w", err)
	}

	cache := indicators.NewCache()
	ledger := portfolio.NewLedger(startingCapital)
	riskState := risk.NewState(strat.Risk.MaxPositions, strat.Risk.MaxDailyLoss, startingCapital)
	guard := risk.NewPortfolioGuard(startingCapital, nil)

	// currentPrice is updated to each candle's close right before the
	// runtime evaluates it — the same "price lookup reflects the candle
	// just closed" contract main.go wires for the live paper broker.
	var currentPrice decimal.Decimal
	priceLookup := func(sym string) (decimal.Decimal, bool) {
		if sym == symbol {
			return currentPrice, true
		}
		return decimal.Zero, false
	}
	broker := execution.NewPaperBroker(execution.FillBasic, priceLookup)

	var trades []models.Trade
	var logs []string
	hooks := strategy.Hooks{
		OnTrade: func(t models.Trade) {
			for i := range trades {
				if trades[i].ID == t.ID {
					trades[i] = t // a trade mutates (open -> closed); replace, don't duplicate
					return
				}
			}
			trades = append(trades, t)
		},
		OnLog: func(strategyID, level, message string) {
			logs = append(logs, fmt.Sprintf("[%s] %s", level, message))
		},
	}

	rt := strategy.NewRuntime(strat, strategy.Deps{
		Cache: cache, Broker: broker, Ledger: ledger, Risk: riskState,
		PortfolioGuard: guard, Cost: costModel, Hooks: hooks,
	})
	if err := rt.Subscribe(); err != nil {
		return Result{}, fmt.Errorf("backtest: %w", err)
	}

	ctx := context.Background()
	for _, c := range candles {
		currentPrice = c.Close
		// Indicator cache must update before the strategy reads it for the
		// SAME candle — same ordering guarantee the live pipeline enforces
		// via Pipeline.SetIndicatorUpdater (ENGINE_SPEC Sec 0.4).
		cache.OnCandleClose(symbol, string(strat.Timeframe), c)
		rt.OnCandleClose(ctx, symbol, strat.Timeframe, c)
	}

	m := analytics.Compute(trades, startingCapital, benchmarkReturnPct)
	curve := analytics.EquityCurve(trades, startingCapital)

	return Result{
		Trades: trades, Metrics: m, EquityCurve: curve,
		OpenPositions: rt.OpenPositionCount(), FinalCash: ledger.Cash(), Logs: logs,
	}, nil
}
