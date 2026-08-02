package models

type StrategyType string

const (
	StrategyIntraday StrategyType = "intraday"
	StrategySwing    StrategyType = "swing"
)

type AssetType string

const (
	AssetETF     AssetType = "ETF"
	AssetStock   AssetType = "STOCK"
	AssetIndex   AssetType = "INDEX"
	AssetFutures AssetType = "FUTURES"
	AssetOptions AssetType = "OPTIONS"
)

type Direction string

const (
	DirectionLong  Direction = "long"
	DirectionShort Direction = "short"
	DirectionBoth  Direction = "both"
)

type OrderState string

const (
	OrderPending   OrderState = "PENDING"
	OrderOpen      OrderState = "OPEN"
	OrderPartial   OrderState = "PARTIAL"
	OrderFilled    OrderState = "FILLED"
	OrderRejected  OrderState = "REJECTED"
	OrderCancelled OrderState = "CANCELLED"
	OrderExited    OrderState = "EXITED"
)

type TradeState string

const (
	TradeOpen      TradeState = "OPEN"
	TradeActive    TradeState = "ACTIVE"
	TradeClosed    TradeState = "CLOSED"
	TradeStopped   TradeState = "STOPPED"
	TradeTargetHit TradeState = "TARGET_HIT"
	TradeExpired   TradeState = "EXPIRED"
)

// ExitBlocked is an internal order-level marker (not a TradeState) set when a
// circuit limit prevents an exit fill. Trade stays ACTIVE until the fill
// actually succeeds. See ENGINE_SPEC.md Sec 3.
const ExitBlocked = "EXIT_BLOCKED"

type OrderSide string

const (
	SideBuy  OrderSide = "BUY"
	SideSell OrderSide = "SELL"
)

type Product string

const (
	ProductMIS  Product = "MIS"
	ProductCNC  Product = "CNC"
	ProductNRML Product = "NRML"
)

type OrderType string

const (
	OrderMarket    OrderType = "MARKET"
	OrderLimit     OrderType = "LIMIT"
	OrderStopLimit OrderType = "STOP_LIMIT"
)

type ExecutionMode string

const (
	ModePaper ExecutionMode = "paper"
	ModeLive  ExecutionMode = "live"
)

type Timeframe string

const (
	TF1m  Timeframe = "1m"
	TF3m  Timeframe = "3m"
	TF5m  Timeframe = "5m"
	TF10m Timeframe = "10m"
	TF15m Timeframe = "15m"
	TF30m Timeframe = "30m"
	TF1h  Timeframe = "1h"
	TF4h  Timeframe = "4h"
	TF1d  Timeframe = "1d"
	TF1w  Timeframe = "1w"
)

// TimeframeMinutes returns the timeframe's duration in minutes. 1d/1w are
// session-based, not fixed-minute, and are handled separately by the candle
// aggregator (they close on session/week boundaries, not a minute count).
var TimeframeMinutes = map[Timeframe]int{
	TF1m:  1,
	TF3m:  3,
	TF5m:  5,
	TF10m: 10,
	TF15m: 15,
	TF30m: 30,
	TF1h:  60,
	TF4h:  240,
}
