// Package angelone is a direct client for Angel One's SmartAPI — plain
// REST + WebSocket, no SDK. There is no official Go SDK; none is needed,
// the API is just HTTP/JSON and a WebSocket feed.
//
// NOTE: the WebSocket binary tick format (ws.go) is implemented against
// SmartAPI's documented WebSocket 2.0 wire layout. Broker binary formats
// occasionally change — verify byte offsets against the current official
// docs before pointing this at a live session.
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

	"tradingengine/internal/models"
)

const baseURL = "https://apiconnect.angelone.in"

type Client struct {
	http *http.Client

	apiKey     string
	clientCode string
	pin        string
	totpSecret string

	mu           sync.RWMutex
	jwtToken     string
	refreshToken string
	feedToken    string
}

func NewClient(apiKey, clientCode, pin, totpSecret string) *Client {
	return &Client{
		http:       &http.Client{Timeout: 15 * time.Second},
		apiKey:     apiKey,
		clientCode: clientCode,
		pin:        pin,
		totpSecret: totpSecret,
	}
}

func (c *Client) FeedToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.feedToken
}

func (c *Client) JWTToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.jwtToken
}

type loginResponse struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    struct {
		JWTToken     string `json:"jwtToken"`
		RefreshToken string `json:"refreshToken"`
		FeedToken    string `json:"feedToken"`
	} `json:"data"`
}

// Login authenticates via TOTP + PIN and stores the session tokens
// (SmartAPI loginByPassword). Nothing in the engine calls this
// automatically at startup — it only runs when live/paper-with-broker-data
// mode is explicitly selected.
func (c *Client) Login(ctx context.Context) error {
	totp, err := GenerateTOTP(c.totpSecret)
	if err != nil {
		return err
	}

	body := map[string]string{
		"clientcode": c.clientCode,
		"password":   c.pin,
		"totp":       totp,
	}

	var resp loginResponse
	if err := c.request(ctx, http.MethodPost, "/rest/auth/angelbroking/user/v1/loginByPassword", nil, body, &resp); err != nil {
		return err
	}
	if !resp.Status {
		return fmt.Errorf("angelone: login failed: %s", resp.Message)
	}

	c.mu.Lock()
	c.jwtToken = resp.Data.JWTToken
	c.refreshToken = resp.Data.RefreshToken
	c.feedToken = resp.Data.FeedToken
	c.mu.Unlock()
	return nil
}

type historicalRequest struct {
	Exchange    string `json:"exchange"`
	SymbolToken string `json:"symboltoken"`
	Interval    string `json:"interval"`
	FromDate    string `json:"fromdate"`
	ToDate      string `json:"todate"`
}

type historicalResponse struct {
	Status  bool             `json:"status"`
	Message string           `json:"message"`
	Data    [][7]interface{} `json:"data"` // [timestamp, open, high, low, close, volume]
}

// GetHistoricalCandles fetches OHLCV candles. interval is one of Angel
// One's interval codes: ONE_MINUTE, FIVE_MINUTE, FIFTEEN_MINUTE,
// THIRTY_MINUTE, ONE_HOUR, ONE_DAY.
func (c *Client) GetHistoricalCandles(ctx context.Context, exchange, symbolToken, interval string, from, to time.Time) ([]models.Candle, error) {
	req := historicalRequest{
		Exchange:    exchange,
		SymbolToken: symbolToken,
		Interval:    interval,
		FromDate:    from.Format("2006-01-02 15:04"),
		ToDate:      to.Format("2006-01-02 15:04"),
	}

	var resp historicalResponse
	if err := c.request(ctx, http.MethodPost, "/rest/secure/angelbroking/historical/v1/getCandleData", nil, req, &resp); err != nil {
		return nil, err
	}
	if !resp.Status {
		return nil, fmt.Errorf("angelone: historical fetch failed: %s", resp.Message)
	}

	candles := make([]models.Candle, 0, len(resp.Data))
	for _, row := range resp.Data {
		ts, _ := time.Parse("2006-01-02T15:04:05-07:00", fmt.Sprint(row[0]))
		candles = append(candles, models.Candle{
			Symbol:   symbolToken,
			OpenTime: ts,
			Open:     toDecimal(row[1]),
			High:     toDecimal(row[2]),
			Low:      toDecimal(row[3]),
			Close:    toDecimal(row[4]),
			Volume:   toInt64(row[5]),
			Closed:   true,
		})
	}
	return candles, nil
}

func toDecimal(v interface{}) decimal.Decimal {
	switch n := v.(type) {
	case float64:
		return decimal.NewFromFloat(n)
	case string:
		d, _ := decimal.NewFromString(n)
		return d
	}
	return decimal.Zero
}

func toInt64(v interface{}) int64 {
	if n, ok := v.(float64); ok {
		return int64(n)
	}
	return 0
}

// Do is the exported form of request, for callers outside this package
// (e.g. execution.AngelOneBroker) that need the order-management endpoints.
func (c *Client) Do(ctx context.Context, method, path string, body, out interface{}) error {
	return c.request(ctx, method, path, nil, body, out)
}

func (c *Client) request(ctx context.Context, method, path string, headers map[string]string, body, out interface{}) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-UserType", "USER")
	httpReq.Header.Set("X-SourceID", "WEB")
	httpReq.Header.Set("X-PrivateKey", c.apiKey)
	// SmartAPI requires these on every call (login included) but doesn't
	// verify they match real network info — a placeholder value is the
	// documented/commonly-used approach, not a workaround. Confirmed live
	// 2026-08-02: login/quote calls 400 with "Required header
	// 'X-MACaddress' is missing" without these (connectors/angelone hit
	// the same gap, fixed there first).
	httpReq.Header.Set("X-ClientLocalIP", "127.0.0.1")
	httpReq.Header.Set("X-ClientPublicIP", "127.0.0.1")
	httpReq.Header.Set("X-MACAddress", "00:00:00:00:00:00")
	if jwt := c.JWTToken(); jwt != "" {
		httpReq.Header.Set("Authorization", "Bearer "+jwt)
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.http.Do(httpReq)
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
