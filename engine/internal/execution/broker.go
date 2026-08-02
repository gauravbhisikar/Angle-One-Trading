package execution

import (
	"context"

	"github.com/shopspring/decimal"

	"tradingengine/internal/models"
)

type OrderRequest struct {
	Symbol      string
	Side        models.OrderSide
	Product     models.Product
	OrderType   models.OrderType
	Quantity    int
	LimitPrice  decimal.Decimal
	SlippagePct float64
}

type FillResult struct {
	FilledQuantity int
	AvgPrice       decimal.Decimal
	State          models.OrderState
	RejectReason   string
}

// BrokerAdapter is implemented once per broker (paper, angel, zerodha, ...).
// The engine core never branches on broker name (DSL_SPEC Sec 9).
type BrokerAdapter interface {
	PlaceOrder(ctx context.Context, req OrderRequest) (*FillResult, error)
	CancelOrder(ctx context.Context, orderID string) error
	GetPositions(ctx context.Context) ([]models.Position, error)
}
