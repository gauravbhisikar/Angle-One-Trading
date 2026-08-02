package storage

import (
	"database/sql"
	"time"
)

type ReviewRepo struct{ db *sql.DB }

func NewReviewRepo(db *sql.DB) *ReviewRepo { return &ReviewRepo{db: db} }

func (r *ReviewRepo) SaveDailyReview(strategyID string, version int, date string, json string) error {
	_, err := r.db.Exec(
		`INSERT INTO daily_reviews (strategy_id, strategy_version, review_date, json, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(strategy_id, strategy_version, review_date) DO UPDATE SET json=excluded.json`,
		strategyID, version, date, json, time.Now().UTC().Format(rfc3339),
	)
	return err
}

func (r *ReviewRepo) GetDailyReview(strategyID string, version int, date string) (string, error) {
	var out string
	err := r.db.QueryRow(
		`SELECT json FROM daily_reviews WHERE strategy_id = ? AND strategy_version = ? AND review_date = ?`,
		strategyID, version, date,
	).Scan(&out)
	return out, err
}

func (r *ReviewRepo) SaveAIReview(strategyID string, version int, from, to, json string) error {
	_, err := r.db.Exec(
		`INSERT INTO ai_reviews (strategy_id, strategy_version, period_from, period_to, json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		strategyID, version, from, to, json, time.Now().UTC().Format(rfc3339),
	)
	return err
}

func (r *ReviewRepo) GetLatestAIReview(strategyID string, version int) (string, error) {
	var out string
	err := r.db.QueryRow(
		`SELECT json FROM ai_reviews WHERE strategy_id = ? AND strategy_version = ? ORDER BY created_at DESC LIMIT 1`,
		strategyID, version,
	).Scan(&out)
	return out, err
}

func (r *ReviewRepo) DeleteByStrategy(strategyID string) error {
	if _, err := r.db.Exec(`DELETE FROM daily_reviews WHERE strategy_id = ?`, strategyID); err != nil {
		return err
	}
	_, err := r.db.Exec(`DELETE FROM ai_reviews WHERE strategy_id = ?`, strategyID)
	return err
}
