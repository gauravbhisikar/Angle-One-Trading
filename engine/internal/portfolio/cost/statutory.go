package cost

import (
	"github.com/shopspring/decimal"

	"tradingengine/internal/models"
)

// India equity statutory rates. Approximate published rates as of this
// spec's writing — review periodically, exchanges/SEBI revise these.
var (
	sttIntradaySell   = decimal.NewFromFloat(0.00025)   // 0.025%, sell side only
	sttDeliveryEach   = decimal.NewFromFloat(0.001)     // 0.1%, both buy and sell
	exchangeTxnCharge = decimal.NewFromFloat(0.0000297) // ~0.00297%
	sebiTurnoverFee   = decimal.NewFromFloat(0.000001)  // ₹10 per crore
	stampDutyBuy      = decimal.NewFromFloat(0.00015)   // 0.015%, buy side only
	gstRate           = decimal.NewFromFloat(0.18)
)

// statutory computes the broker-independent India equity charge stack for
// one fill's turnover.
func statutory(side models.OrderSide, product models.Product, turnover, brokerage decimal.Decimal) (stt, exch, sebi, stamp, gst decimal.Decimal) {
	isIntraday := product == models.ProductMIS

	if isIntraday {
		if side == models.SideSell {
			stt = turnover.Mul(sttIntradaySell)
		}
	} else {
		stt = turnover.Mul(sttDeliveryEach)
	}

	exch = turnover.Mul(exchangeTxnCharge)
	sebi = turnover.Mul(sebiTurnoverFee)

	if side == models.SideBuy {
		stamp = turnover.Mul(stampDutyBuy)
	}

	gst = brokerage.Add(exch).Mul(gstRate)
	return
}
