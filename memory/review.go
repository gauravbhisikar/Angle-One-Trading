package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Review is reasoning, not numbers — the numbers already live in
// backtests/daily_snapshots. This is "why," for a future agent (or
// human) to read without re-deriving it from raw trade data.
type Review struct {
	ID                 int64
	StrategyID         string
	Version            int
	ReviewDate         string
	Summary            string
	Strengths          []string
	Weaknesses         []string
	RecommendedChanges []string
	Confidence         string
	CreatedAt          time.Time
}

func (m *Manager) SaveReview(ctx context.Context, r Review) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	strengths, _ := json.Marshal(r.Strengths)
	weaknesses, _ := json.Marshal(r.Weaknesses)
	changes, _ := json.Marshal(r.RecommendedChanges)

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO reviews (strategy_id, version, review_date, summary, strengths_json, weaknesses_json,
		 recommended_changes_json, confidence, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.StrategyID, r.Version, r.ReviewDate, r.Summary, string(strengths), string(weaknesses),
		string(changes), r.Confidence, r.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("memory: save review: %w", err)
	}
	id, _ := res.LastInsertId()
	r.ID = id
	if err := appendEvent(ctx, tx, EventReviewGenerated, r.StrategyID, r); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) GetReviewsForStrategy(ctx context.Context, strategyID string) ([]Review, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, strategy_id, version, review_date, coalesce(summary,''), strengths_json, weaknesses_json,
		 recommended_changes_json, confidence, created_at FROM reviews WHERE strategy_id = ? ORDER BY review_date`,
		strategyID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Review
	for rows.Next() {
		var r Review
		var strengths, weaknesses, changes, createdAt string
		if err := rows.Scan(&r.ID, &r.StrategyID, &r.Version, &r.ReviewDate, &r.Summary, &strengths, &weaknesses, &changes, &r.Confidence, &createdAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(strengths), &r.Strengths)
		json.Unmarshal([]byte(weaknesses), &r.Weaknesses)
		json.Unmarshal([]byte(changes), &r.RecommendedChanges)
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		out = append(out, r)
	}
	return out, rows.Err()
}
