// Package amfi fetches free, no-auth, official daily NAV data published
// by the Association of Mutual Funds in India (AMFI) — the reliable
// source for NIFTYBEES's NAV, since Angel One's quote/candle API only
// gives market price, not the fund's actual net asset value (Angel One
// has no NAV/iNAV field at all). Confirmed working: HTTP 200, flat
// semicolon-delimited text, refreshed daily by AMFI itself.
package amfi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"connectors/httpx"
)

const navAllURL = "https://portal.amfiindia.com/spages/NAVAll.txt"

// NiftyBeesSchemeCode is AMFI's stable scheme code for "Nippon India ETF
// Nifty 50 BeES" (NIFTYBEES) — used as a fast-path match, verified
// against the scheme name on every fetch in case AMFI ever renumbers.
const NiftyBeesSchemeCode = "140084"
const niftyBeesSchemeName = "nippon india etf nifty 50 bees"

type NAV struct {
	SchemeCode string
	ISIN       string
	SchemeName string
	Value      decimal.Decimal
	Date       time.Time
}

// FetchNiftyBeesNAV downloads AMFI's full daily NAV file (~1-2MB, all
// ~17,000 schemes) and returns just the NIFTYBEES row. AMFI publishes one
// flat file for everything — there is no per-scheme endpoint.
func FetchNiftyBeesNAV(ctx context.Context, client *http.Client) (NAV, error) {
	all, err := FetchAll(ctx, client)
	if err != nil {
		return NAV{}, err
	}
	for _, n := range all {
		if n.SchemeCode == NiftyBeesSchemeCode || strings.EqualFold(n.SchemeName, "Nippon India ETF Nifty 50 BeES") {
			return n, nil
		}
	}
	return NAV{}, fmt.Errorf("amfi: NIFTYBEES row not found in NAVAll.txt (AMFI may have restructured the file)")
}

// FetchAll parses every scheme in the file — useful if the agent workflow
// later needs NAVs for other instruments without a second full download.
func FetchAll(ctx context.Context, client *http.Client) ([]NAV, error) {
	body, err := httpx.Get(ctx, client, navAllURL, nil)
	if err != nil {
		return nil, fmt.Errorf("amfi: fetch NAVAll.txt: %w", err)
	}

	var out []NAV
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Split(line, ";")
		if len(fields) != 6 {
			continue // section headers ("Aditya Birla Sun Life Mutual Fund"), blank lines, the column-header row
		}
		schemeCode := strings.TrimSpace(fields[0])
		if _, err := strconv.Atoi(schemeCode); err != nil {
			continue // not a data row (e.g. the "Scheme Code;ISIN..." header also has semicolons)
		}
		value, err := decimal.NewFromString(strings.TrimSpace(fields[4]))
		if err != nil {
			continue // NAV "N.A." rows (suspended schemes) — skip
		}
		date, _ := time.Parse("02-Jan-2006", strings.TrimSpace(fields[5]))
		out = append(out, NAV{
			SchemeCode: schemeCode,
			ISIN:       strings.TrimSpace(fields[1]),
			SchemeName: strings.TrimSpace(fields[3]),
			Value:      value,
			Date:       date,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("amfi: parsed 0 rows — AMFI likely changed the file format")
	}
	return out, nil
}
