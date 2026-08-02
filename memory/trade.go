package memory

import (
	"context"
	"fmt"
	"time"
)

type TradeRecord struct {
	ID           string
	DeploymentID string
	StrategyID   string
	Symbol       string
	EntryTime    time.Time
	ExitTime     *time.Time
	EntryPrice   string
	ExitPrice    string
	Quantity     int
	PnL          string
	HoldingDays  int
	ExitReason   string
}

// SaveTrade upserts by ID — a trade mutates (opens, then closes), same
// as the engine's own trade record. Emits TradeOpened on first insert and
// TradeClosed once ExitTime is set — callers naturally get the right
// event by calling SaveTrade again after a trade closes.
func (m *Manager) SaveTrade(ctx context.Context, t TradeRecord) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var existed bool
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM trades WHERE id = ?`, t.ID).Scan(new(int)); err == nil {
		existed = true
	}

	var exitTime interface{}
	if t.ExitTime != nil {
		exitTime = t.ExitTime.Format(time.RFC3339)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO trades (id, deployment_id, strategy_id, symbol, entry_time, exit_time, entry_price, exit_price,
		 quantity, pnl, holding_days, exit_reason, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   exit_time=excluded.exit_time, exit_price=excluded.exit_price, pnl=excluded.pnl,
		   holding_days=excluded.holding_days, exit_reason=excluded.exit_reason`,
		t.ID, t.DeploymentID, t.StrategyID, t.Symbol, t.EntryTime.Format(time.RFC3339), exitTime,
		t.EntryPrice, t.ExitPrice, t.Quantity, t.PnL, t.HoldingDays, t.ExitReason, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("memory: save trade: %w", err)
	}

	eventType := EventTradeOpened
	if existed && t.ExitTime != nil {
		eventType = EventTradeClosed
	}
	if err := appendEvent(ctx, tx, eventType, t.StrategyID, t); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) GetTradesForStrategy(ctx context.Context, strategyID string) ([]TradeRecord, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, deployment_id, strategy_id, symbol, entry_time, coalesce(exit_time,''), entry_price,
		 coalesce(exit_price,''), quantity, coalesce(pnl,''), holding_days, coalesce(exit_reason,'')
		 FROM trades WHERE strategy_id = ? ORDER BY entry_time`,
		strategyID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TradeRecord
	for rows.Next() {
		var t TradeRecord
		var entryTime, exitTime string
		if err := rows.Scan(&t.ID, &t.DeploymentID, &t.StrategyID, &t.Symbol, &entryTime, &exitTime,
			&t.EntryPrice, &t.ExitPrice, &t.Quantity, &t.PnL, &t.HoldingDays, &t.ExitReason); err != nil {
			return nil, err
		}
		t.EntryTime, _ = time.Parse(time.RFC3339, entryTime)
		if exitTime != "" {
			et, _ := time.Parse(time.RFC3339, exitTime)
			t.ExitTime = &et
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
