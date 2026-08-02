package execution

import (
	"context"

	"github.com/shopspring/decimal"

	"tradingengine/internal/models"
)

type FillMode int

const (
	// FillBasic: every order fills in full, immediately, at LTP + slippage.
	FillBasic FillMode = iota
	// FillAdvanced: caps fill size by a participation-rate liquidity model
	// against recent average volume (ENGINE_SPEC Sec 4) — never raw
	// single-candle volume.
	FillAdvanced
)

// LiquidityLookup returns the recent average volume for a symbol, used
// only in FillAdvanced mode.
type LiquidityLookup func(symbol string) (avgVolume int64, ok bool)

// CircuitCheck reports whether an order in the given direction is allowed
// at the current LTP (ENGINE_SPEC Sec 3) — false means the symbol is at
// its circuit band in that direction.
type CircuitCheck func(symbol string, price decimal.Decimal, side models.OrderSide) bool

type PaperBroker struct {
	mode          FillMode
	participation float64 // fraction of avg volume fillable per bar, Advanced mode only
	liquidity     LiquidityLookup
	circuit       CircuitCheck
	priceLookup   func(symbol string) (decimal.Decimal, bool)
	positions     map[string]*models.Position
	nextOrderID   int
}

func NewPaperBroker(mode FillMode, priceLookup func(symbol string) (decimal.Decimal, bool)) *PaperBroker {
	return &PaperBroker{
		mode:          mode,
		participation: 0.1,
		priceLookup:   priceLookup,
		positions:     map[string]*models.Position{},
	}
}

func (p *PaperBroker) SetLiquidityLookup(fn LiquidityLookup) { p.liquidity = fn }
func (p *PaperBroker) SetCircuitCheck(fn CircuitCheck)       { p.circuit = fn }

func (p *PaperBroker) PlaceOrder(ctx context.Context, req OrderRequest) (*FillResult, error) {
	ltp, ok := p.priceLookup(req.Symbol)
	if !ok {
		return &FillResult{State: models.OrderRejected, RejectReason: "no_price_available"}, nil
	}

	if p.circuit != nil && !p.circuit(req.Symbol, ltp, req.Side) {
		return &FillResult{State: models.OrderRejected, RejectReason: "circuit_limit_hit"}, nil
	}

	fillPrice := ltp
	slip := ltp.Mul(decimal.NewFromFloat(req.SlippagePct / 100))
	if req.Side == models.SideBuy {
		fillPrice = fillPrice.Add(slip)
	} else {
		fillPrice = fillPrice.Sub(slip)
	}

	fillQty := req.Quantity
	if p.mode == FillAdvanced && p.liquidity != nil {
		if avgVol, ok := p.liquidity(req.Symbol); ok {
			cap := int(float64(avgVol) * p.participation)
			if cap > 0 && fillQty > cap {
				fillQty = cap
			}
		}
	}

	state := models.OrderFilled
	if fillQty < req.Quantity {
		state = models.OrderPartial
	}
	if fillQty <= 0 {
		return &FillResult{State: models.OrderRejected, RejectReason: "no_liquidity"}, nil
	}

	return &FillResult{FilledQuantity: fillQty, AvgPrice: fillPrice, State: state}, nil
}

func (p *PaperBroker) CancelOrder(ctx context.Context, orderID string) error {
	return nil // paper orders fill synchronously in PlaceOrder; nothing stays open to cancel
}

func (p *PaperBroker) GetPositions(ctx context.Context) ([]models.Position, error) {
	out := make([]models.Position, 0, len(p.positions))
	for _, pos := range p.positions {
		out = append(out, *pos)
	}
	return out, nil
}
