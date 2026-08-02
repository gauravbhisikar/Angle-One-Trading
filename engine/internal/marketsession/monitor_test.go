package marketsession

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/dsl"
	"tradingengine/internal/execution"
	"tradingengine/internal/marketdata"
	"tradingengine/internal/portfolio"
	"tradingengine/internal/scheduler"
	"tradingengine/internal/storage"
	"tradingengine/internal/strategy"
)

const testDSL = `{
  "version": "1.2", "strategy_id": "session-test-1", "strategy_name": "Session Test",
  "strategy_version": 1, "type": "swing", "asset_type": "ETF", "direction": "long", "enabled": true,
  "timeframe": "1d", "symbols": ["NIFTYBEES"],
  "entry": { "all": [ { "indicator": "close", "operator": ">", "value": 1 } ] },
  "exit": { "any": [ { "take_profit": 10 }, { "stop_loss": 5 } ] },
  "position_sizing": { "type": "fixed_pct", "value": 10 },
  "execution": { "mode": "paper", "broker": "angel", "exchange": "NSE", "product": "CNC",
    "order_type": "MARKET", "entry": "market", "slippage_pct": 0 },
  "risk": { "max_daily_loss": 5, "max_positions": 5 },
  "holding": { "max_days": 15 },
  "cost_model": "angel_equity", "benchmark": "NIFTYBEES"
}`

const testDSL2 = `{
  "version": "1.2", "strategy_id": "session-test-2", "strategy_name": "Session Test 2",
  "strategy_version": 1, "type": "swing", "asset_type": "ETF", "direction": "long", "enabled": true,
  "timeframe": "1d", "symbols": ["NIFTYBEES"],
  "entry": { "all": [ { "indicator": "close", "operator": ">", "value": 1 } ] },
  "exit": { "any": [ { "take_profit": 10 }, { "stop_loss": 5 } ] },
  "position_sizing": { "type": "fixed_pct", "value": 10 },
  "execution": { "mode": "paper", "broker": "angel", "exchange": "NSE", "product": "CNC",
    "order_type": "MARKET", "entry": "market", "slippage_pct": 0 },
  "risk": { "max_daily_loss": 5, "max_positions": 5 },
  "holding": { "max_days": 15 },
  "cost_model": "angel_equity", "benchmark": "NIFTYBEES"
}`

func runDSL(t *testing.T, eng *scheduler.Engine, raw string) string {
	t.Helper()
	strat, err := dsl.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res := dsl.Validate(strat); !res.Valid() {
		t.Fatalf("validate: %+v", res.Errors)
	}
	ledger := portfolio.NewLedger(decimal.NewFromInt(100000))
	broker := execution.NewPaperBroker(execution.FillBasic, func(string) (decimal.Decimal, bool) {
		return decimal.NewFromInt(250), true
	})
	if _, err := eng.RunStrategy(strat, ledger, broker, strategy.Hooks{}); err != nil {
		t.Fatalf("RunStrategy: %v", err)
	}
	return strat.StrategyID
}

func fridayIST(hour, minute int) time.Time {
	loc, _ := time.LoadLocation("Asia/Kolkata")
	return time.Date(2026, 7, 31, hour, minute, 0, 0, loc) // 2026-07-31 is a Friday
}
func saturdayIST(hour, minute int) time.Time {
	loc, _ := time.LoadLocation("Asia/Kolkata")
	return time.Date(2026, 8, 1, hour, minute, 0, 0, loc) // Saturday
}

func TestMonitorAutoPauseAndResume(t *testing.T) {
	feed := marketdata.NewMockFeed(time.Hour) // never actually ticks during this test
	eng := scheduler.NewEngine(10, feed, decimal.NewFromInt(100000), nil)

	strategyA := runDSL(t, eng, testDSL)  // will be auto-paused by the monitor
	strategyB := runDSL(t, eng, testDSL2) // will be manually paused by a "human" before close

	dbPath := filepath.Join(t.TempDir(), "trading.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	logs := storage.NewLogRepo(db)

	mon := NewMonitor(eng, logs, time.Hour) // interval irrelevant, CheckNow drives the test

	// Start mid-session (Friday 11:00 IST).
	mon.now = func() time.Time { return fridayIST(11, 0) }
	mon.CheckNow()

	rtA, _ := eng.Get(strategyA)
	if rtA.State() != strategy.StateRunning {
		t.Fatalf("expected strategy A still running mid-session, got %s", rtA.State())
	}

	// A human pauses strategy B for their own reason, still during market hours.
	if err := eng.PauseStrategy(strategyB); err != nil {
		t.Fatalf("manual pause: %v", err)
	}

	// Market closes (Friday 16:00 IST) — A (running) gets auto-paused; B
	// (already paused by a human) is left alone / not claimed by the monitor.
	mon.now = func() time.Time { return fridayIST(16, 0) }
	mon.CheckNow()

	rtA, _ = eng.Get(strategyA)
	if rtA.State() != strategy.StatePaused {
		t.Fatalf("expected strategy A auto-paused at close, got %s", rtA.State())
	}
	rtB, _ := eng.Get(strategyB)
	if rtB.State() != strategy.StatePaused {
		t.Fatalf("expected strategy B to remain paused, got %s", rtB.State())
	}
	if !mon.autoPaused[strategyA] {
		t.Fatal("expected strategy A to be tracked in autoPaused")
	}
	if mon.autoPaused[strategyB] {
		t.Fatal("expected strategy B NOT to be tracked in autoPaused — it was already paused by a human before close")
	}

	systemLogs, err := logs.ListByStrategy(SystemLogStrategyID, 50)
	if err != nil {
		t.Fatalf("ListByStrategy(SYSTEM): %v", err)
	}
	if !anyContains(systemLogs, "market_close") {
		t.Fatalf("expected a market_close SYSTEM log entry, got %+v", systemLogs)
	}

	// Market reopens. Force wasOpen back to false to simulate a fresh
	// closed->open transition (this test manipulates the clock directly
	// rather than waiting real time, so it also resets the transition
	// baseline explicitly).
	mon.mu.Lock()
	mon.wasOpen = false
	mon.mu.Unlock()
	mon.now = func() time.Time { return fridayIST(9, 30) }
	mon.CheckNow()

	rtA, _ = eng.Get(strategyA)
	if rtA.State() != strategy.StateRunning {
		t.Fatalf("expected strategy A auto-resumed at open, got %s", rtA.State())
	}
	rtB, _ = eng.Get(strategyB)
	if rtB.State() != strategy.StatePaused {
		t.Fatalf("expected strategy B (human-paused) to STAY paused across the open transition, got %s", rtB.State())
	}

	systemLogs, _ = logs.ListByStrategy(SystemLogStrategyID, 50)
	if !anyContains(systemLogs, "market_open") {
		t.Fatal("expected a market_open SYSTEM log entry")
	}

	aLogs, _ := logs.ListByStrategy(strategyA, 50)
	if len(aLogs) < 2 {
		t.Fatalf("expected at least 2 per-strategy log entries for A (auto-pause + auto-resume), got %d", len(aLogs))
	}
	bLogs, _ := logs.ListByStrategy(strategyB, 50)
	if len(bLogs) != 0 {
		t.Fatalf("expected 0 auto-pause/resume log entries for B (never auto-touched), got %d: %+v", len(bLogs), bLogs)
	}

	t.Logf("system logs: %d entries, strategy A logs: %d, strategy B logs: %d", len(systemLogs), len(aLogs), len(bLogs))
}

func TestMarketStatusWeekend(t *testing.T) {
	st := Current(saturdayIST(11, 0))
	if st.Open {
		t.Fatal("expected weekend to classify as closed")
	}
	if st.Reason != "weekend" {
		t.Fatalf("expected reason 'weekend', got %q", st.Reason)
	}
}

func anyContains(entries []storage.LogEntry, substr string) bool {
	for _, e := range entries {
		if strings.Contains(e.Message, substr) {
			return true
		}
	}
	return false
}
