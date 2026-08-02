// Package dsl implements the Strategy DSL described in docs/DSL_SPEC.md.
// The AI generates this JSON. The engine parses, validates, and executes it.
// No eval(), no dynamic code execution — every field is a fixed enum or
// typed literal.
package dsl

import (
	"encoding/json"
	"fmt"

	"tradingengine/internal/models"
)

type Strategy struct {
	Version         string              `json:"version"`
	StrategyID      string              `json:"strategy_id"`
	StrategyName    string              `json:"strategy_name"`
	StrategyVersion int                 `json:"strategy_version"`
	Type            models.StrategyType `json:"type"`
	AssetType       models.AssetType    `json:"asset_type"`
	Direction       models.Direction    `json:"direction"`
	Enabled         bool                `json:"enabled"`
	Timeframe       models.Timeframe    `json:"timeframe"`
	Symbols         []string            `json:"symbols"`
	Entry           *Condition          `json:"entry"`
	Exit            *Condition          `json:"exit"`
	ExitPriority    []string            `json:"exit_priority,omitempty"`
	Confirmation    string              `json:"confirmation,omitempty"`
	PositionSizing  PositionSizing      `json:"position_sizing"`
	Execution       Execution           `json:"execution"`
	Session         *Session            `json:"session,omitempty"`
	Cooldown        *Cooldown           `json:"cooldown,omitempty"`
	Reentry         *Reentry            `json:"reentry,omitempty"`
	Risk            Risk                `json:"risk"`
	Portfolio       *Portfolio          `json:"portfolio,omitempty"`
	Holding         Holding             `json:"holding"`
	Calendar        *Calendar           `json:"calendar,omitempty"`
	MarketRegime    []string            `json:"market_regime,omitempty"`
	CostModel       string              `json:"cost_model"`
	Benchmark       string              `json:"benchmark"`
	Review          *Review             `json:"review,omitempty"`
	Metadata        *Metadata           `json:"metadata,omitempty"`
	Tags            []string            `json:"tags,omitempty"`
}

type PositionSizing struct {
	Type  string  `json:"type"` // fixed_pct | fixed_amount | fixed_qty | risk_based
	Value float64 `json:"value"`
}

type Execution struct {
	Mode        models.ExecutionMode `json:"mode"`
	Broker      string               `json:"broker"`
	Exchange    string               `json:"exchange"`
	Product     models.Product       `json:"product"`
	OrderType   models.OrderType     `json:"order_type"`
	Entry       string               `json:"entry"` // market | limit | stop_limit
	SlippagePct float64              `json:"slippage_pct"`
}

type Session struct {
	EntryStart string `json:"entry_start"`
	EntryEnd   string `json:"entry_end"`
}

type Cooldown struct {
	Bars int `json:"bars"`
}

type Reentry struct {
	Allowed      bool `json:"allowed"`
	MaxReentries int  `json:"max_reentries"`
}

type Risk struct {
	MaxDailyLoss       float64 `json:"max_daily_loss"`
	MaxPositions       int     `json:"max_positions"`
	MaxPositionSizePct float64 `json:"max_position_size_pct,omitempty"`
}

type Portfolio struct {
	MaxSectorExposure float64 `json:"max_sector_exposure"`
	MaxSymbolExposure float64 `json:"max_symbol_exposure"`
}

type Holding struct {
	MaxDays                  int    `json:"max_days,omitempty"`
	ForceSquareOff           string `json:"force_square_off,omitempty"`
	MaxOpenTradeDurationMins int    `json:"max_open_trade_duration_minutes,omitempty"`
}

type Calendar struct {
	Holiday   string `json:"holiday"`
	ExpiryDay string `json:"expiry_day"`
}

type Review struct {
	MinCompletedTrades int `json:"min_completed_trades"`
	ReviewAfterDays    int `json:"review_after_days"`
}

type Metadata struct {
	Author      string `json:"author"`
	CreatedAt   string `json:"created_at"`
	Description string `json:"description"`
	Reason      string `json:"reason"`
	Notes       string `json:"notes"`
}

// Parse decodes and structurally validates a DSL document's JSON shape.
// It does not run the semantic Validate() rules — call Validate separately.
func Parse(raw []byte) (*Strategy, error) {
	var s Strategy
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("dsl: invalid json: %w", err)
	}
	return &s, nil
}
