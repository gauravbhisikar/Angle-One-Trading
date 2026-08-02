package memory

import (
	"context"
	"fmt"
	"time"
)

type Lesson struct {
	Key          string
	Description  string
	TimesSeen    int
	TimesSuccess int
	TimesFailed  int
	Confidence   float64 // times_success / times_seen
	UpdatedAt    time.Time
}

// RecordLesson increments one lesson's outcome counters and recomputes
// confidence — this is genuinely aggregated experience, not an
// append-only event on its own, though a LessonLearned event is still
// appended so the audit trail shows when/why confidence shifted.
// description is only used the first time key is seen (an upsert on an
// existing key doesn't overwrite the description with a possibly-empty one).
func (m *Manager) RecordLesson(ctx context.Context, key, description string, success bool) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	successInc, failInc := 0, 0
	if success {
		successInc = 1
	} else {
		failInc = 1
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO lessons (lesson_key, description, times_seen, times_success, times_failed, confidence, updated_at)
		 VALUES (?, ?, 1, ?, ?, ?, ?)
		 ON CONFLICT(lesson_key) DO UPDATE SET
		   times_seen = times_seen + 1,
		   times_success = times_success + excluded.times_success,
		   times_failed = times_failed + excluded.times_failed,
		   confidence = CAST(times_success + excluded.times_success AS REAL) / (times_seen + 1),
		   updated_at = excluded.updated_at`,
		key, description, successInc, failInc, boolToConfidence(success), time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("memory: record lesson: %w", err)
	}
	if err := appendEvent(ctx, tx, EventLessonLearned, "", map[string]interface{}{"lesson_key": key, "success": success}); err != nil {
		return err
	}
	return tx.Commit()
}

func boolToConfidence(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (m *Manager) GetLessons(ctx context.Context) ([]Lesson, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT lesson_key, description, times_seen, times_success, times_failed, confidence, updated_at
		 FROM lessons ORDER BY times_seen DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Lesson
	for rows.Next() {
		var l Lesson
		var confidence, updatedAt string
		if err := rows.Scan(&l.Key, &l.Description, &l.TimesSeen, &l.TimesSuccess, &l.TimesFailed, &confidence, &updatedAt); err != nil {
			return nil, err
		}
		fmt.Sscanf(confidence, "%f", &l.Confidence)
		l.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, l)
	}
	return out, rows.Err()
}
