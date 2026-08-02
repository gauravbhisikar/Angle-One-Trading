package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// EventType is the fixed vocabulary of things worth recording. Every
// Save*/Record* call appends exactly one of these to the immutable
// events log alongside its derived-table write.
type EventType string

const (
	EventStrategyCreated    EventType = "StrategyCreated"
	EventStrategyBacktested EventType = "StrategyBacktested"
	EventStrategyDeployed   EventType = "StrategyDeployed"
	EventDeploymentStatus   EventType = "DeploymentStatusChanged"
	EventTradeOpened        EventType = "TradeOpened"
	EventTradeClosed        EventType = "TradeClosed"
	EventDailySnapshotSaved EventType = "DailySnapshotSaved"
	EventReviewGenerated    EventType = "DailyReviewGenerated"
	EventLessonLearned      EventType = "LessonLearned"
	EventStrategyRetired    EventType = "StrategyRetired"
)

func appendEvent(ctx context.Context, tx *sql.Tx, eventType EventType, strategyID string, payload interface{}) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var sid sql.NullString
	if strategyID != "" {
		sid = sql.NullString{String: strategyID, Valid: true}
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO events (event_type, strategy_id, payload_json, created_at) VALUES (?, ?, ?, ?)`,
		string(eventType), sid, string(raw), time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

type Event struct {
	ID         int64
	Type       EventType
	StrategyID string
	Payload    string
	CreatedAt  time.Time
}

// EventsForStrategy returns the full immutable history for one strategy —
// the audit trail a future agent (or a human debugging "why did this
// happen") can replay in order.
func (m *Manager) EventsForStrategy(ctx context.Context, strategyID string) ([]Event, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, event_type, strategy_id, payload_json, created_at FROM events WHERE strategy_id = ? ORDER BY id`,
		strategyID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var sid sql.NullString
		var createdAt string
		if err := rows.Scan(&e.ID, &e.Type, &sid, &e.Payload, &createdAt); err != nil {
			return nil, err
		}
		e.StrategyID = sid.String
		e.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		out = append(out, e)
	}
	return out, rows.Err()
}
