// Package storage persists strategies, orders, trades, logs, and reviews
// to SQLite. Prices are stored as TEXT (decimal string), never as
// floating-point columns, so the Decimal contract (ENGINE_SPEC Sec 2)
// holds all the way to disk.
package storage

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("storage: open: %w", err)
	}
	db.SetMaxOpenConns(1) // sqlite: single writer, avoids SQLITE_BUSY under this engine's light concurrency
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS strategies (
	strategy_id TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	first_run_at TEXT NOT NULL DEFAULT '',
	last_run_at  TEXT NOT NULL DEFAULT '',
	purged_at    TEXT NOT NULL DEFAULT '',
	final_pnl              TEXT NOT NULL DEFAULT '',
	final_win_rate         REAL NOT NULL DEFAULT 0,
	final_profit_factor    REAL NOT NULL DEFAULT 0,
	final_completed_trades INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS strategy_versions (
	strategy_id TEXT NOT NULL,
	version     INTEGER NOT NULL,
	dsl_json    TEXT NOT NULL,
	enabled     INTEGER NOT NULL DEFAULT 1,
	created_at  TEXT NOT NULL,
	PRIMARY KEY (strategy_id, version)
);

CREATE TABLE IF NOT EXISTS orders (
	id               TEXT PRIMARY KEY,
	strategy_id      TEXT NOT NULL,
	strategy_version INTEGER NOT NULL,
	symbol           TEXT NOT NULL,
	side             TEXT NOT NULL,
	product          TEXT NOT NULL,
	order_type       TEXT NOT NULL,
	quantity         INTEGER NOT NULL,
	limit_price      TEXT NOT NULL,
	filled_quantity  INTEGER NOT NULL,
	avg_fill_price   TEXT NOT NULL,
	state            TEXT NOT NULL,
	reject_reason    TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_orders_strategy ON orders(strategy_id, strategy_version);

CREATE TABLE IF NOT EXISTS trades (
	id                TEXT PRIMARY KEY,
	strategy_id       TEXT NOT NULL,
	strategy_version  INTEGER NOT NULL,
	symbol            TEXT NOT NULL,
	direction         TEXT NOT NULL,
	entry_order_id    TEXT NOT NULL DEFAULT '',
	exit_order_id     TEXT NOT NULL DEFAULT '',
	quantity          INTEGER NOT NULL,
	entry_price       TEXT NOT NULL,
	exit_price        TEXT NOT NULL DEFAULT '0',
	high_water_mark   TEXT NOT NULL DEFAULT '0',
	low_water_mark    TEXT NOT NULL DEFAULT '0',
	state             TEXT NOT NULL,
	close_reason      TEXT NOT NULL DEFAULT '',
	entry_time        TEXT NOT NULL,
	exit_time         TEXT,
	holding_days      INTEGER NOT NULL DEFAULT 0,
	pnl               TEXT NOT NULL DEFAULT '0',
	costs             TEXT NOT NULL DEFAULT '0'
);
CREATE INDEX IF NOT EXISTS idx_trades_strategy ON trades(strategy_id, strategy_version);

CREATE TABLE IF NOT EXISTS logs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	strategy_id TEXT NOT NULL,
	level       TEXT NOT NULL,
	message     TEXT NOT NULL,
	created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_logs_strategy ON logs(strategy_id);

CREATE TABLE IF NOT EXISTS daily_reviews (
	strategy_id      TEXT NOT NULL,
	strategy_version INTEGER NOT NULL,
	review_date      TEXT NOT NULL,
	json             TEXT NOT NULL,
	created_at       TEXT NOT NULL,
	PRIMARY KEY (strategy_id, strategy_version, review_date)
);

CREATE TABLE IF NOT EXISTS ai_reviews (
	strategy_id      TEXT NOT NULL,
	strategy_version INTEGER NOT NULL,
	period_from      TEXT NOT NULL,
	period_to        TEXT NOT NULL,
	json             TEXT NOT NULL,
	created_at       TEXT NOT NULL
);

-- What a backtest predicted at deploy time, so the dashboard can compare
-- it against real live/paper performance later (internal/analytics.Compute
-- over actual trades) — one row per strategy_id, overwritten if re-deployed.
CREATE TABLE IF NOT EXISTS predicted_metrics (
	strategy_id   TEXT PRIMARY KEY,
	cagr          REAL NOT NULL,
	sharpe        REAL NOT NULL,
	sortino       REAL NOT NULL,
	drawdown      REAL NOT NULL,
	win_rate      REAL NOT NULL,
	profit_factor REAL NOT NULL,
	total_trades  INTEGER NOT NULL,
	source        TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL
);
`

// predictedMetricsExtraColumns adds description/rationale/confidence_json
// to predicted_metrics for databases created before those columns existed
// — CREATE TABLE IF NOT EXISTS doesn't add columns to an already-existing
// table, so a fresh ALTER TABLE per column is needed. SQLite has no "ADD
// COLUMN IF NOT EXISTS", so a "duplicate column" error is expected and
// ignored on every run after the first.
var predictedMetricsExtraColumns = []string{
	`ALTER TABLE predicted_metrics ADD COLUMN description TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE predicted_metrics ADD COLUMN rationale TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE predicted_metrics ADD COLUMN confidence_json TEXT NOT NULL DEFAULT ''`,
}

// strategiesExtraColumns adds first_run_at to strategies for databases
// created before evalcutoff existed — same duplicate-column-tolerant
// pattern as predictedMetricsExtraColumns.
var strategiesExtraColumns = []string{
	`ALTER TABLE strategies ADD COLUMN first_run_at TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE strategies ADD COLUMN last_run_at TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE strategies ADD COLUMN purged_at TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE strategies ADD COLUMN final_pnl TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE strategies ADD COLUMN final_win_rate REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE strategies ADD COLUMN final_profit_factor REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE strategies ADD COLUMN final_completed_trades INTEGER NOT NULL DEFAULT 0`,
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("storage: migrate: %w", err)
	}
	for _, stmt := range predictedMetricsExtraColumns {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("storage: migrate predicted_metrics columns: %w", err)
		}
	}
	for _, stmt := range strategiesExtraColumns {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("storage: migrate strategies columns: %w", err)
		}
	}
	return nil
}
