package contextbuilder

import (
	"context"
	"net/http"
	"strings"
	"time"

	"connectors/httpx"
	"connectors/news"
	"connectors/rbi"
	"connectors/webreader"
)

// Finding is one piece of research evidence: a curated-feed item (news
// headline or RBI release) whose full body was fetched, not a generic
// web-search result. There is deliberately no open web-search step here —
// see contextbuilder/BACKLOG.md: no free, reliable discovery-search API
// was found (Google Trends 404'd, Reddit 403'd, checked live). What IS
// real: news/RBI feeds are already curated to legitimate publishers, and
// webreader can fetch the full body behind a feed's link. Research here
// means "search what's already curated, read the full text of matches,"
// not "search the open web."
type Finding struct {
	Source      string `json:"source"` // "news" | "rbi"
	Title       string `json:"title"`
	URL         string `json:"url"`
	PublishedAt string `json:"published_at,omitempty"`
	Excerpt     string `json:"excerpt"` // full body via webreader, truncated to a sane length
}

const maxExcerptLen = 4000

// Research searches curated feeds (news headlines, RBI releases) for
// items matching query (simple case-insensitive keyword match against
// the title — no semantic search, disclosed as such), and fetches the
// full body of each match via webreader. Caller (the agent's research
// node) decides whether to call this at all — most requests won't need
// it, per contextbuilder's task-aware design.
func Research(ctx context.Context, client *http.Client, query string, maxResults int) ([]Finding, error) {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil, nil
	}

	matches := func(title string) bool {
		lower := strings.ToLower(title)
		for _, t := range terms {
			if strings.Contains(lower, t) {
				return true
			}
		}
		return false
	}

	var findings []Finding

	if headlines, err := news.FetchAll(ctx, client); err == nil {
		for _, h := range headlines {
			if len(findings) >= maxResults {
				break
			}
			if !matches(h.Title) {
				continue
			}
			excerpt := fetchExcerpt(ctx, client, h.Link)
			findings = append(findings, Finding{
				Source: "news", Title: h.Title, URL: h.Link,
				PublishedAt: h.Published.Format(time.RFC3339), Excerpt: excerpt,
			})
		}
	}

	if len(findings) < maxResults {
		if items, err := rbi.FetchPolicyAnnouncements(ctx, client); err == nil {
			for _, h := range items {
				if len(findings) >= maxResults {
					break
				}
				if !matches(h.Title) {
					continue
				}
				excerpt := fetchExcerpt(ctx, client, h.Link)
				findings = append(findings, Finding{
					Source: "rbi", Title: h.Title, URL: h.Link,
					PublishedAt: h.Published.Format(time.RFC3339), Excerpt: excerpt,
				})
			}
		}
	}

	return findings, nil
}

func fetchExcerpt(ctx context.Context, client *http.Client, url string) string {
	if url == "" {
		return ""
	}
	text, err := webreader.FetchWithFallback(ctx, client, url, webreader.Jina, webreader.PlainHTTP)
	if err != nil {
		return ""
	}
	if len(text) > maxExcerptLen {
		text = text[:maxExcerptLen]
	}
	return text
}

// NewHTTPClient is the client Research (and the HTTP server) should use —
// exported so cmd/server can share one client/cookie-jar across requests
// instead of building a new one per call.
func NewHTTPClient() *http.Client { return httpx.New() }
