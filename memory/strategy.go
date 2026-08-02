package memory

import (
	"context"
	"fmt"
	"time"
)

type StrategyRecord struct {
	StrategyID       string
	Version          int
	ParentStrategyID string // empty for a brand-new strategy, not a version bump
	Name             string
	DSLJSON          string
	Objective        string
	Style            string
	Risk             string
	Status           string // backtest | paper | live | archived
	ChangeReason     string // why this version differs from its parent, empty for v1
	CreatedAt        time.Time
}

// SaveStrategy inserts a new strategy version — never updates an existing
// row (DSL_SPEC Sec 26: strategies are immutable; a change is always a
// new version, same strategy_id, version+1, with ParentStrategyID/
// ChangeReason explaining the lineage).
func (m *Manager) SaveStrategy(ctx context.Context, s StrategyRecord) error {
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO strategies (strategy_id, version, parent_strategy_id, name, dsl_json, objective, style, risk, status, change_reason, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.StrategyID, s.Version, nullIfEmpty(s.ParentStrategyID), s.Name, s.DSLJSON,
		s.Objective, s.Style, s.Risk, s.Status, s.ChangeReason, s.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("memory: save strategy: %w", err)
	}
	if err := appendEvent(ctx, tx, EventStrategyCreated, s.StrategyID, s); err != nil {
		return err
	}
	return tx.Commit()
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func scanStrategyRow(row interface {
	Scan(dest ...interface{}) error
}) (StrategyRecord, error) {
	var s StrategyRecord
	var parent, createdAt string
	err := row.Scan(&s.StrategyID, &s.Version, &parent, &s.Name, &s.DSLJSON, &s.Objective, &s.Style, &s.Risk, &s.Status, &s.ChangeReason, &createdAt)
	s.ParentStrategyID = parent
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return s, err
}

const strategyColumns = "strategy_id, version, coalesce(parent_strategy_id,''), name, dsl_json, objective, style, risk, status, coalesce(change_reason,''), created_at"

// GetStrategyHistory returns every version of one strategy, oldest first —
// the full lineage (DSL_SPEC Sec 26 versioning, made queryable).
func (m *Manager) GetStrategyHistory(ctx context.Context, strategyID string) ([]StrategyRecord, error) {
	rows, err := m.db.QueryContext(ctx,
		"SELECT "+strategyColumns+" FROM strategies WHERE strategy_id = ? ORDER BY version", strategyID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrategyRecord
	for rows.Next() {
		s, err := scanStrategyRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSuccessfulStrategies returns the most recent strategy versions whose
// latest backtest shows a positive CAGR — a heuristic definition of
// "success" (refinable later; V1 uses the same signal the Strategy Lab's
// tournament ranks by).
func (m *Manager) GetSuccessfulStrategies(ctx context.Context, limit int) ([]StrategyRecord, error) {
	return m.strategiesByLatestCAGR(ctx, limit, true)
}

// GetFailedStrategies is GetSuccessfulStrategies' mirror: latest backtest
// CAGR <= 0.
func (m *Manager) GetFailedStrategies(ctx context.Context, limit int) ([]StrategyRecord, error) {
	return m.strategiesByLatestCAGR(ctx, limit, false)
}

func (m *Manager) strategiesByLatestCAGR(ctx context.Context, limit int, positive bool) ([]StrategyRecord, error) {
	cmp := ">"
	if !positive {
		cmp = "<="
	}
	query := `
		SELECT ` + strategyColumns + ` FROM strategies s
		WHERE (s.strategy_id, s.version) IN (
			SELECT strategy_id, version FROM backtests b1
			WHERE b1.id = (SELECT MAX(id) FROM backtests b2 WHERE b2.strategy_id = b1.strategy_id)
			AND CAST(b1.cagr AS REAL) ` + cmp + ` 0
		)
		ORDER BY s.created_at DESC LIMIT ?`
	rows, err := m.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrategyRecord
	for rows.Next() {
		s, err := scanStrategyRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
