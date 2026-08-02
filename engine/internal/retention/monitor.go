// Package retention reclaims disk space from strategies that have gone
// quiet: once a strategy has been idle (no /run call) for 90 days, its raw
// trades/orders/logs are deleted. Everything an operator or the AI agent
// actually reasons from afterward is untouched — strategy_versions (so it
// can be redeployed and re-run unchanged), predicted_metrics/ai_reviews/
// daily_reviews (the analysis), and the separate memory.db the agent
// learns from all survive a purge. This trades "keep every raw fill
// forever" for "keep the conclusion forever, regenerate the raw data by
// re-running if ever needed again" — which is fine here since paper
// trading is free to re-run and never a real settlement record.
package retention

import (
	"context"
	"fmt"
	"time"

	"tradingengine/internal/scheduler"
	"tradingengine/internal/storage"
	"tradingengine/internal/strategy"
)

// PurgeAfterIdle is how long a strategy must go without a /run call
// before its raw trades/orders/logs are deleted. Anchored to last_run_at
// (updated every run), not first_run_at, so a strategy someone keeps
// re-running is never purged just because it's old.
const PurgeAfterIdle = 90 * 24 * time.Hour

// SystemLogStrategyID mirrors marketsession.SystemLogStrategyID — logs
// about a strategy's OWN purge can't be written to that strategy's own
// (about to be deleted) log rows, so they go to the shared system log.
const SystemLogStrategyID = "SYSTEM"

type Monitor struct {
	engine     *scheduler.Engine
	strategies *storage.StrategyRepo
	trades     *storage.TradeRepo
	orders     *storage.OrderRepo
	logs       *storage.LogRepo
	interval   time.Duration
	now        func() time.Time
}

func NewMonitor(engine *scheduler.Engine, strategies *storage.StrategyRepo, trades *storage.TradeRepo, orders *storage.OrderRepo, logs *storage.LogRepo, interval time.Duration) *Monitor {
	return &Monitor{engine: engine, strategies: strategies, trades: trades, orders: orders, logs: logs, interval: interval, now: time.Now}
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
	ids, err := m.strategies.ListStrategyIDs()
	if err != nil {
		return
	}
	purged := 0
	for _, id := range ids {
		if m.purgeOne(id) {
			purged++
		}
	}
	if purged > 0 {
		m.logs.Insert(SystemLogStrategyID, "info", fmt.Sprintf("retention: purged raw trades/orders/logs for %d strategies idle >%d days", purged, int(PurgeAfterIdle.Hours()/24)))
	}
}

func (m *Monitor) purgeOne(id string) bool {
	// Never touch a strategy the scheduler still has live in memory —
	// running OR paused both mean it's actively being managed (paused
	// still tracks open positions), only a strategy the engine has fully
	// forgotten (stopped, or never loaded this process) is eligible.
	if rt, ok := m.engine.Get(id); ok && rt.State() != strategy.StateStopped {
		return false
	}

	if _, already, err := m.strategies.GetPurgedAt(id); err != nil || already {
		return false // already purged, or lookup failed — don't risk a duplicate/partial purge on a read error
	}

	lastRun, ok, err := m.strategies.GetLastRunAt(id)
	if err != nil || !ok {
		return false // never run — nothing to purge yet
	}
	if m.now().Sub(lastRun) < PurgeAfterIdle {
		return false
	}

	if err := m.trades.DeleteByStrategy(id); err != nil {
		return false
	}
	if err := m.orders.DeleteByStrategy(id); err != nil {
		return false
	}
	if err := m.logs.DeleteByStrategy(id); err != nil {
		return false
	}
	_ = m.strategies.SetPurgedAt(id, m.now())
	return true
}
