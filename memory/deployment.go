package memory

import (
	"context"
	"fmt"
	"time"
)

type Deployment struct {
	DeploymentID string
	StrategyID   string
	Version      int
	Mode         string // paper | live
	Status       string // running | paused | stopped
	StartedAt    time.Time
	StoppedAt    *time.Time
}

func (m *Manager) SaveDeployment(ctx context.Context, d Deployment) error {
	if d.StartedAt.IsZero() {
		d.StartedAt = time.Now().UTC()
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO deployments (deployment_id, strategy_id, version, mode, status, started_at, stopped_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL)`,
		d.DeploymentID, d.StrategyID, d.Version, d.Mode, d.Status, d.StartedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("memory: save deployment: %w", err)
	}
	if err := appendEvent(ctx, tx, EventStrategyDeployed, d.StrategyID, d); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateDeploymentStatus records a run/pause/resume/stop transition —
// updates the derived row and appends the immutable event, same pattern
// as every other Save* here.
func (m *Manager) UpdateDeploymentStatus(ctx context.Context, deploymentID, strategyID, status string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var stoppedAt interface{}
	if status == "stopped" {
		stoppedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE deployments SET status = ?, stopped_at = coalesce(?, stopped_at) WHERE deployment_id = ?`,
		status, stoppedAt, deploymentID,
	); err != nil {
		return fmt.Errorf("memory: update deployment status: %w", err)
	}
	if err := appendEvent(ctx, tx, EventDeploymentStatus, strategyID, map[string]string{"deployment_id": deploymentID, "status": status}); err != nil {
		return err
	}
	return tx.Commit()
}

// GetCurrentDeployments returns every deployment not in "stopped" status —
// what a strategy generator should check before proposing something that
// overlaps with what's already running.
func (m *Manager) GetCurrentDeployments(ctx context.Context) ([]Deployment, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT deployment_id, strategy_id, version, mode, status, started_at, stopped_at
		 FROM deployments WHERE status != 'stopped' ORDER BY started_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Deployment
	for rows.Next() {
		var d Deployment
		var startedAt string
		var stoppedAt *string
		if err := rows.Scan(&d.DeploymentID, &d.StrategyID, &d.Version, &d.Mode, &d.Status, &startedAt, &stoppedAt); err != nil {
			return nil, err
		}
		d.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
		if stoppedAt != nil {
			t, _ := time.Parse(time.RFC3339, *stoppedAt)
			d.StoppedAt = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
