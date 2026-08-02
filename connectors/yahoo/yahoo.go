// Package yahoo pulls free, no-auth OHLCV and quote data from Yahoo
// Finance's public (unofficial, but long-stable and widely used) chart
// API. Confirmed reachable and working: NIFTYBEES.NS, ^NSEI (Nifty 50),
// ^INDIAVIX, ^DJI, ^IXIC, CL=F (crude), INR=X (USD/INR). Best used as a
// cross-check against Angel One's own candles, and as the primary source
// for global-cues data Angel One doesn't carry (Dow, Nasdaq, crude, FX).
package yahoo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/shopspring/decimal"

	"connectors/httpx"
)

const baseURL = "https://query1.finance.yahoo.com/v8/finance/chart/"

// Symbols confirmed working against this API as of this build.
const (
	SymbolNiftyBees = "NIFTYBEES.NS"
	SymbolNifty50   = "%5ENSEI" // ^NSEI, URL-encoded
	SymbolIndiaVIX  = "%5EINDIAVIX"
	SymbolDowJones  = "%5EDJI"
	SymbolNasdaq    = "%5EIXIC"
	SymbolCrudeWTI  = "CL=F"
	SymbolUSDINR    = "INR=X"
)

type Candle struct {
	Time   time.Time
	Open   decimal.Decimal
	High   decimal.Decimal
	Low    decimal.Decimal
	Close  decimal.Decimal
	Volume int64
}

type Quote struct {
	Symbol        string
	Currency      string
	Price         decimal.Decimal
	PreviousClose decimal.Decimal
	FiftyTwoHigh  decimal.Decimal
	FiftyTwoLow   decimal.Decimal
	MarketTime    time.Time
}

// ChangePct is the simple % move from the prior close to Price — what
// most "global cues" signals (Dow up 0.4%, crude down 1.2%) actually mean.
func (q Quote) ChangePct() decimal.Decimal {
	if q.PreviousClose.IsZero() {
		return decimal.Zero
	}
	return q.Price.Sub(q.PreviousClose).Div(q.PreviousClose).Mul(decimal.NewFromInt(100))
}

type chartResponse struct {
	Chart struct {
		Result []struct {
			Meta struct {
				Currency           string  `json:"currency"`
				Symbol             string  `json:"symbol"`
				RegularMarketPrice float64 `json:"regularMarketPrice"`
				ChartPreviousClose float64 `json:"chartPreviousClose"`
				FiftyTwoWeekHigh   float64 `json:"fiftyTwoWeekHigh"`
				FiftyTwoWeekLow    float64 `json:"fiftyTwoWeekLow"`
				RegularMarketTime  int64   `json:"regularMarketTime"`
			} `json:"meta"`
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Open   []*float64 `json:"open"`
					High   []*float64 `json:"high"`
					Low    []*float64 `json:"low"`
					Close  []*float64 `json:"close"`
					Volume []*int64   `json:"volume"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
		Error interface{} `json:"error"`
	} `json:"chart"`
}

// FetchCandles returns historical OHLCV for a Yahoo ticker symbol.
// interval: "1m","5m","15m","1d" etc (Yahoo's intraday history is capped
// to the last ~60 days regardless of interval). rangeStr: "5d","1mo","1y".
func FetchCandles(ctx context.Context, client *http.Client, symbol, interval, rangeStr string) ([]Candle, Quote, error) {
	u := fmt.Sprintf("%s%s?interval=%s&range=%s", baseURL, symbol, url.QueryEscape(interval), url.QueryEscape(rangeStr))
	body, err := httpx.Get(ctx, client, u, nil)
	if err != nil {
		return nil, Quote{}, err
	}

	var parsed chartResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, Quote{}, fmt.Errorf("yahoo: decode %s: %w", symbol, err)
	}
	if len(parsed.Chart.Result) == 0 {
		return nil, Quote{}, fmt.Errorf("yahoo: no data for %s (symbol wrong or delisted?)", symbol)
	}
	r := parsed.Chart.Result[0]

	quote := Quote{
		Symbol: r.Meta.Symbol, Currency: r.Meta.Currency,
		Price:         decimal.NewFromFloat(r.Meta.RegularMarketPrice),
		PreviousClose: decimal.NewFromFloat(r.Meta.ChartPreviousClose),
		FiftyTwoHigh:  decimal.NewFromFloat(r.Meta.FiftyTwoWeekHigh),
		FiftyTwoLow:   decimal.NewFromFloat(r.Meta.FiftyTwoWeekLow),
		MarketTime:    time.Unix(r.Meta.RegularMarketTime, 0),
	}

	if len(r.Indicators.Quote) == 0 {
		return nil, quote, nil // meta-only response (can happen for range=1d on some tickers)
	}
	q := r.Indicators.Quote[0]
	candles := make([]Candle, 0, len(r.Timestamp))
	for i, ts := range r.Timestamp {
		if i >= len(q.Close) || q.Close[i] == nil {
			continue // Yahoo pads gaps (holidays/pre-market) with nulls
		}
		c := Candle{Time: time.Unix(ts, 0)}
		if i < len(q.Open) && q.Open[i] != nil {
			c.Open = decimal.NewFromFloat(*q.Open[i])
		}
		if i < len(q.High) && q.High[i] != nil {
			c.High = decimal.NewFromFloat(*q.High[i])
		}
		if i < len(q.Low) && q.Low[i] != nil {
			c.Low = decimal.NewFromFloat(*q.Low[i])
		}
		c.Close = decimal.NewFromFloat(*q.Close[i])
		if i < len(q.Volume) && q.Volume[i] != nil {
			c.Volume = *q.Volume[i]
		}
		candles = append(candles, c)
	}
	return candles, quote, nil
}

func FetchQuote(ctx context.Context, client *http.Client, symbol string) (Quote, error) {
	_, quote, err := FetchCandles(ctx, client, symbol, "1d", "1d")
	return quote, err
}

// GlobalCues bundles the overnight/macro data points a single-symbol
// (NIFTYBEES) strategy generator would otherwise be blind to.
type GlobalCues struct {
	DowJones Quote
	Nasdaq   Quote
	CrudeWTI Quote
	USDINR   Quote
}

func FetchGlobalCues(ctx context.Context, client *http.Client) (GlobalCues, error) {
	var cues GlobalCues
	var err error
	if cues.DowJones, err = FetchQuote(ctx, client, SymbolDowJones); err != nil {
		return cues, fmt.Errorf("dow: %w", err)
	}
	if cues.Nasdaq, err = FetchQuote(ctx, client, SymbolNasdaq); err != nil {
		return cues, fmt.Errorf("nasdaq: %w", err)
	}
	if cues.CrudeWTI, err = FetchQuote(ctx, client, SymbolCrudeWTI); err != nil {
		return cues, fmt.Errorf("crude: %w", err)
	}
	if cues.USDINR, err = FetchQuote(ctx, client, SymbolUSDINR); err != nil {
		return cues, fmt.Errorf("usdinr: %w", err)
	}
	return cues, nil
}
