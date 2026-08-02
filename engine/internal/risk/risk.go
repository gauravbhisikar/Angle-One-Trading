// Package risk enforces DSL_SPEC's per-strategy risk block (Sec 13),
// cooldown (Sec 11), and reentry (Sec 12) rules. Each strategy gets its
// own State — never shared, same isolation guarantee as its portfolio
// ledger (ENGINE_SPEC Sec 0.5).
package risk

import (
	"fmt"
	"sync"

	"github.com/shopspring/decimal"
)

type State struct {
	mu sync.Mutex

	maxPositions    int
	maxDailyLossPct float64
	startingCapital decimal.Decimal

	tradingDay       string
	dailyLoss        decimal.Decimal
	openPositions    map[string]bool
	cooldownUntilBar map[string]int
	reentryCount     map[string]int
}

func NewState(maxPositions int, maxDailyLossPct float64, startingCapital decimal.Decimal) *State {
	return &State{
		maxPositions:     maxPositions,
		maxDailyLossPct:  maxDailyLossPct,
		startingCapital:  startingCapital,
		openPositions:    map[string]bool{},
		cooldownUntilBar: map[string]int{},
		reentryCount:     map[string]int{},
	}
}

// RollDay resets daily-loss and reentry counters. Called once per new
// trading day (DSL_SPEC Sec 11-12: reentry counter resets daily for
// intraday; daily loss is always a per-day figure).
func (s *State) RollDay(day string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tradingDay == day {
		return
	}
	s.tradingDay = day
	s.dailyLoss = decimal.Zero
	s.reentryCount = map[string]int{}
}

// CanEnter checks max_positions, max_daily_loss, cooldown, and reentry cap
// before an entry signal is allowed to place an order.
func (s *State) CanEnter(symbol string, currentBar int, cooldownBars int, reentryAllowed bool, maxReentries int) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.maxDailyLossPct > 0 && !s.startingCapital.IsZero() {
		lossPct := s.dailyLoss.Div(s.startingCapital).Mul(decimal.NewFromInt(100))
		if lossPct.GreaterThanOrEqual(decimal.NewFromFloat(s.maxDailyLossPct)) {
			return false, "max_daily_loss_breached"
		}
	}

	if s.maxPositions > 0 && len(s.openPositions) >= s.maxPositions && !s.openPositions[symbol] {
		return false, "max_positions_reached"
	}

	if until, ok := s.cooldownUntilBar[symbol]; ok && currentBar < until {
		return false, "cooldown_active"
	}

	if count := s.reentryCount[symbol]; count > 0 {
		if !reentryAllowed {
			return false, "reentry_not_allowed"
		}
		if maxReentries > 0 && count >= maxReentries {
			return false, "max_reentries_reached"
		}
	}

	return true, ""
}

func (s *State) RecordEntry(symbol string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openPositions[symbol] = true
}

// RecordExit updates realized daily loss, starts the cooldown window, and
// increments the reentry counter for this symbol.
func (s *State) RecordExit(symbol string, currentBar, cooldownBars int, realizedPnL decimal.Decimal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.openPositions, symbol)
	if realizedPnL.IsNegative() {
		s.dailyLoss = s.dailyLoss.Add(realizedPnL.Abs())
	}
	s.cooldownUntilBar[symbol] = currentBar + cooldownBars
	s.reentryCount[symbol]++
}

func (s *State) OpenPositionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.openPositions)
}

func (s *State) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("risk.State{open=%d, dailyLoss=%s}", len(s.openPositions), s.dailyLoss)
}
