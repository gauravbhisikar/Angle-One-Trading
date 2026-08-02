package storage

import (
	"database/sql"
	"time"
)

// PredictedMetrics is what a backtest said before deployment — captured
// once at deploy time so a caller can later compare it against real
// live/paper performance (analytics.Compute over actual trades) without
// having to re-run the backtest.
type PredictedMetrics struct {
	StrategyID     string
	CAGR           float64
	Sharpe         float64
	Sortino        float64
	Drawdown       float64
	WinRate        float64
	ProfitFactor   float64
	TotalTrades    int
	Source         string // e.g. "agent" — where this prediction came from
	Description    string // what the agent generated — the strategy in plain English
	Rationale      string // why the agent picked/recommended it, grounded in real numbers
	ConfidenceJSON string // full {score, breakdown, basis} blob from nodes/robustness.py, opaque to the engine
	CreatedAt      time.Time
}

type PredictedMetricsRepo struct{ db *sql.DB }

func NewPredictedMetricsRepo(db *sql.DB) *PredictedMetricsRepo { return &PredictedMetricsRepo{db: db} }

// Save upserts — a strategy redeployed after a new backtest replaces its
// old prediction rather than accumulating a history of them (V1 scope:
// "what did the backtest say this time", not a prediction log).
func (r *PredictedMetricsRepo) Save(m PredictedMetrics) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := r.db.Exec(
		`INSERT INTO predicted_metrics (strategy_id, cagr, sharpe, sortino, drawdown, win_rate, profit_factor, total_trades, source, description, rationale, confidence_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(strategy_id) DO UPDATE SET cagr=excluded.cagr, sharpe=excluded.sharpe, sortino=excluded.sortino,
		 drawdown=excluded.drawdown, win_rate=excluded.win_rate, profit_factor=excluded.profit_factor,
		 total_trades=excluded.total_trades, source=excluded.source, description=excluded.description,
		 rationale=excluded.rationale, confidence_json=excluded.confidence_json, created_at=excluded.created_at`,
		m.StrategyID, m.CAGR, m.Sharpe, m.Sortino, m.Drawdown, m.WinRate, m.ProfitFactor, m.TotalTrades, m.Source,
		m.Description, m.Rationale, m.ConfidenceJSON, m.CreatedAt.Format(rfc3339),
	)
	return err
}

func (r *PredictedMetricsRepo) Get(strategyID string) (PredictedMetrics, bool, error) {
	var m PredictedMetrics
	var createdAt string
	err := r.db.QueryRow(
		`SELECT strategy_id, cagr, sharpe, sortino, drawdown, win_rate, profit_factor, total_trades, source, description, rationale, confidence_json, created_at
		 FROM predicted_metrics WHERE strategy_id = ?`,
		strategyID,
	).Scan(&m.StrategyID, &m.CAGR, &m.Sharpe, &m.Sortino, &m.Drawdown, &m.WinRate, &m.ProfitFactor, &m.TotalTrades, &m.Source,
		&m.Description, &m.Rationale, &m.ConfidenceJSON, &createdAt)
	if err == sql.ErrNoRows {
		return PredictedMetrics{}, false, nil
	}
	if err != nil {
		return PredictedMetrics{}, false, err
	}
	m.CreatedAt = parseTime(createdAt)
	return m, true, nil
}

func (r *PredictedMetricsRepo) DeleteByStrategy(strategyID string) error {
	_, err := r.db.Exec(`DELETE FROM predicted_metrics WHERE strategy_id = ?`, strategyID)
	return err
}
