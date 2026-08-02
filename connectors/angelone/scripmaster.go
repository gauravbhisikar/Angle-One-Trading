// Package angelone gets the maximum possible data straight from the
// broker: it's free, requires no scraping, and — for price data — matches
// exactly what the trading engine itself executes against.
package angelone

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"connectors/httpx"
)

// scripMasterURL is Angel One's full instrument master — free, no API
// key, no login required. ~35MB JSON, every tradable instrument across
// every exchange segment. Confirmed live schema (2026-08):
//
//	{"token":"10576","symbol":"NIFTYBEES-EQ","name":"NIFTYBEES","expiry":"",
//	 "strike":"-1.000000","lotsize":"1","instrumenttype":"","exch_seg":"NSE",
//	 "tick_size":"1.000000", ...}
//	{"token":"40921","symbol":"NIFTY11AUG2622350CE","name":"NIFTY",
//	 "expiry":"11AUG2026","strike":"2235000.000000","lotsize":"65",
//	 "instrumenttype":"OPTIDX","exch_seg":"NFO","tick_size":"5.000000", ...}
//
// strike and tick_size are stored x100 (paise-scaled) — divide by 100 for
// the real rupee value. This single free file is the Instrument Master
// ENGINE_SPEC Sec 8 calls for: lot size, tick size, expiry, all in one
// place, for every symbol including NIFTYBEES itself.
const scripMasterURL = "https://margincalculator.angelbroking.com/OpenAPI_File/files/OpenAPIScripMaster.json"

type Instrument struct {
	Token          string
	Symbol         string
	Name           string
	Expiry         string // DDMMMYYYY, e.g. "11AUG2026"; empty for cash instruments
	Strike         decimal.Decimal
	LotSize        int
	InstrumentType string // "" = equity/ETF, "OPTIDX"/"OPTSTK" = options, "FUTIDX"/"FUTSTK" = futures
	Exchange       string // NSE, NFO, BSE, ...
	TickSize       decimal.Decimal
}

type rawInstrument struct {
	Token          string `json:"token"`
	Symbol         string `json:"symbol"`
	Name           string `json:"name"`
	Expiry         string `json:"expiry"`
	Strike         string `json:"strike"`
	LotSize        string `json:"lotsize"`
	InstrumentType string `json:"instrumenttype"`
	ExchSeg        string `json:"exch_seg"`
	TickSize       string `json:"tick_size"`
}

// FetchScripMaster downloads and parses the full instrument list. It's a
// large (~35MB) file — callers building a long-running service should
// cache the result and refresh once daily (ENGINE_SPEC Sec 8: "refreshed
// every morning"), not re-fetch per request.
func FetchScripMaster(ctx context.Context, client *http.Client) ([]Instrument, error) {
	body, err := httpx.Get(ctx, client, scripMasterURL, nil)
	if err != nil {
		return nil, fmt.Errorf("angelone: fetch scrip master: %w", err)
	}

	var raw []rawInstrument
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("angelone: decode scrip master: %w", err)
	}

	out := make([]Instrument, 0, len(raw))
	for _, r := range raw {
		strike := scaledDecimal(r.Strike)
		tick := scaledDecimal(r.TickSize)
		lot, _ := strconv.Atoi(r.LotSize)
		out = append(out, Instrument{
			Token: r.Token, Symbol: r.Symbol, Name: r.Name, Expiry: r.Expiry,
			Strike: strike, LotSize: lot, InstrumentType: r.InstrumentType,
			Exchange: r.ExchSeg, TickSize: tick,
		})
	}
	return out, nil
}

// scaledDecimal divides Angel One's paise-scaled string fields (strike,
// tick_size) by 100 to get the real rupee value.
func scaledDecimal(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d.Div(decimal.NewFromInt(100))
}

// FindEquity looks up a cash-market symbol (e.g. "NIFTYBEES" -> the
// "NIFTYBEES-EQ" row). Exact match on Name, exchange NSE.
func FindEquity(instruments []Instrument, name string) (Instrument, bool) {
	for _, i := range instruments {
		if i.Exchange == "NSE" && i.InstrumentType == "" && strings.EqualFold(i.Name, name) {
			return i, true
		}
	}
	return Instrument{}, false
}

// FindIndex looks up an index instrument (NIFTY 50, INDIA VIX, BANKNIFTY,
// ...) — these are instrumenttype "AMXIDX" in the scrip master, distinct
// from equities/ETFs (instrumenttype ""). Angel One's own historical
// candle API can serve these tokens exactly like any equity.
func FindIndex(instruments []Instrument, name string) (Instrument, bool) {
	for _, i := range instruments {
		if i.Exchange == "NSE" && i.InstrumentType == "AMXIDX" && strings.EqualFold(i.Name, name) {
			return i, true
		}
	}
	return Instrument{}, false
}

// FindOptionsByExpiry returns every NIFTY index option (CE and PE, every
// strike) for one expiry — the raw material for an option-chain / PCR /
// max-pain connector. expiry must match Angel One's exact format,
// e.g. "11AUG2026" (list expiries first via ListNiftyExpiries).
func FindOptionsByExpiry(instruments []Instrument, expiry string) []Instrument {
	var out []Instrument
	for _, i := range instruments {
		if i.Exchange == "NFO" && i.InstrumentType == "OPTIDX" && i.Name == "NIFTY" && i.Expiry == expiry {
			out = append(out, i)
		}
	}
	return out
}

// ListNiftyExpiries returns every distinct NIFTY option expiry currently
// in the scrip master, nearest first is not guaranteed — sort by parsing
// the DDMMMYYYY format if chronological order matters.
func ListNiftyExpiries(instruments []Instrument) []string {
	seen := map[string]bool{}
	var out []string
	for _, i := range instruments {
		if i.Exchange == "NFO" && i.InstrumentType == "OPTIDX" && i.Name == "NIFTY" && i.Expiry != "" {
			if !seen[i.Expiry] {
				seen[i.Expiry] = true
				out = append(out, i.Expiry)
			}
		}
	}
	return out
}

// FindFuturesByExpiry returns the NIFTY index future for one expiry.
func FindFuturesByExpiry(instruments []Instrument, expiry string) (Instrument, bool) {
	for _, i := range instruments {
		if i.Exchange == "NFO" && i.InstrumentType == "FUTIDX" && i.Name == "NIFTY" && i.Expiry == expiry {
			return i, true
		}
	}
	return Instrument{}, false
}
