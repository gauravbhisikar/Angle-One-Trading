// Package scheduler wires the shared market-data/indicator pipeline to up
// to MaxConcurrentStrategies isolated strategy runtimes (ENGINE_SPEC
// Sec 0): one process, one event loop, one broker feed connection.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shopspring/decimal"

	"tradingengine/internal/dsl"
	"tradingengine/internal/execution"
	"tradingengine/internal/indicators"
	"tradingengine/internal/marketdata"
	"tradingengine/internal/models"
	"tradingengine/internal/portfolio"
	"tradingengine/internal/portfolio/cost"
	"tradingengine/internal/risk"
	"tradingengine/internal/strategy"
)

var ErrConcurrencyLimitReached = errors.New("scheduler: concurrency_limit_reached")

type Engine struct {
	maxConcurrent  int
	Pipeline       *marketdata.Pipeline
	Cache          *indicators.Cache
	PortfolioGuard *risk.PortfolioGuard

	mu          sync.Mutex
	runtimes    map[string]*strategy.Runtime
	listenerIDs map[string]int
}

func NewEngine(maxConcurrent int, feed marketdata.Feed, totalCapital decimal.Decimal, sectorLookup risk.SectorLookup) *Engine {
	cache := indicators.NewCache()
	pipeline := marketdata.NewPipeline(feed)
	// Single shared registration for the whole engine, guaranteed to run
	// before any strategy reads the cache for the same candle (ENGINE_SPEC
	// Sec 0.4) — see Pipeline.SetIndicatorUpdater.
	pipeline.SetIndicatorUpdater(func(symbol string, tf models.Timeframe, candle models.Candle) {
		cache.OnCandleClose(symbol, string(tf), candle)
	})

	return &Engine{
		maxConcurrent:  maxConcurrent,
		Pipeline:       pipeline,
		Cache:          cache,
		PortfolioGuard: risk.NewPortfolioGuard(totalCapital, sectorLookup),
		runtimes:       map[string]*strategy.Runtime{},
		listenerIDs:    map[string]int{},
	}
}

func (e *Engine) ActiveCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.runtimes)
}

func (e *Engine) Get(strategyID string) (*strategy.Runtime, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rt, ok := e.runtimes[strategyID]
	return rt, ok
}

// ActiveStrategyIDs lists every strategy this engine currently tracks
// (running or paused — anything not stopped, since StopStrategy removes
// it from this map entirely). Used by marketsession.Monitor to know which
// strategies to auto-pause at market close.
func (e *Engine) ActiveStrategyIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := make([]string, 0, len(e.runtimes))
	for id := range e.runtimes {
		ids = append(ids, id)
	}
	return ids
}

// RunStrategy activates one DSL strategy. Rejected outright at the cap —
// never queued to run degraded (ENGINE_SPEC Sec 0.6).
func (e *Engine) RunStrategy(s *dsl.Strategy, ledger *portfolio.Ledger, broker execution.BrokerAdapter, hooks strategy.Hooks) (*strategy.Runtime, error) {
	e.mu.Lock()
	if _, exists := e.runtimes[s.StrategyID]; exists {
		e.mu.Unlock()
		return nil, fmt.Errorf("scheduler: strategy %s is already running", s.StrategyID)
	}
	if len(e.runtimes) >= e.maxConcurrent {
		e.mu.Unlock()
		return nil, ErrConcurrencyLimitReached
	}
	e.mu.Unlock()

	costModel, err := cost.Get(s.CostModel)
	if err != nil {
		return nil, err
	}

	riskState := risk.NewState(s.Risk.MaxPositions, s.Risk.MaxDailyLoss, ledger.Cash())
	rt := strategy.NewRuntime(s, strategy.Deps{
		Cache:          e.Cache,
		Broker:         broker,
		Ledger:         ledger,
		Risk:           riskState,
		PortfolioGuard: e.PortfolioGuard,
		Cost:           costModel,
		Hooks:          hooks,
	})

	for _, tf := range rt.RequiredTimeframes() {
		e.Pipeline.EnsureTimeframe(tf)
	}
	if err := rt.Subscribe(); err != nil {
		return nil, err
	}
	if err := e.Pipeline.Subscribe(s.Symbols); err != nil {
		rt.Unsubscribe()
		return nil, err
	}

	listenerID := e.Pipeline.OnCandleClose(func(symbol string, tf models.Timeframe, candle models.Candle) {
		rt.OnCandleClose(context.Background(), symbol, tf, candle)
	})

	e.mu.Lock()
	e.runtimes[s.StrategyID] = rt
	e.listenerIDs[s.StrategyID] = listenerID
	e.mu.Unlock()
	return rt, nil
}

func (e *Engine) PauseStrategy(strategyID string) error {
	rt, ok := e.Get(strategyID)
	if !ok {
		return fmt.Errorf("scheduler: strategy %s not running", strategyID)
	}
	rt.SetState(strategy.StatePaused)
	return nil
}

func (e *Engine) ResumeStrategy(strategyID string) error {
	rt, ok := e.Get(strategyID)
	if !ok {
		return fmt.Errorf("scheduler: strategy %s not running", strategyID)
	}
	rt.SetState(strategy.StateRunning)
	return nil
}

// StopStrategy halts the strategy and releases its indicator
// subscriptions, freeing a concurrency slot for another strategy.
func (e *Engine) StopStrategy(strategyID string) error {
	e.mu.Lock()
	rt, ok := e.runtimes[strategyID]
	listenerID, hasListener := e.listenerIDs[strategyID]
	if ok {
		delete(e.runtimes, strategyID)
		delete(e.listenerIDs, strategyID)
	}
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("scheduler: strategy %s not running", strategyID)
	}
	rt.SetState(strategy.StateStopped)
	rt.Unsubscribe()
	if hasListener {
		e.Pipeline.RemoveListener(listenerID)
	}
	return nil
}

func (e *Engine) Run(ctx context.Context) {
	e.Pipeline.Run(ctx)
}
