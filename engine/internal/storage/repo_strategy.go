package storage

import (
	"database/sql"
	"fmt"
	"time"

	"tradingengine/internal/dsl"
)

type StrategyRepo struct{ db *sql.DB }

func NewStrategyRepo(db *sql.DB) *StrategyRepo { return &StrategyRepo{db: db} }

// SaveVersion inserts a new, immutable strategy version (DSL_SPEC Sec 26:
// versions are never overwritten). It also upserts the parent strategy
// row so a first-time strategy_id gets registered.
func (r *StrategyRepo) SaveVersion(s *dsl.Strategy, raw []byte) error {
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := r.db.Exec(
		`INSERT INTO strategies (strategy_id, name, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(strategy_id) DO NOTHING`,
		s.StrategyID, s.StrategyName, now,
	); err != nil {
		return fmt.Errorf("storage: save strategy: %w", err)
	}

	if _, err := r.db.Exec(
		`INSERT INTO strategy_versions (strategy_id, version, dsl_json, enabled, created_at) VALUES (?, ?, ?, ?, ?)`,
		s.StrategyID, s.StrategyVersion, string(raw), s.Enabled, now,
	); err != nil {
		return fmt.Errorf("storage: save strategy version: %w", err)
	}
	return nil
}

func (r *StrategyRepo) GetVersion(strategyID string, version int) (*dsl.Strategy, []byte, error) {
	var raw string
	err := r.db.QueryRow(
		`SELECT dsl_json FROM strategy_versions WHERE strategy_id = ? AND version = ?`,
		strategyID, version,
	).Scan(&raw)
	if err != nil {
		return nil, nil, err
	}
	s, err := dsl.Parse([]byte(raw))
	return s, []byte(raw), err
}

func (r *StrategyRepo) GetLatestVersion(strategyID string) (*dsl.Strategy, []byte, error) {
	var raw string
	err := r.db.QueryRow(
		`SELECT dsl_json FROM strategy_versions WHERE strategy_id = ? ORDER BY version DESC LIMIT 1`,
		strategyID,
	).Scan(&raw)
	if err != nil {
		return nil, nil, err
	}
	s, err := dsl.Parse([]byte(raw))
	return s, []byte(raw), err
}

func (r *StrategyRepo) ListVersions(strategyID string) ([]int, error) {
	rows, err := r.db.Query(`SELECT version FROM strategy_versions WHERE strategy_id = ? ORDER BY version`, strategyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteStrategy removes the strategy row and every version — permanent,
// unlike SaveVersion's append-only lineage. Callers must stop the running
// strategy first (see handleDelete); this only touches storage.
func (r *StrategyRepo) DeleteStrategy(strategyID string) error {
	if _, err := r.db.Exec(`DELETE FROM strategy_versions WHERE strategy_id = ?`, strategyID); err != nil {
		return fmt.Errorf("storage: delete strategy versions: %w", err)
	}
	if _, err := r.db.Exec(`DELETE FROM strategies WHERE strategy_id = ?`, strategyID); err != nil {
		return fmt.Errorf("storage: delete strategy: %w", err)
	}
	return nil
}

func (r *StrategyRepo) ListStrategyIDs() ([]string, error) {
	rows, err := r.db.Query(`SELECT strategy_id FROM strategies ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
