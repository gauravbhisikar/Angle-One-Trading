package memory

import (
	"context"
	"fmt"
	"time"
)

type DailySnapshot struct {
	DeploymentID   string
	Date           string // YYYY-MM-DD
	PortfolioValue string
	TodayReturnPct string
	TotalReturnPct string
	DrawdownPct    string
	OpenPositions  int
	MarketRegime   string
	Notes          string
}

// SaveDailySnapshot upserts one day's paper-trading state — "never
// delete" per the golden rule; this table has no retention/prune call
// anywhere in this package on purpose.
func (m *Manager) SaveDailySnapshot(ctx context.Context, d DailySnapshot) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO daily_snapshots (deployment_id, date, portfolio_value, today_return_pct, total_return_pct,
		 drawdown_pct, open_positions, market_regime, notes, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(deployment_id, date) DO UPDATE SET
		   portfolio_value=excluded.portfolio_value, today_return_pct=excluded.today_return_pct,
		   total_return_pct=excluded.total_return_pct, drawdown_pct=excluded.drawdown_pct,
		   open_positions=excluded.open_positions, market_regime=excluded.market_regime, notes=excluded.notes`,
		d.DeploymentID, d.Date, d.PortfolioValue, d.TodayReturnPct, d.TotalReturnPct,
		d.DrawdownPct, d.OpenPositions, d.MarketRegime, d.Notes, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("memory: save daily snapshot: %w", err)
	}
	if err := appendEvent(ctx, tx, EventDailySnapshotSaved, "", d); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) GetDailySnapshots(ctx context.Context, deploymentID string) ([]DailySnapshot, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT deployment_id, date, portfolio_value, today_return_pct, total_return_pct, drawdown_pct,
		 open_positions, coalesce(market_regime,''), coalesce(notes,'')
		 FROM daily_snapshots WHERE deployment_id = ? ORDER BY date`,
		deploymentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailySnapshot
	for rows.Next() {
		var d DailySnapshot
		if err := rows.Scan(&d.DeploymentID, &d.Date, &d.PortfolioValue, &d.TodayReturnPct, &d.TotalReturnPct,
			&d.DrawdownPct, &d.OpenPositions, &d.MarketRegime, &d.Notes); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
