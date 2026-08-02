// Package nse covers the handful of data points genuinely not available
// anywhere else for free: FII/DII daily flow, the NSE trading holiday
// calendar, and NIFTYBEES corporate actions (dividends).
//
// Reliability note (read before wiring this into anything automated):
// these are nseindia.com's own frontend API endpoints (the same ones
// nseindia.com's website calls, not a public documented API) — they
// require a warm-up GET to pick up session cookies (WarmUpNSE), and NSE
// is known to rate-limit or outright 403 requests from datacenter/cloud
// IPs regardless. Verified from this build environment: nseindia.com
// itself returned HTTP 403 even with browser headers. Expect this to work
// from a normal residential/office connection and to need retry/backoff
// or a proxy from a cloud server. Treat every function here as
// best-effort with a manual-refresh fallback, not a guaranteed feed.
package nse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"connectors/httpx"
)

const (
	holidaysURL          = "https://www.nseindia.com/api/holiday-master?type=trading"
	corporateActionsURL  = "https://www.nseindia.com/api/corporates-corporateActions?index=equities&symbol="
	corporateAnnounceURL = "https://www.nseindia.com/api/corporate-announcements?index=equities&symbol="
	fiiDiiURL            = "https://www.nseindia.com/api/fiidiiTradeReact"
	marketBreadthURL     = "https://www.nseindia.com/api/market-data-pre-open?key=ALL"
)

func nseHeaders() map[string]string {
	return map[string]string{
		"Referer": "https://www.nseindia.com/",
	}
}

// fetchWithWarmup does the cookie warm-up dance and then the real request,
// on the same client (which must have a cookie jar — httpx.New() does).
func fetchWithWarmup(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	if err := httpx.WarmUpNSE(ctx, client); err != nil {
		return nil, fmt.Errorf("nse: warm-up failed (likely blocked from this IP): %w", err)
	}
	return httpx.Get(ctx, client, url, nseHeaders())
}

type Holiday struct {
	Date        time.Time
	Description string
}

// FetchHolidays returns this year's NSE trading holiday calendar
// (ENGINE_SPEC Sec 7). The response shape groups holidays by segment
// ("CM", "FO", ...); this pulls the CM (cash market) list, which is what
// a NIFTYBEES-only strategy cares about.
func FetchHolidays(ctx context.Context, client *http.Client) ([]Holiday, error) {
	body, err := fetchWithWarmup(ctx, client, holidaysURL)
	if err != nil {
		return nil, err
	}

	var parsed map[string][]struct {
		TradingDate string `json:"tradingDate"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("nse: decode holiday-master: %w", err)
	}

	rows, ok := parsed["CM"]
	if !ok {
		return nil, fmt.Errorf("nse: no \"CM\" segment in holiday-master response (NSE may have changed the shape)")
	}
	out := make([]Holiday, 0, len(rows))
	for _, r := range rows {
		d, _ := time.Parse("02-Jan-2006", r.TradingDate)
		out = append(out, Holiday{Date: d, Description: r.Description})
	}
	return out, nil
}

type CorporateAction struct {
	Symbol     string
	Series     string
	Purpose    string // e.g. "Dividend - Rs 1.50 Per Share"
	ExDate     time.Time
	RecordDate time.Time
}

// FetchCorporateActions returns NIFTYBEES's dividend/corporate-action
// history — NAV data alone (AMFI) doesn't tell you dividend dates, only
// the resulting NAV drop.
func FetchCorporateActions(ctx context.Context, client *http.Client, symbol string) ([]CorporateAction, error) {
	body, err := fetchWithWarmup(ctx, client, corporateActionsURL+symbol)
	if err != nil {
		return nil, err
	}

	var rows []struct {
		Symbol     string `json:"symbol"`
		Series     string `json:"series"`
		Purpose    string `json:"subject"` // confirmed live field name is "subject", not "purpose"
		ExDate     string `json:"exDate"`
		RecordDate string `json:"recDate"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("nse: decode corporate-actions: %w", err)
	}
	out := make([]CorporateAction, 0, len(rows))
	for _, r := range rows {
		ex, _ := time.Parse("02-Jan-2006", r.ExDate)
		rec, _ := time.Parse("02-Jan-2006", r.RecordDate)
		out = append(out, CorporateAction{Symbol: r.Symbol, Series: r.Series, Purpose: r.Purpose, ExDate: ex, RecordDate: rec})
	}
	return out, nil
}

type Announcement struct {
	Symbol      string
	Subject     string
	Description string
	Date        time.Time
}

// FetchAnnouncements covers the broader category corporate actions
// (dividends only) doesn't: board meetings, earnings, mergers, trading
// window closures, general disclosures — the full NSE corporate
// announcements feed for one symbol.
func FetchAnnouncements(ctx context.Context, client *http.Client, symbol string) ([]Announcement, error) {
	body, err := fetchWithWarmup(ctx, client, corporateAnnounceURL+symbol)
	if err != nil {
		return nil, err
	}

	// Confirmed live field names (2026-08-02): there is no "subject" field
	// at all — "desc" is the announcement category (e.g. "Other
	// Restructuring") and "attchmntText" is the actual detail text. An
	// earlier version of this mapped Subject from a "subject" key that
	// doesn't exist, which would have always come back empty.
	var rows []struct {
		Symbol   string `json:"symbol"`
		Desc     string `json:"desc"`
		AttchTxt string `json:"attchmntText"`
		BcastDT  string `json:"an_dt"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("nse: decode corporate-announcements: %w", err)
	}
	out := make([]Announcement, 0, len(rows))
	for _, r := range rows {
		d, _ := time.Parse("02-Jan-2006 15:04:05", r.BcastDT)
		out = append(out, Announcement{Symbol: r.Symbol, Subject: r.Desc, Description: r.AttchTxt, Date: d})
	}
	return out, nil
}

type MarketBreadth struct {
	Timestamp      time.Time
	Advances       int
	Declines       int
	Unchanged      int
	NewHighs       int             // stocks trading at/near their 52-week high right now
	NewLows        int             // stocks trading at/near their 52-week low right now
	AdvanceDecline decimal.Decimal // advances/declines, >1 = broad-based strength
}

type breadthRow struct {
	Metadata struct {
		LastPrice float64 `json:"lastPrice"`
		YearHigh  float64 `json:"yearHigh"`
		YearLow   float64 `json:"yearLow"`
	} `json:"metadata"`
}

// FetchMarketBreadth pulls NSE's pre-open snapshot across ~2000 listed
// stocks and derives Market Breadth (advance/decline ratio, count of
// stocks near their 52-week high/low) — one of the strongest regime
// indicators, and unlike NSE's other endpoints (holidays, corp actions,
// FII/DII), this one responded 200 in testing even without the cookie
// warm-up, so it may be less aggressively rate-limited. Still routed
// through the same warm-up helper for consistency and safety margin.
func FetchMarketBreadth(ctx context.Context, client *http.Client) (MarketBreadth, error) {
	body, err := fetchWithWarmup(ctx, client, marketBreadthURL)
	if err != nil {
		return MarketBreadth{}, err
	}

	var parsed struct {
		Advances  int          `json:"advances"`
		Declines  int          `json:"declines"`
		Unchanged int          `json:"unchanged"`
		Timestamp string       `json:"timestamp"`
		Data      []breadthRow `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return MarketBreadth{}, fmt.Errorf("nse: decode market-data-pre-open: %w", err)
	}

	newHighs, newLows := 0, 0
	for _, row := range parsed.Data {
		if row.Metadata.YearHigh <= 0 || row.Metadata.YearLow <= 0 {
			continue
		}
		if row.Metadata.LastPrice >= row.Metadata.YearHigh*0.99 {
			newHighs++
		}
		if row.Metadata.LastPrice <= row.Metadata.YearLow*1.01 {
			newLows++
		}
	}

	ts, _ := time.Parse("02-Jan-2006 15:04:05", parsed.Timestamp)
	ad := decimal.Zero
	if parsed.Declines > 0 {
		ad = decimal.NewFromInt(int64(parsed.Advances)).Div(decimal.NewFromInt(int64(parsed.Declines)))
	}

	return MarketBreadth{
		Timestamp: ts, Advances: parsed.Advances, Declines: parsed.Declines, Unchanged: parsed.Unchanged,
		NewHighs: newHighs, NewLows: newLows, AdvanceDecline: ad,
	}, nil
}

type FIIDIIFlow struct {
	Date      time.Time
	Category  string  // "FII/FPI" or "DII"
	BuyValue  float64 // Rs crore
	SellValue float64
	NetValue  float64
}

// FetchFIIDII returns the latest published FII/DII cash-market flow —
// one of the single biggest daily sentiment drivers for Nifty, and one of
// the easiest data points to forget to wire in.
func FetchFIIDII(ctx context.Context, client *http.Client) ([]FIIDIIFlow, error) {
	body, err := fetchWithWarmup(ctx, client, fiiDiiURL)
	if err != nil {
		return nil, err
	}

	var rows []struct {
		Date      string `json:"date"`
		Category  string `json:"category"`
		BuyValue  string `json:"buyValue"`
		SellValue string `json:"sellValue"`
		NetValue  string `json:"netValue"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("nse: decode fiidiiTradeReact: %w", err)
	}
	out := make([]FIIDIIFlow, 0, len(rows))
	for _, r := range rows {
		d, _ := time.Parse("02-Jan-2006", r.Date)
		out = append(out, FIIDIIFlow{
			Date: d, Category: r.Category,
			BuyValue: parseFloat(r.BuyValue), SellValue: parseFloat(r.SellValue), NetValue: parseFloat(r.NetValue),
		})
	}
	return out, nil
}

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}
