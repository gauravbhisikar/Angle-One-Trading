// Package featurestore persists daily computed features per symbol —
// technical indicators computed once and stored, queryable historically,
// instead of every consumer (memory context snapshots, a future Decision
// Context Engine, backtests) recomputing RSI/EMA/ADX from raw candles
// every time it wants to know "what was the trend on 2024-03-15."
//
// Lives inside engine/ (not its own top-level module like connectors/ or
// memory/) for a concrete reason, not convenience: it reuses the engine's
// actual indicator implementations (internal/indicators) rather than
// reimplementing RSI/EMA/MACD/ADX a third time. Go's internal/ package
// visibility means only code inside the tradingengine module tree can
// import tradingengine/internal/indicators — a separate module could not,
// even with a replace directive. Reimplementing the math independently
// was the alternative, and this project has already avoided that trap
// once (internal/backtest reuses the live strategy runtime instead of a
// separate simulator, for the same drift-risk reason).
//
// Scope: technical indicators are computed here, deterministically, from
// caller-supplied candles (same "engine never fetches external data
// itself" boundary as POST /backtest). Macro/sentiment columns (VIX,
// FII/DII, breadth, news sentiment) exist in the schema but are NOT
// backfilled historically — those connectors only reliably give
// current-day snapshots (see connectors/README.md), not free historical
// series, so they're populated via UpsertMacro for whatever day a caller
// actually has data for, sparse by design rather than faked.
package featurestore

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("featurestore: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("featurestore: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS features (
	symbol         TEXT NOT NULL,
	date           TEXT NOT NULL, -- YYYY-MM-DD

	-- Technical, computed deterministically from candles by this package.
	close          TEXT,
	rsi14          TEXT,
	ema20          TEXT,
	ema50          TEXT,
	ema_cross_bullish INTEGER, -- 1/0/NULL (NULL = no signal that day)
	ema_cross_bearish INTEGER,
	macd_bullish   INTEGER,
	macd_bearish   INTEGER,
	adx14          TEXT,
	atr14          TEXT,
	bollinger_percent_b TEXT,

	-- Macro/sentiment, sparse — populated via UpsertMacro when a caller
	-- has it for that day, never backfilled/guessed.
	vix              TEXT,
	fii_net          TEXT,
	dii_net          TEXT,
	breadth_ad_ratio TEXT,
	news_sentiment   TEXT,
	news_score       TEXT,

	updated_at TEXT NOT NULL,
	PRIMARY KEY (symbol, date)
);
CREATE INDEX IF NOT EXISTS idx_features_symbol_date ON features(symbol, date);
`
