// Package dailyreview automatically captures one DailyReview snapshot per
// strategy per calendar day, so the dashboard can show real day-by-day
// performance history (what it made/lost each day it had a closed trade,
// right up until it was stopped or auto-paused) instead of only ever
// showing a single live-total-so-far figure. This existed as an on-demand,
// nothing-ever-calls-it capability (GET /strategies/{id}/daily-review)
// before this package — this is what actually populates it automatically.
package dailyreview

import (
	"context"
	"encoding/json"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/analytics"
	"tradingengine/internal/models"
	"tradingengine/internal/storage"
)

type Monitor struct {
	strategies      *storage.StrategyRepo
	trades          *storage.TradeRepo
	reviews         *storage.ReviewRepo
	startingCapital decimal.Decimal
	interval        time.Duration
	now             func() time.Time
}

func NewMonitor(strategies *storage.StrategyRepo, trades *storage.TradeRepo, reviews *storage.ReviewRepo, startingCapital decimal.Decimal, interval time.Duration) *Monitor {
	return &Monitor{strategies: strategies, trades: trades, reviews: reviews, startingCapital: startingCapital, interval: interval, now: time.Now}
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

// CheckNow runs one capture cycle immediately — exported for tests.
func (m *Monitor) CheckNow() {
	m.checkAndAct()
}

func (m *Monitor) checkAndAct() {
	ids, err := m.strategies.ListStrategyIDs()
	if err != nil {
		return
	}
	today := m.now().UTC().Format("2006-01-02")
	for _, id := range ids {
		m.captureOne(id, today)
	}
}

// captureOne saves today's snapshot for one strategy — a no-op (via the
// upsert in SaveDailyReview) if run more than once the same day, so
// polling more often than once a day is harmless, just redundant.
func (m *Monitor) captureOne(id, today string) {
	// Never run for a strategy that was never started — nothing to
	// review, and it would otherwise seed a permanent stream of
	// meaningless zero-trade daily rows for every strategy anyone has
	// ever created, run or not.
	if _, ok, err := m.strategies.GetLastRunAt(id); err != nil || !ok {
		return
	}

	strat, _, err := m.strategies.GetLatestVersion(id)
	if err != nil {
		return
	}

	allTrades, err := m.trades.ListByStrategy(strat.StrategyID, strat.StrategyVersion)
	if err != nil {
		return
	}

	dayTrades := make([]models.Trade, 0, len(allTrades))
	openCount := 0
	for _, t := range allTrades {
		if t.State == models.TradeActive || t.State == models.TradeOpen {
			openCount++
			continue
		}
		if !t.ExitTime.IsZero() && t.ExitTime.UTC().Format("2006-01-02") == today {
			dayTrades = append(dayTrades, t)
		}
	}
	if len(dayTrades) == 0 && openCount == 0 {
		return // nothing happened for this strategy today — don't write a meaningless empty row
	}

	review := analytics.GenerateDailyReview(strat.StrategyID, strat.StrategyVersion, today, dayTrades, openCount, m.startingCapital, 0, "")
	raw, err := json.Marshal(review)
	if err != nil {
		return
	}
	_ = m.reviews.SaveDailyReview(strat.StrategyID, strat.StrategyVersion, today, string(raw))
}
