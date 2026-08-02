// Package httpx is the shared HTTP client every connector uses: sane
// timeouts, a browser-like User-Agent (several free sources — notably
// NSE — reject Go's default UA outright), and a cookie jar (NSE's API
// requires hitting the homepage first to pick up session cookies before
// its /api/* endpoints will respond).
package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"
)

const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// New returns a client with a cookie jar (needed for the NSE warm-up
// pattern; harmless for sources that ignore cookies) and a 15s timeout —
// long enough for slow free endpoints, short enough to fail fast in an
// automated pipeline.
func New() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Timeout: 15 * time.Second, Jar: jar}
}

// Get performs a GET with the standard headers and returns the raw body.
// Callers needing NSE's cookie warm-up should call WarmUpNSE first with
// the same *http.Client.
func Get(ctx context.Context, client *http.Client, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("httpx: GET %s -> HTTP %d", url, resp.StatusCode)
	}
	return body, nil
}

// WarmUpNSE hits nseindia.com's homepage to collect the session cookies
// its /api/* endpoints require. NSE is known to rate-limit and
// occasionally block datacenter/cloud IPs outright regardless of this —
// if every NSE connector call fails with 403, that's the likely cause,
// not a bug in the request itself. Works reliably from a normal
// residential/office IP.
func WarmUpNSE(ctx context.Context, client *http.Client) error {
	_, err := Get(ctx, client, "https://www.nseindia.com/option-chain", map[string]string{
		"Referer": "https://www.nseindia.com/",
	})
	return err
}
