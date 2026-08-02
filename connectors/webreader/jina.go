// Package webreader turns any URL into clean, LLM-ready Markdown via
// Jina AI Reader (r.jina.ai) — free, no API key, no signup. Prefix any
// URL with "https://r.jina.ai/" and get back readable text instead of raw
// HTML: no ads, no nav chrome, no manual scraping/parsing per site.
//
// Confirmed working in testing (2026-08-01), with two real caveats worth
// knowing before relying on it:
//  1. Anonymous/free-tier requests share Jina's IP pool. Popular domains
//     that get scraped a lot (investing.com, google.com) can come back
//     403 "AbuseAlleviationError" due to OTHER users' traffic, not yours.
//     Less-hammered sites (AMC pages, blogs, most news sites) worked fine.
//  2. It does not execute JavaScript. Client-rendered SPA pages (e.g.
//     Groww's index pages) come back as an empty shell, not the data.
//     Works well on server-rendered pages: most news articles, blogs,
//     AMC/company fund pages, static content.
//
// Best use here: reading news article bodies, an AMC's fund/dividend
// page, or any page a connector doesn't have a structured API for —
// exactly the "give the agent workflow live web context" ask, without
// standing up a headless browser or paying for a search API.
package webreader

import (
	"context"
	"fmt"
	"net/http"

	"connectors/httpx"
)

const readerPrefix = "https://r.jina.ai/"

// Read fetches url through Jina Reader and returns clean Markdown.
func Read(ctx context.Context, client *http.Client, url string) (string, error) {
	body, err := httpx.Get(ctx, client, readerPrefix+url, nil)
	if err != nil {
		return "", fmt.Errorf("webreader: %w", err)
	}
	return string(body), nil
}
