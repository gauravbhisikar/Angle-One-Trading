// Package memory is the persistent, cross-agent memory store — every
// strategy ever created, the market context the AI saw when creating it,
// every backtest, every paper-trading deployment, every trade, every
// daily snapshot, every review, and every lesson learned. Nothing here
// depends on conversation history; a future agent process restarts cold
// and rebuilds everything it knows by querying this store.
//
// V1 scope (deliberately not the full architecture some designs propose):
// covers Strategy/Context/Backtest/Execution/Reflection/Lessons — every
// layer with a concrete consumer today. NOT built: a "System/Knowledge"
// static-config layer (low value, mostly duplicates docs/DSL_SPEC.md and
// docs/ENGINE_SPEC.md), a "Research Memory" layer (no research agent
// exists yet to produce research runs), User Preference memory (no
// preference-collection UI exists), and semantic/vector search (real
// future work, but nothing queries "find similar strategies" yet — see
// BACKLOG.md). Building those now would be guessing at a shape before
// there's a real consumer, the same trap avoided elsewhere in this
// project (connectors/BACKLOG.md's deferred confidence-scoring/regime
// items).
//
// Event-sourced core: every write appends an immutable row to `events`
// (what happened, when, with what payload) AND updates a read-optimized
// derived table in the same transaction. The events log is the audit
// trail / source of truth; derived tables are for fast queries. Rebuilding
// derived tables by replaying events is possible but not implemented in
// V1 — nothing needs it yet (see BACKLOG.md).
package memory

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Manager struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Manager, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("memory: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("memory: migrate: %w", err)
	}
	return &Manager{db: db}, nil
}

func (m *Manager) Close() error { return m.db.Close() }

const schema = `
-- Immutable event log. Every Save* call in this package appends here in
-- the same transaction as its derived-table write. Never updated, never
-- deleted (this is the audit trail).
CREATE TABLE IF NOT EXISTS events (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	event_type   TEXT NOT NULL,
	strategy_id  TEXT,
	payload_json TEXT NOT NULL,
	created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_strategy ON events(strategy_id);
CREATE INDEX IF NOT EXISTS idx_events_type ON events(event_type);

-- One row per strategy VERSION, never overwritten (DSL_SPEC Sec 26:
-- strategies are immutable, edits create a new version).
CREATE TABLE IF NOT EXISTS strategies (
	strategy_id       TEXT NOT NULL,
	version           INTEGER NOT NULL,
	parent_strategy_id TEXT,
	name              TEXT NOT NULL,
	dsl_json          TEXT NOT NULL,
	objective         TEXT,
	style             TEXT,
	risk              TEXT,
	status            TEXT NOT NULL, -- backtest | paper | live | archived
	change_reason     TEXT,          -- why this version differs from its parent
	created_at        TEXT NOT NULL,
	PRIMARY KEY (strategy_id, version)
);
CREATE INDEX IF NOT EXISTS idx_strategies_parent ON strategies(parent_strategy_id);

-- Exactly what the AI saw when it built this strategy version — the
-- data that lets a future agent answer "why did this fail?" months later.
CREATE TABLE IF NOT EXISTS strategy_context (
	strategy_id    TEXT NOT NULL,
	version        INTEGER NOT NULL,
	market_regime  TEXT,
	vix            TEXT,
	fii_net        TEXT,
	dii_net        TEXT,
	breadth_ad_ratio TEXT,
	news_sentiment TEXT,
	news_score     TEXT,
	pcr            TEXT,
	rsi            TEXT,
	trend          TEXT,
	volume_regime  TEXT,
	notes          TEXT,
	created_at     TEXT NOT NULL,
	PRIMARY KEY (strategy_id, version)
);

-- One row per backtest run (a strategy version can be backtested more
-- than once, e.g. against different date ranges — not unique per version).
CREATE TABLE IF NOT EXISTS backtests (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	strategy_id     TEXT NOT NULL,
	version         INTEGER NOT NULL,
	period_from     TEXT,
	period_to       TEXT,
	total_return    TEXT,
	cagr            TEXT,
	drawdown        TEXT,
	sharpe          TEXT,
	sortino         TEXT,
	profit_factor   TEXT,
	win_rate        TEXT,
	total_trades    INTEGER,
	equity_curve_json TEXT,
	created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_backtests_strategy ON backtests(strategy_id, version);

CREATE TABLE IF NOT EXISTS deployments (
	deployment_id TEXT PRIMARY KEY,
	strategy_id   TEXT NOT NULL,
	version       INTEGER NOT NULL,
	mode          TEXT NOT NULL, -- paper | live
	status        TEXT NOT NULL, -- running | paused | stopped
	started_at    TEXT NOT NULL,
	stopped_at    TEXT
);
CREATE INDEX IF NOT EXISTS idx_deployments_strategy ON deployments(strategy_id);

CREATE TABLE IF NOT EXISTS trades (
	id             TEXT PRIMARY KEY,
	deployment_id  TEXT NOT NULL,
	strategy_id    TEXT NOT NULL,
	symbol         TEXT NOT NULL,
	entry_time     TEXT NOT NULL,
	exit_time      TEXT,
	entry_price    TEXT NOT NULL,
	exit_price     TEXT,
	quantity       INTEGER NOT NULL,
	pnl            TEXT,
	holding_days   INTEGER,
	exit_reason    TEXT,
	created_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_trades_deployment ON trades(deployment_id);
CREATE INDEX IF NOT EXISTS idx_trades_strategy ON trades(strategy_id);

CREATE TABLE IF NOT EXISTS daily_snapshots (
	deployment_id   TEXT NOT NULL,
	date            TEXT NOT NULL,
	portfolio_value TEXT,
	today_return_pct TEXT,
	total_return_pct TEXT,
	drawdown_pct    TEXT,
	open_positions  INTEGER,
	market_regime   TEXT,
	notes           TEXT,
	created_at      TEXT NOT NULL,
	PRIMARY KEY (deployment_id, date)
);

-- Reasoning, not numbers (the numbers live in backtests/daily_snapshots).
CREATE TABLE IF NOT EXISTS reviews (
	id                     INTEGER PRIMARY KEY AUTOINCREMENT,
	strategy_id            TEXT NOT NULL,
	version                INTEGER NOT NULL,
	review_date            TEXT NOT NULL,
	summary                TEXT,
	strengths_json         TEXT,
	weaknesses_json        TEXT,
	recommended_changes_json TEXT,
	confidence             TEXT,
	created_at             TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_reviews_strategy ON reviews(strategy_id);

-- Aggregated experience, not append-only events — counters incremented
-- on every RecordLesson call for the same lesson_key.
CREATE TABLE IF NOT EXISTS lessons (
	lesson_key    TEXT PRIMARY KEY,
	description   TEXT NOT NULL,
	times_seen    INTEGER NOT NULL DEFAULT 0,
	times_success INTEGER NOT NULL DEFAULT 0,
	times_failed  INTEGER NOT NULL DEFAULT 0,
	confidence    TEXT NOT NULL DEFAULT '0',
	updated_at    TEXT NOT NULL
);
`
