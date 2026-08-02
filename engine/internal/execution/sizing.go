// Package execution places and tracks orders: position sizing, broker
// adapters (paper fill simulation + Angel One live), and order state
// management (DSL_SPEC Sec 24, ENGINE_SPEC Sec 1).
package execution

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

// ErrZeroQuantity is returned when sizing rounds down to zero shares/lots —
// the trade is rejected outright, never rounded up (ENGINE_SPEC Sec 1.1).
var ErrZeroQuantity = errors.New("execution: qty_zero_after_rounding")

// ResolveQuantity turns a DSL position_sizing block into a final integer
// quantity, applying India's no-fractional-shares and lot-size rules
// (ENGINE_SPEC Sec 1.1-1.2). stopDistancePct is the exit tree's
// stop_loss/trailing_sl percent, required for "risk_based" sizing.
func ResolveQuantity(sizing dsl.PositionSizing, capital, price decimal.Decimal, assetType models.AssetType, lotSize int, stopDistancePct float64) (int, error) {
	if price.LessThanOrEqual(decimal.Zero) {
		return 0, fmt.Errorf("execution: price must be positive, got %s", price)
	}

	isDerivative := assetType == models.AssetFutures || assetType == models.AssetOptions
	if isDerivative && lotSize <= 0 {
		return 0, fmt.Errorf("execution: lot_size required for %s", assetType)
	}

	var qty int
	switch sizing.Type {
	case "fixed_pct":
		amount := capital.Mul(decimal.NewFromFloat(sizing.Value / 100))
		qty = floorQty(amount, price, isDerivative, lotSize)
	case "fixed_amount":
		amount := decimal.NewFromFloat(sizing.Value)
		qty = floorQty(amount, price, isDerivative, lotSize)
	case "fixed_qty":
		qty = int(sizing.Value)
		if isDerivative {
			qty = (qty / lotSize) * lotSize
		}
	case "risk_based":
		if stopDistancePct <= 0 {
			return 0, fmt.Errorf("execution: risk_based sizing requires a stop_loss/trailing_sl distance")
		}
		riskAmount := capital.Mul(decimal.NewFromFloat(sizing.Value / 100))
		riskPerShare := price.Mul(decimal.NewFromFloat(stopDistancePct / 100))
		if riskPerShare.LessThanOrEqual(decimal.Zero) {
			return 0, fmt.Errorf("execution: invalid risk-per-share for risk_based sizing")
		}
		qty = floorQty(riskAmount.Div(riskPerShare).Mul(price), price, isDerivative, lotSize)
	default:
		return 0, fmt.Errorf("execution: unknown position_sizing.type %q", sizing.Type)
	}

	if qty <= 0 {
		return 0, ErrZeroQuantity
	}
	return qty, nil
}

func floorQty(amount, price decimal.Decimal, isDerivative bool, lotSize int) int {
	if isDerivative {
		lotCost := price.Mul(decimal.NewFromInt(int64(lotSize)))
		lots := amount.Div(lotCost).Floor()
		return int(lots.IntPart()) * lotSize
	}
	return int(amount.Div(price).Floor().IntPart())
}

// RoundToTick rounds a limit/stop price to the instrument's tick size
// (ENGINE_SPEC Sec 1.3) — fetched per-instrument, never a hardcoded 0.05.
func RoundToTick(price, tickSize decimal.Decimal) decimal.Decimal {
	if tickSize.LessThanOrEqual(decimal.Zero) {
		return price
	}
	units := price.Div(tickSize).Round(0)
	return units.Mul(tickSize)
}
