// Package tri covers NIFTY 50 TRI (Total Return Index) — the fair
// benchmark for NIFTYBEES (DSL_SPEC benchmark field), since NIFTYBEES
// pays out dividends the price-only NIFTY 50 index doesn't reflect.
//
// No free live/scriptable API was found for this despite real attempts
// (niftyindices.com's historical-data page calls an undocumented,
// private ASP.NET WebMethod — two different payload shapes were tried
// against it live and both were rejected; reverse-engineering it further
// wasn't worth chasing an endpoint that could change without notice
// anyway). What NSE Indices does offer for free is an interactive
// export button on that same page a human can click.
//
// So this package is deliberately CSV-drop automated rather than
// live-scrape automated: export the date range you need from
// niftyindices.com once (daily granularity is enough — DSL_SPEC's
// benchmark comparison is not intraday), drop the file in a watch
// folder, and everything downstream — parsing, caching, staleness
// tracking, retention pruning — runs with zero manual steps from there.
// If niftyindices.com's endpoint ever gets documented or reverse
// engineered cleanly, swap Import's caller for a live fetch; nothing
// else in this package needs to change.
package tri

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"connectors/store"
)

const Source = "nifty_tri"
const Key = "NIFTY50_TR"

type Point struct {
	Date  time.Time
	Close decimal.Decimal
}

// Import parses one CSV export (as downloaded from niftyindices.com's
// historical-data page — a "Date" column plus a "Close"/"Index
// Value"/"TotalReturnsIndex" column, header names matched
// case-insensitively so minor export-format changes don't break it) and
// saves every row into store.
func Import(ctx context.Context, st *store.Store, csvPath string) (int, error) {
	f, err := os.Open(csvPath)
	if err != nil {
		return 0, fmt.Errorf("tri: open %s: %w", csvPath, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return 0, fmt.Errorf("tri: read header: %w", err)
	}

	dateCol, closeCol := -1, -1
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "date" {
			dateCol = i
		}
		if h == "close" || h == "index value" || h == "totalreturnsindex" || h == "closing value" {
			closeCol = i
		}
	}
	if dateCol == -1 || closeCol == -1 {
		return 0, fmt.Errorf("tri: couldn't find Date/Close columns in header %v", header)
	}

	dateLayouts := []string{"02-Jan-2006", "02-01-2006", "2006-01-02"}
	count := 0
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, fmt.Errorf("tri: read row %d: %w", count, err)
		}
		if len(row) <= dateCol || len(row) <= closeCol {
			continue
		}

		var date time.Time
		for _, layout := range dateLayouts {
			if d, err := time.Parse(layout, strings.TrimSpace(row[dateCol])); err == nil {
				date = d
				break
			}
		}
		if date.IsZero() {
			continue // unparseable date, skip rather than abort the whole import
		}

		value, err := decimal.NewFromString(strings.TrimSpace(row[closeCol]))
		if err != nil {
			continue
		}

		if err := st.Save(ctx, Source, Key, date.Format("2006-01-02"), value.String()); err != nil {
			return count, fmt.Errorf("tri: save %s: %w", date.Format("2006-01-02"), err)
		}
		count++
	}
	return count, nil
}

// AutoImport scans watchDir for *.csv files, imports each one, then moves
// it into watchDir/processed so a re-run never reprocesses the same file
// — this is the "drop a file, everything else is automatic" half of the
// pipeline. Safe to call on every startup/schedule tick: an empty or
// already-processed watch folder is a fast no-op.
func AutoImport(ctx context.Context, st *store.Store, watchDir string) (imported int, err error) {
	processedDir := filepath.Join(watchDir, "processed")
	if err := os.MkdirAll(processedDir, 0o755); err != nil {
		return 0, fmt.Errorf("tri: create processed dir: %w", err)
	}

	entries, err := os.ReadDir(watchDir)
	if err != nil {
		return 0, fmt.Errorf("tri: read watch dir: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".csv") {
			continue
		}
		path := filepath.Join(watchDir, e.Name())
		n, err := Import(ctx, st, path)
		if err != nil {
			return imported, fmt.Errorf("tri: import %s: %w", e.Name(), err)
		}
		imported += n

		if err := os.Rename(path, filepath.Join(processedDir, e.Name())); err != nil {
			return imported, fmt.Errorf("tri: archive %s after import: %w", e.Name(), err)
		}
	}
	return imported, nil
}

// Latest returns the most recent cached TRI value.
func Latest(ctx context.Context, st *store.Store) (Point, error) {
	snap, err := st.Latest(ctx, Source, Key)
	if err != nil {
		return Point{}, fmt.Errorf("tri: no cached TRI data yet — run AutoImport first: %w", err)
	}
	date, _ := time.Parse("2006-01-02", snap.Date)
	value, _ := decimal.NewFromString(snap.Value)
	return Point{Date: date, Close: value}, nil
}

// ReturnSince computes % TRI return from a given date to the latest
// cached point — the actual number DSL_SPEC's benchmark_return needs.
func ReturnSince(ctx context.Context, st *store.Store, from time.Time) (decimal.Decimal, error) {
	history, err := st.History(ctx, Source, Key, from.Format("2006-01-02"), time.Now().Format("2006-01-02"))
	if err != nil {
		return decimal.Zero, err
	}
	if len(history) < 2 {
		return decimal.Zero, fmt.Errorf("tri: not enough cached history since %s (have %d points)", from.Format("2006-01-02"), len(history))
	}
	startVal, _ := decimal.NewFromString(history[0].Value)
	endVal, _ := decimal.NewFromString(history[len(history)-1].Value)
	if startVal.IsZero() {
		return decimal.Zero, fmt.Errorf("tri: zero starting value, cannot compute return")
	}
	return endVal.Sub(startVal).Div(startVal).Mul(decimal.NewFromInt(100)), nil
}
