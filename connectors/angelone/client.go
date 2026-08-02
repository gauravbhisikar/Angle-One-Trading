package angelone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

const baseURL = "https://apiconnect.angelone.in"

// Client is a slim, standalone Angel One SmartAPI client — deliberately
// duplicated from engine/internal/marketdata/angelone rather than shared,
// so this connectors module has zero dependency on the trading engine and
// can run in a separate future agent process untouched by engine changes.
type Client struct {
	http       *http.Client
	apiKey     string
	clientCode string
	pin        string
	totpSecret string

	mu        sync.RWMutex
	jwtToken  string
	feedToken string
}

func NewClient(apiKey, clientCode, pin, totpSecret string) *Client {
	return &Client{
		http:       &http.Client{Timeout: 20 * time.Second},
		apiKey:     apiKey,
		clientCode: clientCode,
		pin:        pin,
		totpSecret: totpSecret,
	}
}

type loginResponse struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    struct {
		JWTToken  string `json:"jwtToken"`
		FeedToken string `json:"feedToken"`
	} `json:"data"`
}

// Login authenticates via TOTP+PIN. Never called automatically by
// anything in this module — a caller must explicitly invoke it, since it
// touches the user's real broker session.
func (c *Client) Login(ctx context.Context) error {
	totp, err := GenerateTOTP(c.totpSecret)
	if err != nil {
		return err
	}
	body := map[string]string{"clientcode": c.clientCode, "password": c.pin, "totp": totp}

	var resp loginResponse
	if err := c.request(ctx, http.MethodPost, "/rest/auth/angelbroking/user/v1/loginByPassword", body, &resp); err != nil {
		return err
	}
	if !resp.Status {
		return fmt.Errorf("angelone: login failed: %s", resp.Message)
	}
	c.mu.Lock()
	c.jwtToken, c.feedToken = resp.Data.JWTToken, resp.Data.FeedToken
	c.mu.Unlock()
	return nil
}

// QuoteData is one instrument's live snapshot. OpenInterest is only
// populated for F&O instruments. NOTE: "opnInterest" is SmartAPI's
// documented field name for full-mode quotes as of this build — verify
// against current official docs before relying on it, the same caveat as
// the engine's WebSocket binary format (broker fields do shift over time).
type QuoteData struct {
	Token                  string
	TradingSymbol          string
	LTP                    decimal.Decimal
	OpenInterest           int64
	Open, High, Low, Close decimal.Decimal
}

type quoteRequest struct {
	Mode           string              `json:"mode"`
	ExchangeTokens map[string][]string `json:"exchangeTokens"`
}

type quoteResponse struct {
	Status bool `json:"status"`
	Data   struct {
		Fetched []struct {
			ExchType      string  `json:"exchange"`
			TradingSymbol string  `json:"tradingSymbol"`
			SymbolToken   string  `json:"symbolToken"`
			LTP           float64 `json:"ltp"`
			Open          float64 `json:"open"`
			High          float64 `json:"high"`
			Low           float64 `json:"low"`
			Close         float64 `json:"close"`
			OpenInterest  float64 `json:"opnInterest"`
		} `json:"fetched"`
	} `json:"data"`
}

// GetQuotes batch-fetches full-mode quotes (LTP + OI + OHLC) for up to
// ~50 tokens on one exchange segment per call — the mechanism an
// option-chain/PCR/max-pain connector polls repeatedly across strikes.
func (c *Client) GetQuotes(ctx context.Context, exchange string, tokens []string) ([]QuoteData, error) {
	req := quoteRequest{Mode: "FULL", ExchangeTokens: map[string][]string{exchange: tokens}}
	var resp quoteResponse
	if err := c.request(ctx, http.MethodPost, "/rest/secure/angelbroking/market/v1/quote", req, &resp); err != nil {
		return nil, err
	}
	if !resp.Status {
		return nil, fmt.Errorf("angelone: quote request rejected")
	}
	out := make([]QuoteData, 0, len(resp.Data.Fetched))
	for _, f := range resp.Data.Fetched {
		out = append(out, QuoteData{
			Token: f.SymbolToken, TradingSymbol: f.TradingSymbol,
			LTP: decimal.NewFromFloat(f.LTP), OpenInterest: int64(f.OpenInterest),
			Open: decimal.NewFromFloat(f.Open), High: decimal.NewFromFloat(f.High),
			Low: decimal.NewFromFloat(f.Low), Close: decimal.NewFromFloat(f.Close),
		})
	}
	return out, nil
}

type historicalRequest struct {
	Exchange    string `json:"exchange"`
	SymbolToken string `json:"symboltoken"`
	Interval    string `json:"interval"`
	FromDate    string `json:"fromdate"`
	ToDate      string `json:"todate"`
}

type historicalResponse struct {
	Status bool             `json:"status"`
	Data   [][7]interface{} `json:"data"`
}

type Candle struct {
	Time                   time.Time
	Open, High, Low, Close decimal.Decimal
	Volume                 int64
}

// GetHistoricalCandles fetches OHLCV for any instrument (cash, futures,
// or options — same endpoint). interval: ONE_MINUTE, FIVE_MINUTE,
// FIFTEEN_MINUTE, ONE_HOUR, ONE_DAY.
func (c *Client) GetHistoricalCandles(ctx context.Context, exchange, symbolToken, interval string, from, to time.Time) ([]Candle, error) {
	req := historicalRequest{
		Exchange: exchange, SymbolToken: symbolToken, Interval: interval,
		FromDate: from.Format("2006-01-02 15:04"), ToDate: to.Format("2006-01-02 15:04"),
	}
	var resp historicalResponse
	if err := c.request(ctx, http.MethodPost, "/rest/secure/angelbroking/historical/v1/getCandleData", req, &resp); err != nil {
		return nil, err
	}
	if !resp.Status {
		return nil, fmt.Errorf("angelone: historical fetch rejected for token %s", symbolToken)
	}
	candles := make([]Candle, 0, len(resp.Data))
	for _, row := range resp.Data {
		ts, _ := time.Parse("2006-01-02T15:04:05-07:00", fmt.Sprint(row[0]))
		candles = append(candles, Candle{
			Time: ts, Open: toDecimal(row[1]), High: toDecimal(row[2]),
			Low: toDecimal(row[3]), Close: toDecimal(row[4]), Volume: toInt64(row[5]),
		})
	}
	return candles, nil
}

func toDecimal(v interface{}) decimal.Decimal {
	if n, ok := v.(float64); ok {
		return decimal.NewFromFloat(n)
	}
	return decimal.Zero
}
func toInt64(v interface{}) int64 {
	if n, ok := v.(float64); ok {
		return int64(n)
	}
	return 0
}

func (c *Client) request(ctx context.Context, method, path string, body, out interface{}) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-UserType", "USER")
	req.Header.Set("X-SourceID", "WEB")
	req.Header.Set("X-PrivateKey", c.apiKey)
	// SmartAPI requires these on every call (login included) but doesn't
	// verify they match real network info — a placeholder value is the
	// documented/commonly-used approach, not a workaround.
	req.Header.Set("X-ClientLocalIP", "127.0.0.1")
	req.Header.Set("X-ClientPublicIP", "127.0.0.1")
	req.Header.Set("X-MACAddress", "00:00:00:00:00:00")
	c.mu.RLock()
	if c.jwtToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.jwtToken)
	}
	c.mu.RUnlock()

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("angelone: %s %s -> HTTP %d: %s", method, path, resp.StatusCode, string(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
