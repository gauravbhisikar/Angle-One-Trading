// Package portfolio holds each strategy's isolated paper-trading ledger —
// cash and holdings never shared across strategies or versions
// (ENGINE_SPEC Sec 0.5, DSL_SPEC Sec 26).
package portfolio

import (
	"fmt"
	"sync"

	"github.com/shopspring/decimal"

	"tradingengine/internal/models"
)

type Ledger struct {
	mu sync.Mutex

	startingCash decimal.Decimal
	cash         decimal.Decimal
	holdings     map[string]*models.Position
	realizedPnL  decimal.Decimal
	totalCosts   decimal.Decimal
}

func NewLedger(startingCash decimal.Decimal) *Ledger {
	return &Ledger{
		startingCash: startingCash,
		cash:         startingCash,
		holdings:     map[string]*models.Position{},
	}
}

func (l *Ledger) Cash() decimal.Decimal {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cash
}

func (l *Ledger) Position(symbol string) (models.Position, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.holdings[symbol]
	if !ok {
		return models.Position{}, false
	}
	return *p, true
}

func (l *Ledger) deployedCapital() decimal.Decimal {
	total := decimal.Zero
	for _, p := range l.holdings {
		total = total.Add(p.AvgPrice.Mul(decimal.NewFromInt(int64(p.Quantity))))
	}
	return total
}

// ApplyBuy debits cash by turnover + costs and adds to the position,
// weighted-averaging entry price if adding to an existing holding.
func (l *Ledger) ApplyBuy(symbol string, qty int, price, costs decimal.Decimal) error {
	if qty <= 0 {
		return fmt.Errorf("portfolio: buy quantity must be a positive integer, got %d", qty)
	}
	turnover := price.Mul(decimal.NewFromInt(int64(qty)))

	l.mu.Lock()
	defer l.mu.Unlock()

	need := turnover.Add(costs)
	if l.cash.LessThan(need) {
		return fmt.Errorf("portfolio: insufficient cash for %s: need %s, have %s", symbol, need, l.cash)
	}
	l.cash = l.cash.Sub(need)
	l.totalCosts = l.totalCosts.Add(costs)

	pos, ok := l.holdings[symbol]
	if !ok {
		l.holdings[symbol] = &models.Position{Symbol: symbol, Quantity: qty, AvgPrice: price}
		return nil
	}
	totalQty := pos.Quantity + qty
	weighted := pos.AvgPrice.Mul(decimal.NewFromInt(int64(pos.Quantity))).Add(turnover)
	pos.AvgPrice = weighted.Div(decimal.NewFromInt(int64(totalQty)))
	pos.Quantity = totalQty
	return nil
}

// ApplySell credits cash, realizes PnL (gross, before costs — costs are
// tracked separately so Reconcile's invariant holds), and reduces/closes
// the position. No fractional/short selling in v1 (DSL_SPEC Sec 1.1, 8).
func (l *Ledger) ApplySell(symbol string, qty int, price, costs decimal.Decimal) (decimal.Decimal, error) {
	if qty <= 0 {
		return decimal.Zero, fmt.Errorf("portfolio: sell quantity must be a positive integer, got %d", qty)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	pos, ok := l.holdings[symbol]
	if !ok || pos.Quantity < qty {
		held := 0
		if ok {
			held = pos.Quantity
		}
		return decimal.Zero, fmt.Errorf("portfolio: cannot sell %d of %s, only %d held", qty, symbol, held)
	}

	turnover := price.Mul(decimal.NewFromInt(int64(qty)))
	realized := price.Sub(pos.AvgPrice).Mul(decimal.NewFromInt(int64(qty)))

	l.cash = l.cash.Add(turnover).Sub(costs)
	l.totalCosts = l.totalCosts.Add(costs)
	l.realizedPnL = l.realizedPnL.Add(realized)

	pos.Quantity -= qty
	if pos.Quantity == 0 {
		delete(l.holdings, symbol)
	}
	return realized, nil
}

// Reconcile verifies cash + deployedCapital - realizedPnL + totalCosts ==
// startingCash (ENGINE_SPEC Sec 12). A mismatch is an engine bug signal,
// never a market condition — callers should log it and halt new orders
// for this strategy version, not silently continue.
func (l *Ledger) Reconcile() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	lhs := l.cash.Add(l.deployedCapital()).Sub(l.realizedPnL).Add(l.totalCosts)
	diff := lhs.Sub(l.startingCash).Abs()
	if diff.GreaterThan(decimal.NewFromFloat(0.01)) {
		return fmt.Errorf("portfolio: reconciliation_error: cash+deployed-realizedPnL+costs = %s, expected startingCash = %s (diff %s)", lhs, l.startingCash, diff)
	}
	return nil
}
