package connectors_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"connectors/historical"
	"connectors/httpx"
	"connectors/store"
)

func TestHistoricalSyncAndRefresh(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := httpx.New()

	dbPath := filepath.Join(t.TempDir(), "cache.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	n, err := historical.SyncHistory(ctx, client, st, historical.DefaultSymbol, historical.DefaultYahooTicker, 5)
	if err != nil {
		t.Fatalf("SyncHistory: %v", err)
	}
	// ~250 trading days/year * 5 years, roughly - just sanity check scale.
	if n < 500 {
		t.Fatalf("expected several hundred+ daily candles for 5 years, got %d", n)
	}
	t.Logf("synced %d daily candles", n)

	latest, err := historical.LatestCandle(ctx, st, historical.DefaultSymbol)
	if err != nil {
		t.Fatalf("LatestCandle: %v", err)
	}
	if latest.Close.IsZero() {
		t.Fatal("expected non-zero latest close")
	}
	t.Logf("latest candle: date=%s close=%s", latest.Date.Format("2006-01-02"), latest.Close)

	history, err := historical.GetHistory(ctx, st, historical.DefaultSymbol, latest.Date.AddDate(-1, 0, 0), latest.Date)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) < 200 {
		t.Fatalf("expected ~250 trading days in the last year, got %d", len(history))
	}
	t.Logf("last 1y window: %d candles, first=%s last=%s", len(history), history[0].Date.Format("2006-01-02"), history[len(history)-1].Date.Format("2006-01-02"))

	// Refresh should be a fast, idempotent no-error top-up.
	refreshed, err := historical.Refresh(ctx, client, st, historical.DefaultSymbol, historical.DefaultYahooTicker)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	t.Logf("refresh upserted %d rows (overlap expected, not an error)", refreshed)
}
