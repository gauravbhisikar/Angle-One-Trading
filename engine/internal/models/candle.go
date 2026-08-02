package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// Candle uses Decimal for all prices (ENGINE_SPEC Sec 2) — never float.
// Quantity/volume stays int64 (ENGINE_SPEC Sec 1.4).
type Candle struct {
	Symbol    string
	Timeframe Timeframe
	OpenTime  time.Time
	CloseTime time.Time
	Open      decimal.Decimal
	High      decimal.Decimal
	Low       decimal.Decimal
	Close     decimal.Decimal
	Volume    int64
	Closed    bool
}

// Tick is one price update from the broker feed.
type Tick struct {
	Symbol    string
	Price     decimal.Decimal
	Volume    int64
	Timestamp time.Time
}
