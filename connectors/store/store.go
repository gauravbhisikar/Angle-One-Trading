// Package store is a small local SQLite cache for connector data that's
// worth remembering between runs (TRI history, NAV history, holiday
// calendars) — separate from engine/'s own trading.db, since this is
// research/reference data, not trade state.
//
// Automated retention: every source registers a keep-days policy up
// front. Prune() deletes rows older than that policy in one pass — call
// it on a schedule (daily is enough) so the cache never grows unbounded
// with data nobody queries anymore. Data with no natural expiry (TRI/NAV
// history, kept for backtesting) registers KeepDays: 0 (never pruned);
// short-lived data (FII/DII flow, news) gets a real window.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db       *sql.DB
	policies map[string]int // source -> keep-days (0 = keep forever)
}

// RetentionPolicy declares how long one source's snapshots are worth
// keeping. Register every source you write through Save so Prune knows
// what to do with it — an unregistered source defaults to keep-forever,
// which is the safe default (never silently lose data), not the
// space-efficient one.
type RetentionPolicy struct {
	Source   string
	KeepDays int
}

// Default policies for this module's own connectors. A caller can pass
// additional/overriding policies to Open.
var DefaultPolicies = []RetentionPolicy{
	{Source: "nifty_tri", KeepDays: 0},          // benchmark history — keep forever
	{Source: "nav", KeepDays: 0},                // NAV history — keep forever
	{Source: "historical_candles", KeepDays: 0}, // backtest data — keep forever, that's the point
	{Source: "fii_dii", KeepDays: 90},           // recent flow is what matters for sentiment
	{Source: "news", KeepDays: 7},               // headlines are stale fast
	{Source: "holidays", KeepDays: 400},         // refreshed yearly, keep ~13 months of overlap
	{Source: "corp_actions", KeepDays: 0},       // dividend history — keep forever
}

func Open(ctx context.Context, path string, policies ...RetentionPolicy) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}

	s := &Store{db: db, policies: map[string]int{}}
	for _, p := range DefaultPolicies {
		s.policies[p.Source] = p.KeepDays
	}
	for _, p := range policies {
		s.policies[p.Source] = p.KeepDays
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS snapshots (
	source     TEXT NOT NULL,
	key        TEXT NOT NULL,
	date       TEXT NOT NULL,
	value      TEXT NOT NULL,
	fetched_at TEXT NOT NULL,
	PRIMARY KEY (source, key, date)
);
CREATE INDEX IF NOT EXISTS idx_snapshots_source_date ON snapshots(source, date);
`

// Save upserts one (source, key, date) -> value row. Re-saving the same
// date overwrites rather than duplicates — e.g. re-importing a CSV that
// includes an already-known date is a no-op, not a growing table.
func (s *Store) Save(ctx context.Context, source, key, date, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO snapshots (source, key, date, value, fetched_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(source, key, date) DO UPDATE SET value=excluded.value, fetched_at=excluded.fetched_at`,
		source, key, date, value, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

type Snapshot struct {
	Source, Key, Date, Value string
	FetchedAt                time.Time
}

func (s *Store) Latest(ctx context.Context, source, key string) (Snapshot, error) {
	var snap Snapshot
	var fetchedAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT source, key, date, value, fetched_at FROM snapshots
		 WHERE source = ? AND key = ? ORDER BY date DESC LIMIT 1`,
		source, key,
	).Scan(&snap.Source, &snap.Key, &snap.Date, &snap.Value, &fetchedAt)
	if err != nil {
		return Snapshot{}, err
	}
	snap.FetchedAt, _ = time.Parse(time.RFC3339, fetchedAt)
	return snap, nil
}

func (s *Store) History(ctx context.Context, source, key, fromDate, toDate string) ([]Snapshot, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT source, key, date, value, fetched_at FROM snapshots
		 WHERE source = ? AND key = ? AND date BETWEEN ? AND ? ORDER BY date`,
		source, key, fromDate, toDate,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Snapshot
	for rows.Next() {
		var snap Snapshot
		var fetchedAt string
		if err := rows.Scan(&snap.Source, &snap.Key, &snap.Date, &snap.Value, &fetchedAt); err != nil {
			return nil, err
		}
		snap.FetchedAt, _ = time.Parse(time.RFC3339, fetchedAt)
		out = append(out, snap)
	}
	return out, rows.Err()
}

// Prune deletes snapshots older than each source's registered KeepDays.
// Sources with KeepDays 0 (or unregistered) are left untouched. Call this
// on a schedule (once a day is plenty) — this is the automated cleanup
// half of "fetch data, keep only what's still needed."
func (s *Store) Prune(ctx context.Context) (deleted int64, err error) {
	for source, keepDays := range s.policies {
		if keepDays <= 0 {
			continue
		}
		cutoff := time.Now().AddDate(0, 0, -keepDays).Format("2006-01-02")
		res, err := s.db.ExecContext(ctx, `DELETE FROM snapshots WHERE source = ? AND date < ?`, source, cutoff)
		if err != nil {
			return deleted, fmt.Errorf("store: prune %s: %w", source, err)
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	return deleted, nil
}
