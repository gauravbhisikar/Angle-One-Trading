package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// Order and Trade prices are Decimal, quantities are int (ENGINE_SPEC
// Sec 1.4, Sec 2) — never float, everywhere in the financial path.
type Order struct {
	ID              string
	StrategyID      string
	StrategyVersion int
	Symbol          string
	Side            OrderSide
	Product         Product
	OrderType       OrderType
	Quantity        int
	LimitPrice      decimal.Decimal
	FilledQuantity  int
	AvgFillPrice    decimal.Decimal
	State           OrderState
	RejectReason    string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Trade struct {
	ID              string
	StrategyID      string
	StrategyVersion int
	Symbol          string
	Direction       Direction
	EntryOrderID    string
	ExitOrderID     string
	Quantity        int
	EntryPrice      decimal.Decimal
	ExitPrice       decimal.Decimal
	HighWaterMark   decimal.Decimal // for trailing_sl
	LowWaterMark    decimal.Decimal
	State           TradeState
	CloseReason     string // stop_loss | take_profit | trailing_sl | signal | expired | contract_expired
	EntryTime       time.Time
	ExitTime        time.Time
	HoldingDays     int
	PnL             decimal.Decimal
	Costs           decimal.Decimal
}

type Position struct {
	Symbol   string
	Quantity int
	AvgPrice decimal.Decimal
}
