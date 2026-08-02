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

// SetFirstRunAt records when a strategy was FIRST started, once — a
// second/third/Nth /run call (resume after pause, redeploy, etc.) never
// overwrites it. This is what evalcutoff.Monitor anchors the intraday
// 30-day evaluation window to; it has to survive engine restarts (unlike
// the in-memory scheduler runtime), which is why it's a DB column, not
// just tracked in the running process.
func (r *StrategyRepo) SetFirstRunAt(strategyID string, t time.Time) error {
	_, err := r.db.Exec(
		`UPDATE strategies SET first_run_at = ? WHERE strategy_id = ? AND (first_run_at IS NULL OR first_run_at = '')`,
		t.UTC().Format(time.RFC3339), strategyID,
	)
	return err
}

func (r *StrategyRepo) GetFirstRunAt(strategyID string) (time.Time, bool, error) {
	var raw sql.NullString
	err := r.db.QueryRow(`SELECT first_run_at FROM strategies WHERE strategy_id = ?`, strategyID).Scan(&raw)
	if err != nil {
		return time.Time{}, false, err
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, raw.String)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

// SetLastRunAt records when a strategy was MOST RECENTLY started — unlike
// SetFirstRunAt this overwrites on every /run call. internal/retention's
// Monitor anchors its 90-day purge window to this (idle-since, not
// started-since), so a strategy someone keeps re-running never gets
// purged just because it's old.
func (r *StrategyRepo) SetLastRunAt(strategyID string, t time.Time) error {
	_, err := r.db.Exec(
		`UPDATE strategies SET last_run_at = ? WHERE strategy_id = ?`,
		t.UTC().Format(time.RFC3339), strategyID,
	)
	return err
}

func (r *StrategyRepo) GetLastRunAt(strategyID string) (time.Time, bool, error) {
	var raw sql.NullString
	err := r.db.QueryRow(`SELECT last_run_at FROM strategies WHERE strategy_id = ?`, strategyID).Scan(&raw)
	if err != nil {
		return time.Time{}, false, err
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, raw.String)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

// SetPurgedAt marks that retention.Monitor already deleted this strategy's
// trades/orders/logs — makes the purge idempotent (checked before
// re-running the delete queries every poll) and lets the dashboard show
// "data purged" instead of a bare zero-trades count that would otherwise
// be indistinguishable from "never traded."
func (r *StrategyRepo) SetPurgedAt(strategyID string, t time.Time) error {
	_, err := r.db.Exec(
		`UPDATE strategies SET purged_at = ? WHERE strategy_id = ?`,
		t.UTC().Format(time.RFC3339), strategyID,
	)
	return err
}

func (r *StrategyRepo) GetPurgedAt(strategyID string) (time.Time, bool, error) {
	var raw sql.NullString
	err := r.db.QueryRow(`SELECT purged_at FROM strategies WHERE strategy_id = ?`, strategyID).Scan(&raw)
	if err != nil {
		return time.Time{}, false, err
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, raw.String)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
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
