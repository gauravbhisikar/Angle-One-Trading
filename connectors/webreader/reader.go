package webreader

import (
	"context"
	"net/http"
)

// Reader converts a URL into readable text. Jina is the default
// implementation, not the only one — swap providers (a future Firecrawl/
// Crawl4AI implementation, a headless-browser one, or PlainHTTP below)
// without touching whatever calls Reader, if Jina's free tier ever stops
// being good enough.
type Reader interface {
	Read(ctx context.Context, client *http.Client, url string) (string, error)
}

type jinaReader struct{}

// Jina is the default Reader: free, no key, Markdown output. See
// jina.go's package doc for its real reliability caveats (anonymous-tier
// rate limiting, no JS execution).
var Jina Reader = jinaReader{}

func (jinaReader) Read(ctx context.Context, client *http.Client, url string) (string, error) {
	return Read(ctx, client, url)
}

type plainHTTPReader struct{}

// PlainHTTP is the no-dependency fallback: raw response body, no
// Markdown conversion, no ad/nav stripping. Useful when Jina is rate
// limited, when the target is already plain text/JSON (an RSS feed, an
// API response), or as a last resort so a Reader call never has zero
// working options.
var PlainHTTP Reader = plainHTTPReader{}

func (plainHTTPReader) Read(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return string(buf), nil
}

// FetchWithFallback tries readers in order, returning the first one that
// succeeds — the pattern a caller uses to get "Jina if it works, plain
// HTTP if it doesn't" without hand-rolling the fallback logic per call site.
func FetchWithFallback(ctx context.Context, client *http.Client, url string, readers ...Reader) (string, error) {
	var lastErr error
	for _, r := range readers {
		text, err := r.Read(ctx, client, url)
		if err == nil {
			return text, nil
		}
		lastErr = err
	}
	return "", lastErr
}
