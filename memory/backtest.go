package memory

import (
	"context"
	"fmt"
	"time"
)

type BacktestRecord struct {
	ID              int64
	StrategyID      string
	Version         int
	PeriodFrom      string
	PeriodTo        string
	TotalReturn     string
	CAGR            string
	Drawdown        string
	Sharpe          string
	Sortino         string
	ProfitFactor    string
	WinRate         string
	TotalTrades     int
	EquityCurveJSON string
	CreatedAt       time.Time
}

// SaveBacktest records one backtest run — a strategy version can be
// backtested more than once (different date ranges), so this is
// append-only, not upserted like SaveContext.
func (m *Manager) SaveBacktest(ctx context.Context, b BacktestRecord) error {
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO backtests (strategy_id, version, period_from, period_to, total_return, cagr, drawdown,
		 sharpe, sortino, profit_factor, win_rate, total_trades, equity_curve_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.StrategyID, b.Version, b.PeriodFrom, b.PeriodTo, b.TotalReturn, b.CAGR, b.Drawdown,
		b.Sharpe, b.Sortino, b.ProfitFactor, b.WinRate, b.TotalTrades, b.EquityCurveJSON, b.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("memory: save backtest: %w", err)
	}
	id, _ := res.LastInsertId()
	b.ID = id
	if err := appendEvent(ctx, tx, EventStrategyBacktested, b.StrategyID, b); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) GetBacktestsForStrategy(ctx context.Context, strategyID string) ([]BacktestRecord, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, strategy_id, version, coalesce(period_from,''), coalesce(period_to,''), total_return, cagr, drawdown,
		 sharpe, sortino, profit_factor, win_rate, total_trades, coalesce(equity_curve_json,''), created_at
		 FROM backtests WHERE strategy_id = ? ORDER BY created_at`,
		strategyID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BacktestRecord
	for rows.Next() {
		var b BacktestRecord
		var createdAt string
		if err := rows.Scan(&b.ID, &b.StrategyID, &b.Version, &b.PeriodFrom, &b.PeriodTo, &b.TotalReturn, &b.CAGR,
			&b.Drawdown, &b.Sharpe, &b.Sortino, &b.ProfitFactor, &b.WinRate, &b.TotalTrades, &b.EquityCurveJSON, &createdAt); err != nil {
			return nil, err
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		out = append(out, b)
	}
	return out, rows.Err()
}
