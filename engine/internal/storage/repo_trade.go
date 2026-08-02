package storage

import (
	"database/sql"
	"fmt"

	"tradingengine/internal/models"
)

type TradeRepo struct{ db *sql.DB }

func NewTradeRepo(db *sql.DB) *TradeRepo { return &TradeRepo{db: db} }

func (r *TradeRepo) DeleteByStrategy(strategyID string) error {
	_, err := r.db.Exec(`DELETE FROM trades WHERE strategy_id = ?`, strategyID)
	return err
}

// Upsert inserts a trade on open and updates it in place on every
// subsequent state change (HWM update, exit) — trades mutate over their
// lifecycle, unlike immutable strategy versions.
func (r *TradeRepo) Upsert(t models.Trade) error {
	exitTime := ""
	if !t.ExitTime.IsZero() {
		exitTime = t.ExitTime.UTC().Format(rfc3339)
	}
	_, err := r.db.Exec(
		`INSERT INTO trades (id, strategy_id, strategy_version, symbol, direction, entry_order_id, exit_order_id,
		 quantity, entry_price, exit_price, high_water_mark, low_water_mark, state, close_reason,
		 entry_time, exit_time, holding_days, pnl, costs)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   exit_order_id=excluded.exit_order_id, exit_price=excluded.exit_price,
		   high_water_mark=excluded.high_water_mark, low_water_mark=excluded.low_water_mark,
		   state=excluded.state, close_reason=excluded.close_reason, exit_time=excluded.exit_time,
		   holding_days=excluded.holding_days, pnl=excluded.pnl, costs=excluded.costs`,
		t.ID, t.StrategyID, t.StrategyVersion, t.Symbol, t.Direction, t.EntryOrderID, t.ExitOrderID,
		t.Quantity, t.EntryPrice.String(), t.ExitPrice.String(), t.HighWaterMark.String(), t.LowWaterMark.String(),
		t.State, t.CloseReason, t.EntryTime.UTC().Format(rfc3339), exitTime, t.HoldingDays, t.PnL.String(), t.Costs.String(),
	)
	if err != nil {
		return fmt.Errorf("storage: upsert trade: %w", err)
	}
	return nil
}

func (r *TradeRepo) ListByStrategy(strategyID string, version int) ([]models.Trade, error) {
	rows, err := r.db.Query(
		`SELECT id, strategy_id, strategy_version, symbol, direction, entry_order_id, exit_order_id,
		 quantity, entry_price, exit_price, high_water_mark, low_water_mark, state, close_reason,
		 entry_time, exit_time, holding_days, pnl, costs
		 FROM trades WHERE strategy_id = ? AND strategy_version = ? ORDER BY entry_time`,
		strategyID, version,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrades(rows)
}

func scanTrades(rows *sql.Rows) ([]models.Trade, error) {
	var out []models.Trade
	for rows.Next() {
		var t models.Trade
		var entryPrice, exitPrice, hwm, lwm, entryTime, exitTime, pnl, costs sql.NullString
		if err := rows.Scan(&t.ID, &t.StrategyID, &t.StrategyVersion, &t.Symbol, &t.Direction, &t.EntryOrderID, &t.ExitOrderID,
			&t.Quantity, &entryPrice, &exitPrice, &hwm, &lwm, &t.State, &t.CloseReason,
			&entryTime, &exitTime, &t.HoldingDays, &pnl, &costs); err != nil {
			return nil, err
		}
		t.EntryPrice = parseDecimal(entryPrice.String)
		t.ExitPrice = parseDecimal(exitPrice.String)
		t.HighWaterMark = parseDecimal(hwm.String)
		t.LowWaterMark = parseDecimal(lwm.String)
		t.EntryTime = parseTime(entryTime.String)
		if exitTime.String != "" {
			t.ExitTime = parseTime(exitTime.String)
		}
		t.PnL = parseDecimal(pnl.String)
		t.Costs = parseDecimal(costs.String)
		out = append(out, t)
	}
	return out, rows.Err()
}
