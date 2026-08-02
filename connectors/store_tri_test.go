package connectors_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectors/httpx"
	"connectors/overnight"
	"connectors/store"
	"connectors/tri"
)

func TestStoreRetentionAndTRIAutoImport(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cache.db")

	ctx := context.Background()
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	// Write a fake CSV export shaped like niftyindices.com's format and
	// drop it in a watch folder — proves the "drop file, everything else
	// automatic" pipeline end to end.
	watchDir := filepath.Join(dir, "watch")
	if err := os.MkdirAll(watchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	csvContent := "Date,Open,High,Low,Close\n01-Jul-2026,28000.00,28100.00,27950.00,28050.00\n02-Jul-2026,28050.00,28200.00,28000.00,28150.00\n"
	if err := os.WriteFile(filepath.Join(watchDir, "tri_export.csv"), []byte(csvContent), 0o644); err != nil {
		t.Fatal(err)
	}

	imported, err := tri.AutoImport(ctx, st, watchDir)
	if err != nil {
		t.Fatalf("AutoImport: %v", err)
	}
	if imported != 2 {
		t.Fatalf("expected 2 rows imported, got %d", imported)
	}

	// File should have moved to processed/ — re-running must not double-import.
	if _, err := os.Stat(filepath.Join(watchDir, "processed", "tri_export.csv")); err != nil {
		t.Fatalf("expected processed file to be archived: %v", err)
	}
	imported2, err := tri.AutoImport(ctx, st, watchDir)
	if err != nil {
		t.Fatalf("second AutoImport: %v", err)
	}
	if imported2 != 0 {
		t.Fatalf("expected 0 rows on re-run (already archived), got %d", imported2)
	}

	latest, err := tri.Latest(ctx, st)
	if err != nil {
		t.Fatalf("tri.Latest: %v", err)
	}
	if latest.Close.String() != "28150" {
		t.Fatalf("expected latest close 28150, got %s", latest.Close)
	}

	// Retention: TRI is registered keep-forever, but a short-lived source
	// with an old date must actually get pruned.
	oldDate := time.Now().AddDate(0, 0, -400).Format("2006-01-02")
	if err := st.Save(ctx, "news", "headline-1", oldDate, "stale headline"); err != nil {
		t.Fatal(err)
	}
	recentDate := time.Now().Format("2006-01-02")
	if err := st.Save(ctx, "news", "headline-2", recentDate, "fresh headline"); err != nil {
		t.Fatal(err)
	}

	deleted, err := st.Prune(ctx)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected exactly 1 row pruned (the stale news headline), got %d", deleted)
	}

	// TRI history must have survived the prune untouched (keep-forever policy).
	triHistory, err := st.History(ctx, tri.Source, tri.Key, "2026-01-01", "2026-12-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(triHistory) != 2 {
		t.Fatalf("expected TRI's 2 rows to survive pruning, got %d", len(triHistory))
	}
}

func TestOvernightCascadeFallsBackToUSMarkets(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := httpx.New()

	// No GIFT provider, no Angel One client — must fall through SGX (always
	// skipped) and futures (skipped, no client) down to the US-markets rung.
	sig, err := overnight.Fetch(ctx, client, nil, nil, nil, "", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if sig.Source != overnight.SourceUSMarkets {
		t.Fatalf("expected cascade to land on us_markets_usdinr, got %s (confidence=%.2f, notes=%s)", sig.Source, sig.Confidence, sig.Notes)
	}
	if sig.Confidence <= 0 || sig.Confidence >= 1 {
		t.Fatalf("expected confidence in (0,1), got %.2f", sig.Confidence)
	}
	t.Logf("overnight signal: source=%s change_pct=%s confidence=%.2f detail=%v", sig.Source, sig.ChangePct, sig.Confidence, sig.Detail)
}
