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
	"sort"

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

// timedCandle pairs a candle with the timeframe it belongs to, so a
// multi-timeframe merge can be sorted without losing which series each
// one came from.
type timedCandle struct {
	tf     models.Timeframe
	candle models.Candle
}

// timeframeSortKey ranks a timeframe for tie-breaking two candles that
// close at the exact same instant — finer-grained first, mirroring the
// live pipeline's guarantee that a shorter timeframe's tick lands before
// a coarser one closing at the same boundary. 1d/1w have no fixed-minute
// entry in models.TimeframeMinutes (they're session-based), so they sort
// last in a tie, which is never actually reachable in practice (nothing
// shares a close instant with a daily/weekly candle).
func timeframeSortKey(tf models.Timeframe) int {
	if m, ok := models.TimeframeMinutes[tf]; ok {
		return m
	}
	return 1 << 30
}

// mergeByTime flattens every timeframe's candle slice into one
// chronological stream, so a single pass can drive both the indicator
// cache (every timeframe needs updating) and the strategy runtime (which
// only acts on its own declared timeframe, exactly like the live pipeline
// dispatches every timeframe's close to every listener and lets the
// runtime itself filter — see strategy.Runtime.OnCandleClose).
func mergeByTime(candlesByTF map[models.Timeframe][]models.Candle) []timedCandle {
	total := 0
	for _, cs := range candlesByTF {
		total += len(cs)
	}
	merged := make([]timedCandle, 0, total)
	for tf, cs := range candlesByTF {
		for _, c := range cs {
			merged = append(merged, timedCandle{tf: tf, candle: c})
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		ti, tj := merged[i].candle.CloseTime, merged[j].candle.CloseTime
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return timeframeSortKey(merged[i].tf) < timeframeSortKey(merged[j].tf)
	})
	return merged
}

// Run backtests strat against candlesByTF — one candle slice per
// timeframe the strategy's condition tree references (always at least
// its own declared Timeframe; more if any rule uses a per-leaf Timeframe
// override, e.g. a 5m entry gated by a 15m trend filter). Each slice must
// already be sorted oldest first. v1 scope is single-symbol, matching the
// engine's current NIFTYBEES-only design. Fill simulation is FillBasic
// (instant fill at candle close + slippage) — the same realistic-but-
// simple model the live paper broker uses in its default mode.
func Run(strat *dsl.Strategy, candlesByTF map[models.Timeframe][]models.Candle, startingCapital decimal.Decimal, benchmarkReturnPct float64) (Result, error) {
	if len(candlesByTF[strat.Timeframe]) == 0 {
		return Result{}, fmt.Errorf("backtest: no candles provided for strategy's own timeframe %s", strat.Timeframe)
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

	// Every timeframe the strategy's own condition tree references must
	// have candles supplied — previously a rule with a non-default
	// Timeframe would subscribe successfully then silently never resolve
	// ("still warming up" forever, the leaf just never fires), because
	// this runner only ever replayed one timeframe. This turns that into
	// a loud, immediate error instead of a strategy that quietly never
	// trades.
	for _, tf := range rt.RequiredTimeframes() {
		if len(candlesByTF[tf]) == 0 {
			return Result{}, fmt.Errorf("backtest: strategy references timeframe %s via a rule but no candles were supplied for it", tf)
		}
	}

	ctx := context.Background()
	// Indicator cache updates for EVERY timeframe's candle close (a rule
	// on a non-default timeframe needs its cache entry kept current even
	// on bars the strategy's own OnCandleClose ignores); rt.OnCandleClose
	// is likewise called for every candle and filters to its own
	// Strategy.Timeframe internally (runtime.go) — this exactly mirrors
	// how the live scheduler dispatches every timeframe's close to every
	// listener and lets the runtime itself decide relevance.
	for _, tc := range mergeByTime(candlesByTF) {
		cache.OnCandleClose(symbol, string(tc.tf), tc.candle)
		if tc.tf == strat.Timeframe {
			currentPrice = tc.candle.Close
		}
		rt.OnCandleClose(ctx, symbol, tc.tf, tc.candle)
	}

	m := analytics.Compute(trades, startingCapital, benchmarkReturnPct)
	curve := analytics.EquityCurve(trades, startingCapital)

	return Result{
		Trades: trades, Metrics: m, EquityCurve: curve,
		OpenPositions: rt.OpenPositionCount(), FinalCash: ledger.Cash(), Logs: logs,
	}, nil
}
