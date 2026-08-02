package execution

import (
	"context"
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"tradingengine/internal/marketdata/angelone"
	"tradingengine/internal/models"
)

// AngelOneBroker places live orders via Angel One's SmartAPI REST
// endpoints, implementing the same BrokerAdapter interface as PaperBroker
// so the engine core never branches on broker (DSL_SPEC Sec 9).
type AngelOneBroker struct {
	client      *angelone.Client
	instruments map[string]angelone.Instrument
}

func NewAngelOneBroker(client *angelone.Client, instruments map[string]angelone.Instrument) *AngelOneBroker {
	return &AngelOneBroker{client: client, instruments: instruments}
}

func productCode(p models.Product) string {
	switch p {
	case models.ProductMIS:
		return "INTRADAY"
	case models.ProductCNC:
		return "DELIVERY"
	case models.ProductNRML:
		return "CARRYFORWARD"
	}
	return "INTRADAY"
}

func orderTypeCode(t models.OrderType) string {
	switch t {
	case models.OrderMarket:
		return "MARKET"
	case models.OrderLimit, models.OrderStopLimit:
		return "LIMIT"
	}
	return "MARKET"
}

type placeOrderResponse struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    struct {
		OrderID string `json:"orderid"`
	} `json:"data"`
}

func (b *AngelOneBroker) PlaceOrder(ctx context.Context, req OrderRequest) (*FillResult, error) {
	inst, ok := b.instruments[req.Symbol]
	if !ok {
		return &FillResult{State: models.OrderRejected, RejectReason: fmt.Sprintf("no instrument mapping for %s", req.Symbol)}, nil
	}

	body := map[string]string{
		"variety":         "NORMAL",
		"tradingsymbol":   req.Symbol,
		"symboltoken":     inst.Token,
		"transactiontype": string(req.Side),
		"exchange":        exchangeName(inst.ExchangeType),
		"ordertype":       orderTypeCode(req.OrderType),
		"producttype":     productCode(req.Product),
		"duration":        "DAY",
		"price":           req.LimitPrice.String(),
		"quantity":        fmt.Sprintf("%d", req.Quantity),
	}

	var resp placeOrderResponse
	if err := angeloneRequest(ctx, b.client, http.MethodPost, "/rest/secure/angelbroking/order/v1/placeOrder", body, &resp); err != nil {
		return nil, err
	}
	if !resp.Status {
		return &FillResult{State: models.OrderRejected, RejectReason: resp.Message}, nil
	}

	// Order accepted by the exchange; actual fill confirmation arrives via
	// the order-update feed / a follow-up status poll, not synchronously.
	return &FillResult{State: models.OrderOpen}, nil
}

func (b *AngelOneBroker) CancelOrder(ctx context.Context, orderID string) error {
	body := map[string]string{"variety": "NORMAL", "orderid": orderID}
	var resp placeOrderResponse
	return angeloneRequest(ctx, b.client, http.MethodPost, "/rest/secure/angelbroking/order/v1/cancelOrder", body, &resp)
}

type positionsResponse struct {
	Status bool `json:"status"`
	Data   []struct {
		TradingSymbol string `json:"tradingsymbol"`
		NetQty        string `json:"netqty"`
		AvgPrice      string `json:"avgnetprice"`
	} `json:"data"`
}

func (b *AngelOneBroker) GetPositions(ctx context.Context) ([]models.Position, error) {
	var resp positionsResponse
	if err := angeloneRequest(ctx, b.client, http.MethodGet, "/rest/secure/angelbroking/order/v1/getPosition", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]models.Position, 0, len(resp.Data))
	for _, d := range resp.Data {
		qty := 0
		fmt.Sscanf(d.NetQty, "%d", &qty)
		price, _ := decimal.NewFromString(d.AvgPrice)
		out = append(out, models.Position{Symbol: d.TradingSymbol, Quantity: qty, AvgPrice: price})
	}
	return out, nil
}

func exchangeName(exchangeType int) string {
	switch exchangeType {
	case 1:
		return "NSE"
	case 3:
		return "BSE"
	case 2:
		return "NFO"
	}
	return "NSE"
}

// angeloneRequest is a small helper since Client's request method is
// unexported to its own package — this mirrors the same header contract
// for the order endpoints execution needs.
func angeloneRequest(ctx context.Context, client *angelone.Client, method, path string, body, out interface{}) error {
	return client.Do(ctx, method, path, body, out)
}
