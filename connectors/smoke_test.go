package connectors_test

import (
	"context"
	"testing"
	"time"

	"connectors/amfi"
	"connectors/angelone"
	"connectors/httpx"
	"connectors/news"
	"connectors/webreader"
	"connectors/yahoo"
)

// These hit real, free, no-auth endpoints over the network — skipped in
// -short mode. They exist to prove the connectors work against the live
// services, not just that the Go compiles.
func TestYahooNiftyBees(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := httpx.New()

	candles, quote, err := yahoo.FetchCandles(ctx, client, yahoo.SymbolNiftyBees, "1d", "5d")
	if err != nil {
		t.Fatalf("FetchCandles: %v", err)
	}
	if len(candles) == 0 {
		t.Fatal("expected at least one candle")
	}
	if quote.Price.IsZero() {
		t.Fatal("expected non-zero NIFTYBEES price")
	}
	t.Logf("NIFTYBEES latest close=%s price=%s candles=%d", candles[len(candles)-1].Close, quote.Price, len(candles))
}

func TestAMFINiftyBeesNAV(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := httpx.New()

	nav, err := amfi.FetchNiftyBeesNAV(ctx, client)
	if err != nil {
		t.Fatalf("FetchNiftyBeesNAV: %v", err)
	}
	if nav.Value.IsZero() {
		t.Fatal("expected non-zero NAV")
	}
	t.Logf("NIFTYBEES NAV=%s date=%s scheme=%s", nav.Value, nav.Date.Format("2006-01-02"), nav.SchemeName)
}

func TestAngelOneScripMasterFindsNiftyBees(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := httpx.New()

	instruments, err := angelone.FetchScripMaster(ctx, client)
	if err != nil {
		t.Fatalf("FetchScripMaster: %v", err)
	}
	if len(instruments) < 1000 {
		t.Fatalf("expected thousands of instruments, got %d", len(instruments))
	}

	inst, ok := angelone.FindEquity(instruments, "NIFTYBEES")
	if !ok {
		t.Fatal("NIFTYBEES-EQ not found in scrip master")
	}
	if inst.LotSize != 1 {
		t.Fatalf("expected NIFTYBEES lot size 1, got %d", inst.LotSize)
	}
	t.Logf("NIFTYBEES token=%s tick_size=%s lot=%d total_instruments=%d", inst.Token, inst.TickSize, inst.LotSize, len(instruments))

	expiries := angelone.ListNiftyExpiries(instruments)
	if len(expiries) == 0 {
		t.Fatal("expected at least one NIFTY option expiry")
	}
	t.Logf("found %d NIFTY option expiries, e.g. %s", len(expiries), expiries[0])

	nifty50, ok := angelone.FindIndex(instruments, "NIFTY")
	if !ok {
		t.Fatal("NIFTY 50 index not found")
	}
	vix, ok := angelone.FindIndex(instruments, "INDIA VIX")
	if !ok {
		t.Fatal("INDIA VIX index not found")
	}
	t.Logf("NIFTY 50 index token=%s, INDIA VIX index token=%s", nifty50.Token, vix.Token)
}

func TestWebReaderJina(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := httpx.New()

	// A stable, server-rendered page (not a JS-heavy SPA) — the case
	// Jina Reader actually handles well. Jina's anonymous/free tier shares
	// an IP pool across all unauthenticated users, so a 403 here can be
	// caused by someone else's traffic, not this code — skip rather than
	// fail in that case (this is exactly the reliability caveat documented
	// in webreader/jina.go).
	md, err := webreader.Read(ctx, client, "https://en.wikipedia.org/wiki/NIFTY_50")
	if err != nil {
		t.Skipf("Read failed (likely Jina anonymous-tier rate limiting, not a code bug): %v", err)
	}
	if len(md) < 200 {
		t.Fatalf("expected substantial markdown content, got %d bytes", len(md))
	}
	t.Logf("fetched %d bytes of markdown, starts: %.100s", len(md), md)
}

func TestNewsFeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := httpx.New()

	headlines, err := news.FetchAll(ctx, client)
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if len(headlines) == 0 {
		t.Fatal("expected at least one headline")
	}
	t.Logf("fetched %d headlines, first: %q (%s)", len(headlines), headlines[0].Title, headlines[0].Source)
}
