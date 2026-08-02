package cost

import (
	"github.com/shopspring/decimal"

	"tradingengine/internal/models"
)

type brokerageFunc func(product models.Product, turnover decimal.Decimal) decimal.Decimal

type brokerModel struct {
	name      string
	brokerage brokerageFunc
}

func newBrokerModel(name string, fn brokerageFunc) *brokerModel {
	return &brokerModel{name: name, brokerage: fn}
}

func (b *brokerModel) Name() string { return b.name }

func (b *brokerModel) Compute(side models.OrderSide, product models.Product, price decimal.Decimal, qty int) Breakdown {
	turnover := price.Mul(decimal.NewFromInt(int64(qty)))
	brokerage := b.brokerage(product, turnover)
	stt, exch, sebi, stamp, gst := statutory(side, product, turnover, brokerage)

	total := brokerage.Add(stt).Add(exch).Add(sebi).Add(stamp).Add(gst)
	return Breakdown{
		Brokerage:       brokerage,
		STT:             stt,
		ExchangeCharges: exch,
		SEBIFee:         sebi,
		StampDuty:       stamp,
		GST:             gst,
		Total:           total,
	}
}

// brokerageCapped: intraday brokerage is min(flatCap, pct*turnover);
// delivery (CNC) is free — the common Indian discount-broker model.
func brokerageCapped(flatCap, pct float64) brokerageFunc {
	cap := decimal.NewFromFloat(flatCap)
	rate := decimal.NewFromFloat(pct)
	return func(product models.Product, turnover decimal.Decimal) decimal.Decimal {
		if product != models.ProductMIS {
			return decimal.Zero
		}
		byPct := turnover.Mul(rate)
		if byPct.LessThan(cap) {
			return byPct
		}
		return cap
	}
}

// brokerageFlat: fixed brokerage per executed order regardless of product.
func brokerageFlat(flat float64) brokerageFunc {
	amount := decimal.NewFromFloat(flat)
	return func(models.Product, decimal.Decimal) decimal.Decimal {
		return amount
	}
}
