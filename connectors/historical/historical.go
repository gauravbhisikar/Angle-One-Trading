// Package historical maintains a local, incrementally-updated daily OHLCV
// history — the data a backtest connector needs, distinct from the live
// price connectors (yahoo, angelone) which only care about the current
// candle. V1 scope matches the engine: NIFTYBEES only, daily bars, 5
// years by default, Yahoo Finance as the source (free, confirmed working
// — see connectors/README.md).
//
// Stored in the same store.Store cache (source "historical_candles"),
// keyed by symbol, one row per trading day. Save() upserts on (source,
// key, date), so Refresh() re-fetching a date range that overlaps
// already-stored days is a no-op for those days, not a duplicate write —
// this is what makes "update automatically, don't re-download everything"
// free rather than requiring separate diff logic.
package historical

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"connectors/store"
	"connectors/yahoo"
)

const Source = "historical_candles"

// DefaultSymbol is this build's only supported backtest instrument,
// matching the engine's current NIFTYBEES-only scope.
const DefaultSymbol = "NIFTYBEES"

// DefaultYahooTicker maps DefaultSymbol to Yahoo's ticker convention.
const DefaultYahooTicker = yahoo.SymbolNiftyBees

type Candle struct {
	Date   time.Time
	Open   decimal.Decimal
	High   decimal.Decimal
	Low    decimal.Decimal
	Close  decimal.Decimal
	Volume int64
}

// candleJSON is Candle's on-disk shape — store.Store's value column is a
// single string, so OHLCV gets JSON-encoded into it.
type candleJSON struct {
	O string `json:"o"`
	H string `json:"h"`
	L string `json:"l"`
	C string `json:"c"`
	V int64  `json:"v"`
}

func toCandleJSON(c Candle) candleJSON {
	return candleJSON{O: c.Open.String(), H: c.High.String(), L: c.Low.String(), C: c.Close.String(), V: c.Volume}
}

func fromSnapshot(snap store.Snapshot) (Candle, error) {
	var cj candleJSON
	if err := json.Unmarshal([]byte(snap.Value), &cj); err != nil {
		return Candle{}, fmt.Errorf("historical: decode candle for %s: %w", snap.Date, err)
	}
	date, _ := time.Parse("2006-01-02", snap.Date)
	return Candle{
		Date: date, Open: parseDec(cj.O), High: parseDec(cj.H), Low: parseDec(cj.L),
		Close: parseDec(cj.C), Volume: cj.V,
	}, nil
}

func parseDec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

// SyncHistory does the initial full pull: `years` of daily history
// (5 by default — pass 5) for symbol, fully replacing/upserting whatever
// is already cached. Call this once per symbol; use Refresh afterward.
func SyncHistory(ctx context.Context, client *http.Client, st *store.Store, symbol, yahooTicker string, years int) (int, error) {
	candles, _, err := yahoo.FetchCandles(ctx, client, yahooTicker, "1d", fmt.Sprintf("%dy", years))
	if err != nil {
		return 0, fmt.Errorf("historical: sync %s: %w", symbol, err)
	}
	return saveAll(ctx, st, symbol, candles)
}

// Refresh fetches only the days since the last cached candle (falls back
// to a 30-day pull if nothing is cached yet) and upserts — cheap, safe to
// call after every market close on a schedule.
func Refresh(ctx context.Context, client *http.Client, st *store.Store, symbol, yahooTicker string) (int, error) {
	rangeStr := "1mo"
	if latest, err := LatestCandle(ctx, st, symbol); err == nil {
		// Yahoo's range param only accepts a fixed enum (1d,5d,1mo,3mo,6mo,
		// 1y,...), not arbitrary "Nd" — map the gap to the smallest enum
		// value that covers it (with overlap, harmless since Save upserts).
		daysSince := int(time.Since(latest.Date).Hours() / 24)
		switch {
		case daysSince <= 5:
			rangeStr = "5d"
		case daysSince <= 30:
			rangeStr = "1mo"
		case daysSince <= 90:
			rangeStr = "3mo"
		case daysSince <= 180:
			rangeStr = "6mo"
		default:
			rangeStr = "1y"
		}
	}
	candles, _, err := yahoo.FetchCandles(ctx, client, yahooTicker, "1d", rangeStr)
	if err != nil {
		return 0, fmt.Errorf("historical: refresh %s: %w", symbol, err)
	}
	return saveAll(ctx, st, symbol, candles)
}

func saveAll(ctx context.Context, st *store.Store, symbol string, candles []yahoo.Candle) (int, error) {
	count := 0
	for _, c := range candles {
		cj := toCandleJSON(Candle{Date: c.Time, Open: c.Open, High: c.High, Low: c.Low, Close: c.Close, Volume: c.Volume})
		raw, err := json.Marshal(cj)
		if err != nil {
			continue
		}
		if err := st.Save(ctx, Source, symbol, c.Time.Format("2006-01-02"), string(raw)); err != nil {
			return count, fmt.Errorf("historical: save %s %s: %w", symbol, c.Time.Format("2006-01-02"), err)
		}
		count++
	}
	return count, nil
}

// GetHistory returns cached candles for symbol between start and end
// (inclusive), sorted oldest first — exactly what a backtest driver feeds
// into the engine.
func GetHistory(ctx context.Context, st *store.Store, symbol string, start, end time.Time) ([]Candle, error) {
	snaps, err := st.History(ctx, Source, symbol, start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	out := make([]Candle, 0, len(snaps))
	for _, snap := range snaps {
		c, err := fromSnapshot(snap)
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// LatestCandle returns the most recently cached day for symbol.
func LatestCandle(ctx context.Context, st *store.Store, symbol string) (Candle, error) {
	snap, err := st.Latest(ctx, Source, symbol)
	if err != nil {
		return Candle{}, fmt.Errorf("historical: no cached history for %s yet — run SyncHistory first: %w", symbol, err)
	}
	return fromSnapshot(snap)
}
