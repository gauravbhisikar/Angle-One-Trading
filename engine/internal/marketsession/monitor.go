package marketsession

import (
	"context"
	"fmt"
	"sync"
	"time"

	"tradingengine/internal/scheduler"
	"tradingengine/internal/storage"
	"tradingengine/internal/strategy"
)

// SystemLogStrategyID is the sentinel used to log engine-wide (not
// per-strategy) events through the existing LogRepo — no new table, just
// a reserved ID a caller can query the same way as any strategy's logs
// (GET /system/logs, or storage.LogRepo.ListByStrategy("SYSTEM", n)
// directly), so AI agents have one place to find both per-strategy and
// system-wide history.
const SystemLogStrategyID = "SYSTEM"

// Monitor polls market status and pauses every currently-running
// strategy at close, resuming only the ones it paused itself once the
// market reopens — a strategy a human paused for their own reason before
// close is left alone, not silently resumed. Never touches open
// positions or calls Stop: pausing blocks new entries but the strategy
// runtime keeps managing (and exiting) whatever's already open, exactly
// like a manual pause (internal/strategy/runtime.go's StatePaused).
type Monitor struct {
	engine   *scheduler.Engine
	logs     *storage.LogRepo
	interval time.Duration
	now      func() time.Time // overridable for deterministic tests, defaults to time.Now

	mu         sync.Mutex
	wasOpen    bool
	haveStatus bool
	autoPaused map[string]bool
}

func NewMonitor(engine *scheduler.Engine, logs *storage.LogRepo, interval time.Duration) *Monitor {
	return &Monitor{engine: engine, logs: logs, interval: interval, now: time.Now, autoPaused: map[string]bool{}}
}

func (m *Monitor) log(strategyID, level, message string) {
	if m.logs == nil {
		return
	}
	_ = m.logs.Insert(strategyID, level, message)
}

// Run polls until ctx is cancelled. Checks status immediately on start
// (so a process started mid-session or mid-holiday doesn't wait a full
// interval before its first log), then every m.interval.
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

// CheckNow runs one poll cycle immediately — exported so tests (and a
// manual "force a check" API/CLI hook, if ever wanted) don't have to wait
// out a real interval tick.
func (m *Monitor) CheckNow() {
	m.checkAndAct()
}

func (m *Monitor) checkAndAct() {
	status := Current(m.now())

	m.mu.Lock()
	first := !m.haveStatus
	transitioned := first || status.Open != m.wasOpen
	m.wasOpen = status.Open
	m.haveStatus = true
	m.mu.Unlock()

	if !transitioned {
		return
	}

	if status.Open {
		m.onOpen(status)
	} else {
		m.onClose(status)
	}
}

func (m *Monitor) onOpen(status Status) {
	m.mu.Lock()
	toResume := make([]string, 0, len(m.autoPaused))
	for id := range m.autoPaused {
		toResume = append(toResume, id)
	}
	m.autoPaused = map[string]bool{}
	m.mu.Unlock()

	resumed := 0
	for _, id := range toResume {
		rt, ok := m.engine.Get(id)
		if !ok || rt.State() != strategy.StatePaused {
			continue // strategy was stopped, or a human already changed its state — leave it alone
		}
		if err := m.engine.ResumeStrategy(id); err == nil {
			resumed++
			m.log(id, "info", "auto-resumed: market opened ("+status.ISTTime+" IST)")
		}
	}
	m.log(SystemLogStrategyID, "info", fmt.Sprintf("market_open at %s IST — auto-resumed %d strategies", status.ISTTime, resumed))
}

func (m *Monitor) onClose(status Status) {
	ids := m.engine.ActiveStrategyIDs()

	paused := 0
	m.mu.Lock()
	for _, id := range ids {
		rt, ok := m.engine.Get(id)
		if !ok || rt.State() != strategy.StateRunning {
			continue // already paused (by a human or by us already) or stopped — don't touch, and don't claim credit for auto-pausing it
		}
		if err := m.engine.PauseStrategy(id); err == nil {
			m.autoPaused[id] = true
			paused++
			m.log(id, "info", "auto-paused: market closed ("+status.Reason+", "+status.ISTTime+" IST)")
		}
	}
	m.mu.Unlock()

	m.log(SystemLogStrategyID, "info", fmt.Sprintf("market_close (%s) at %s IST — auto-paused %d running strategies", status.Reason, status.ISTTime, paused))
}
