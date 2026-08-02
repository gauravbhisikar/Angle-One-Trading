// Package cost implements India equity brokerage/tax simulation
// (ENGINE_SPEC Sec 5) — split into a shared statutory layer (STT, exchange
// charges, SEBI fee, stamp duty, GST — identical regardless of broker) and
// a per-broker brokerage preset. Every simulated fill nets out both;
// reported PnL is always post-cost.
package cost

import (
	"fmt"

	"github.com/shopspring/decimal"

	"tradingengine/internal/models"
)

type Breakdown struct {
	Brokerage       decimal.Decimal
	STT             decimal.Decimal
	ExchangeCharges decimal.Decimal
	SEBIFee         decimal.Decimal
	StampDuty       decimal.Decimal
	GST             decimal.Decimal
	Total           decimal.Decimal
}

// Model computes the full charge stack for one fill.
type Model interface {
	// Name is the DSL's cost_model string (e.g. "angel_equity").
	Name() string
	Compute(side models.OrderSide, product models.Product, price decimal.Decimal, qty int) Breakdown
}

var registry = map[string]Model{}

func register(m Model) { registry[m.Name()] = m }

func Get(name string) (Model, error) {
	m, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("cost: unknown cost_model %q", name)
	}
	return m, nil
}

func init() {
	register(newBrokerModel("angel_equity", brokerageCapped(20, 0.0003)))
	register(newBrokerModel("zerodha_equity", brokerageCapped(20, 0.0003)))
	register(newBrokerModel("upstox_equity", brokerageCapped(20, 0.0005)))
	register(newBrokerModel("dhan_equity", brokerageFlat(20)))
}
