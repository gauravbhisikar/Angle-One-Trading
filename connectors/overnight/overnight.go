// Package overnight gives a single "what's the overnight/pre-market
// signal for NIFTY" answer, cascading through providers by preference
// instead of hardcoding "GIFT Nifty unsupported." No free GIFT Nifty
// source exists today (checked: Angel One's scrip master has zero
// NSE-IX instruments; free web scraping attempts failed — see
// connectors/README.md), but the slot for it is real and pluggable —
// bring a GiftProvider (e.g. once you have Upstox creds, or a working
// scraper) and it becomes the top of the cascade with zero changes
// anywhere else.
package overnight

import (
	"context"
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"connectors/angelone"
	"connectors/yahoo"
)

type SourceName string

const (
	SourceGIFTNifty     SourceName = "gift_nifty"
	SourceSGXHistorical SourceName = "sgx_historical" // discontinued exchange-side in 2023 — always skipped, kept as a named rung for clarity
	SourceNiftyFutures  SourceName = "nifty_futures_basis"
	SourceUSMarkets     SourceName = "us_markets_usdinr"
	SourceNone          SourceName = "none"
)

// GiftProvider is the pluggable slot for a future free/paid GIFT Nifty
// source. Return an error (or pass a nil provider) when unavailable —
// the cascade falls through automatically.
type GiftProvider func(ctx context.Context) (decimal.Decimal, error)

type Signal struct {
	Source     SourceName
	ChangePct  decimal.Decimal // indicative % move, sign matters (positive = bullish overnight cue)
	Confidence float64         // 0-1, see package doc: how much this rung is actually trusted
	Notes      string
	Detail     map[string]decimal.Decimal // raw components that fed the signal, for audit/debugging
}

// Fetch walks the cascade in priority order and returns the first rung
// that produces data: GIFT Nifty -> SGX (always skipped, discontinued)
// -> NIFTY futures basis -> US markets + USD/INR composite -> none.
// angel may be nil (skips the futures rung); gift may be nil (skips
// straight past it, the honest default today).
func Fetch(ctx context.Context, client *http.Client, gift GiftProvider, angel *angelone.Client, instruments []angelone.Instrument, futuresExpiry, nifty50Token string) (Signal, error) {
	if gift != nil {
		if pct, err := gift(ctx); err == nil {
			return Signal{Source: SourceGIFTNifty, ChangePct: pct, Confidence: 0.95,
				Notes: "direct GIFT Nifty overnight indicator"}, nil
		}
	}
	// SGX Nifty was discontinued at the exchange level in 2023 (moved to
	// GIFT City) — this rung is permanently a no-op, kept named so the
	// cascade order stays self-documenting rather than silently skipping it.

	if angel != nil && instruments != nil && futuresExpiry != "" && nifty50Token != "" {
		fd, err := angel.FetchNiftyFutures(ctx, instruments, futuresExpiry, nifty50Token)
		if err == nil {
			return Signal{
				Source: SourceNiftyFutures, ChangePct: fd.BasisPct, Confidence: 0.7,
				Notes:  fmt.Sprintf("NIFTY futures %s premium/discount to spot vs a real overnight indicator", fd.Expiry),
				Detail: map[string]decimal.Decimal{"spot": fd.SpotPrice, "future": fd.FuturePrice, "basis": fd.Basis},
			}, nil
		}
	}

	cues, err := yahoo.FetchGlobalCues(ctx, client)
	if err == nil {
		// Simple composite: Dow + Nasdaq average change, USD/INR move
		// flipped in sign (a weaker rupee is a headwind for Nifty, so
		// invert it before averaging into the same "bullish/bearish" scale).
		composite := cues.DowJones.ChangePct().Add(cues.Nasdaq.ChangePct()).
			Sub(cues.USDINR.ChangePct()).Div(decimal.NewFromInt(3))
		return Signal{
			Source: SourceUSMarkets, ChangePct: composite, Confidence: 0.5,
			Notes: "indirect proxy: US market close + USD/INR move, not a direct Indian pre-market indicator",
			Detail: map[string]decimal.Decimal{
				"dow_pct": cues.DowJones.ChangePct(), "nasdaq_pct": cues.Nasdaq.ChangePct(),
				"usdinr_pct": cues.USDINR.ChangePct(), "crude_pct": cues.CrudeWTI.ChangePct(),
			},
		}, nil
	}

	return Signal{Source: SourceNone, Confidence: 0, Notes: "every rung failed: " + err.Error()}, nil
}
