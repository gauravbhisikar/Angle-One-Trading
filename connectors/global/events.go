// Global event headlines — raw, not interpreted. This package deliberately
// stops at "here are the real headlines that mention a global-market
// keyword" and does not attempt a Fed-speech-style "Impact: Moderately
// Bullish, Confidence 76%" narrative score: that kind of qualitative
// judgment is exactly what the AI agent's self_review LLM node already
// does over real numbers (see agent/nodes/self_review.py) — fabricating
// an equivalent score here in Go would be a hidden AI-like judgment
// masquerading as a disclosed rule, which is the one thing this project
// has consistently refused to do (see RegimeContext/RecommendationContext,
// both plain disclosed formulas, never an invented score). Once these
// headlines are wired into the agent's decision_context, its existing
// self_review node narrates them the same way it already narrates news
// sentiment and FII/DII numbers today.
package global

import (
	"context"
	"net/http"
	"strings"

	"connectors/news"
	"connectors/rbi"
)

var globalEventKeywords = []string{
	"fed", "federal reserve", "fomc", "rate hike", "rate cut", "interest rate",
	"ecb", "european central bank", "boj", "bank of japan", "pboc",
	"china", "stimulus", "tariff", "trade war", "opec", "crude oil", "oil price",
	"geopolitical", "war", "sanctions", "recession", "inflation", "dollar index",
	"treasury yield", "s&p 500", "nasdaq", "wall street", "global market", "global cue",
}

func mentionsGlobalEvent(title, description string) bool {
	text := strings.ToLower(title + " " + description)
	for _, kw := range globalEventKeywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// FetchGlobalEvents keyword-filters the same curated feeds
// contextbuilder/research.go already searches (connectors/news,
// connectors/rbi) down to headlines that actually mention a
// global-market driver. Same scope limitation as research.go: this is
// curated-feed search, not open web/news-wire search — no discovery API
// exists for that yet.
func FetchGlobalEvents(ctx context.Context, client *http.Client, maxResults int) ([]news.Headline, error) {
	var all []news.Headline

	if items, err := news.FetchAll(ctx, client); err == nil {
		all = append(all, items...)
	}
	if items, err := rbi.FetchPolicyAnnouncements(ctx, client); err == nil {
		all = append(all, items...)
	}

	var matched []news.Headline
	for _, h := range all {
		if mentionsGlobalEvent(h.Title, h.Description) {
			matched = append(matched, h)
			if len(matched) >= maxResults {
				break
			}
		}
	}
	return matched, nil
}
