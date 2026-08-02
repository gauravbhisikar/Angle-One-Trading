package featurestore_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/featurestore"
)

type rawCandle struct {
	Time   string  `json:"time"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume int64   `json:"volume"`
}

func loadRealCandles(t *testing.T) []featurestore.Candle {
	t.Helper()
	path := filepath.Join("..", "api", "web", "nifty_history.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("bundled real candle snapshot not found at %s: %v", path, err)
	}
	var raw []rawCandle
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode nifty_history.json: %v", err)
	}
	out := make([]featurestore.Candle, 0, len(raw))
	for _, r := range raw {
		ts, err := time.Parse(time.RFC3339, r.Time)
		if err != nil {
			continue
		}
		out = append(out, featurestore.Candle{
			Date: ts, Open: decimal.NewFromFloat(r.Open), High: decimal.NewFromFloat(r.High),
			Low: decimal.NewFromFloat(r.Low), Close: decimal.NewFromFloat(r.Close), Volume: r.Volume,
		})
	}
	return out
}

func TestComputeTechnicalOnRealFiveYearData(t *testing.T) {
	candles := loadRealCandles(t)
	if len(candles) < 500 {
		t.Fatalf("expected several hundred+ real candles, got %d", len(candles))
	}

	rows, err := featurestore.ComputeTechnical("NIFTYBEES", candles)
	if err != nil {
		t.Fatalf("ComputeTechnical: %v", err)
	}
	if len(rows) != len(candles) {
		t.Fatalf("expected one feature row per candle, got %d rows for %d candles", len(rows), len(candles))
	}

	last := rows[len(rows)-1]
	t.Logf("latest (%s): close=%s rsi14=%s ema20=%s ema50=%s adx14=%s atr14=%s bb%%b=%s",
		last.Date, last.Close, last.RSI14, last.EMA20, last.EMA50, last.ADX14, last.ATR14, last.BollingerPercentB)

	if last.RSI14.LessThan(decimal.Zero) || last.RSI14.GreaterThan(decimal.NewFromInt(100)) {
		t.Fatalf("RSI out of valid 0-100 range: %s", last.RSI14)
	}
	if last.EMA20.IsZero() || last.EMA50.IsZero() {
		t.Fatal("expected non-zero EMAs after 5 years of warmup")
	}

	// At least one bullish and one bearish EMA cross should have fired
	// somewhere across 5 real years — sanity check the flags aren't
	// silently always nil.
	sawBullish, sawBearish := false, false
	for _, r := range rows {
		if r.EMACrossBullish != nil && *r.EMACrossBullish {
			sawBullish = true
		}
		if r.EMACrossBearish != nil && *r.EMACrossBearish {
			sawBearish = true
		}
	}
	if !sawBullish || !sawBearish {
		t.Fatalf("expected both bullish and bearish EMA crosses over 5 years, bullish=%v bearish=%v", sawBullish, sawBearish)
	}
}

func TestStoreSaveAndQuery(t *testing.T) {
	candles := loadRealCandles(t)
	if len(candles) < 100 {
		t.Skip("not enough real candles for this test")
	}
	rows, err := featurestore.ComputeTechnical("NIFTYBEES", candles)
	if err != nil {
		t.Fatalf("ComputeTechnical: %v", err)
	}

	ctx := context.Background()
	store, err := featurestore.Open(ctx, filepath.Join(t.TempDir(), "features.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.SaveRows(ctx, rows); err != nil {
		t.Fatalf("SaveRows: %v", err)
	}
	// Idempotent re-save (e.g. after Refresh-ing overlapping historical
	// data) must upsert, not error or duplicate.
	if err := store.SaveRows(ctx, rows[len(rows)-10:]); err != nil {
		t.Fatalf("SaveRows (overlap re-save): %v", err)
	}

	last := rows[len(rows)-1]
	got, err := store.Get(ctx, "NIFTYBEES", last.Date)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.RSI14.Equal(last.RSI14) {
		t.Fatalf("round-trip mismatch: computed RSI %s, stored/read RSI %s", last.RSI14, got.RSI14)
	}

	// Macro overlay — sparse, upserted independently of the technical columns.
	if err := store.UpsertMacro(ctx, featurestore.MacroSnapshot{
		Symbol: "NIFTYBEES", Date: last.Date, VIX: "12.4", FIINet: "2450000000", BreadthADRatio: "2.94",
	}); err != nil {
		t.Fatalf("UpsertMacro: %v", err)
	}
	got2, err := store.Get(ctx, "NIFTYBEES", last.Date)
	if err != nil {
		t.Fatalf("Get after macro upsert: %v", err)
	}
	if got2.VIX != "12.4" {
		t.Fatalf("expected macro VIX to merge into the same row, got %+v", got2)
	}
	if !got2.RSI14.Equal(last.RSI14) {
		t.Fatal("macro upsert must not clobber the technical columns already saved")
	}

	rangeRows, err := store.Query(ctx, "NIFTYBEES", rows[0].Date, last.Date)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rangeRows) != len(rows) {
		t.Fatalf("expected %d rows in full range query, got %d", len(rows), len(rangeRows))
	}
}
