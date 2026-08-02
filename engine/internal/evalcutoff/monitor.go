// Package evalcutoff auto-pauses a strategy once it has accumulated enough
// paper-trading evidence to judge before any real money is committed:
// intraday strategies after 30 days of running, swing strategies after 7
// completed (exited) trades. This is a one-way evaluation gate, not a
// risk control — internal/guardrails (the agent-side node) and the
// engine's own stop-loss/position-size limits handle capital protection;
// this package only stops a strategy from running unattended forever once
// it has produced a decision-worthy sample.
package evalcutoff

import (
	"context"
	"fmt"
	"time"

	"tradingengine/internal/models"
	"tradingengine/internal/scheduler"
	"tradingengine/internal/storage"
	"tradingengine/internal/strategy"
)

const (
	// IntradayMaxAge is how long an intraday strategy is allowed to keep
	// running (from its first /run) before this monitor pauses it.
	IntradayMaxAge = 30 * 24 * time.Hour
	// SwingMaxExitTrades is how many completed (exited) trades a swing
	// strategy is allowed to accumulate before this monitor pauses it.
	SwingMaxExitTrades = 7
)

// closedTradeStates are terminal — a trade in one of these has exited and
// counts toward SwingMaxExitTrades. OPEN/ACTIVE trades haven't exited yet.
var closedTradeStates = map[models.TradeState]bool{
	models.TradeClosed:    true,
	models.TradeStopped:   true,
	models.TradeTargetHit: true,
	models.TradeExpired:   true,
}

// Monitor polls every currently-running strategy and pauses it, once,
// when it crosses its style's evaluation cutoff. Unlike
// marketsession.Monitor it never auto-resumes — crossing the cutoff is a
// terminal evaluation checkpoint for a human (or the AI agent's
// self_review) to act on, not a transient market-hours pause.
type Monitor struct {
	engine     *scheduler.Engine
	strategies *storage.StrategyRepo
	trades     *storage.TradeRepo
	logs       *storage.LogRepo
	interval   time.Duration
	now        func() time.Time
}

func NewMonitor(engine *scheduler.Engine, strategies *storage.StrategyRepo, trades *storage.TradeRepo, logs *storage.LogRepo, interval time.Duration) *Monitor {
	return &Monitor{engine: engine, strategies: strategies, trades: trades, logs: logs, interval: interval, now: time.Now}
}

func (m *Monitor) log(strategyID, level, message string) {
	if m.logs == nil {
		return
	}
	_ = m.logs.Insert(strategyID, level, message)
}

func (m *Monitor) Run(ctx context.Context) {
	m.checkAndAct()
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAndAct()
		}
	}
}

// CheckNow runs one poll cycle immediately — exported for tests.
func (m *Monitor) CheckNow() {
	m.checkAndAct()
}

func (m *Monitor) checkAndAct() {
	for _, id := range m.engine.ActiveStrategyIDs() {
		rt, ok := m.engine.Get(id)
		if !ok || rt.State() != strategy.StateRunning {
			continue // already paused/stopped — a strategy paused by this monitor won't re-trigger since its state is no longer running
		}

		switch rt.Strategy.Type {
		case models.StrategyIntraday:
			m.checkIntraday(id, rt)
		case models.StrategySwing:
			m.checkSwing(id, rt)
		}
	}
}

func (m *Monitor) checkIntraday(id string, rt *strategy.Runtime) {
	firstRun, ok, err := m.strategies.GetFirstRunAt(id)
	if err != nil || !ok {
		return // never recorded a first-run (shouldn't happen for a running strategy) — nothing to evaluate against yet
	}
	age := m.now().Sub(firstRun)
	if age < IntradayMaxAge {
		return
	}
	if err := m.engine.PauseStrategy(id); err != nil {
		return
	}
	m.log(id, "info", fmt.Sprintf(
		"auto-paused: intraday evaluation window reached (%d days since first run, limit %d) — review paper-trading results before deploying real capital",
		int(age.Hours()/24), int(IntradayMaxAge.Hours()/24),
	))
}

func (m *Monitor) checkSwing(id string, rt *strategy.Runtime) {
	trades, err := m.trades.ListByStrategy(id, rt.Strategy.StrategyVersion)
	if err != nil {
		return
	}
	closed := 0
	for _, t := range trades {
		if closedTradeStates[t.State] {
			closed++
		}
	}
	if closed < SwingMaxExitTrades {
		return
	}
	if err := m.engine.PauseStrategy(id); err != nil {
		return
	}
	m.log(id, "info", fmt.Sprintf(
		"auto-paused: swing evaluation window reached (%d completed trades, limit %d) — review paper-trading results before deploying real capital",
		closed, SwingMaxExitTrades,
	))
}
