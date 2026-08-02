package storage

import (
	"database/sql"
	"time"
)

type LogRepo struct{ db *sql.DB }

func NewLogRepo(db *sql.DB) *LogRepo { return &LogRepo{db: db} }

func (r *LogRepo) Insert(strategyID, level, message string) error {
	_, err := r.db.Exec(
		`INSERT INTO logs (strategy_id, level, message, created_at) VALUES (?, ?, ?, ?)`,
		strategyID, level, message, time.Now().UTC().Format(rfc3339),
	)
	return err
}

func (r *LogRepo) DeleteByStrategy(strategyID string) error {
	_, err := r.db.Exec(`DELETE FROM logs WHERE strategy_id = ?`, strategyID)
	return err
}

type LogEntry struct {
	StrategyID string
	Level      string
	Message    string
	CreatedAt  time.Time
}

func (r *LogRepo) ListByStrategy(strategyID string, limit int) ([]LogEntry, error) {
	rows, err := r.db.Query(
		`SELECT strategy_id, level, message, created_at FROM logs WHERE strategy_id = ? ORDER BY id DESC LIMIT ?`,
		strategyID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		var createdAt string
		if err := rows.Scan(&e.StrategyID, &e.Level, &e.Message, &createdAt); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(createdAt)
		out = append(out, e)
	}
	return out, rows.Err()
}
