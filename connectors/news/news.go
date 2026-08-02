// Package news pulls market headlines from free, no-auth RSS feeds — RSS
// is a stable format that rarely breaks, unlike scraping a page's HTML.
// Confirmed working: Economic Times Markets, Moneycontrol. Business
// Standard's markets RSS returned HTTP 403 in testing — dropped rather
// than shipped as a silently-broken source.
package news

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"time"

	"connectors/httpx"
)

const (
	FeedETMarkets    = "https://economictimes.indiatimes.com/markets/rssfeeds/1977021501.cms"
	FeedMoneycontrol = "https://www.moneycontrol.com/rss/marketreports.xml"
)

type Headline struct {
	Title       string
	Link        string
	Description string
	Published   time.Time
	Source      string
}

type rssFeed struct {
	Channel struct {
		Item []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
		} `xml:"item"`
	} `xml:"channel"`
}

// FetchFeed parses one RSS 2.0 feed URL into headlines.
func FetchFeed(ctx context.Context, client *http.Client, feedURL, sourceName string) ([]Headline, error) {
	body, err := httpx.Get(ctx, client, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("news: fetch %s: %w", sourceName, err)
	}

	var parsed rssFeed
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("news: parse %s: %w", sourceName, err)
	}

	out := make([]Headline, 0, len(parsed.Channel.Item))
	for _, item := range parsed.Channel.Item {
		pub, _ := time.Parse(time.RFC1123Z, item.PubDate)
		out = append(out, Headline{
			Title: item.Title, Link: item.Link, Description: item.Description,
			Published: pub, Source: sourceName,
		})
	}
	return out, nil
}

// FetchAll pulls every confirmed-working feed and merges them, newest
// first isn't guaranteed across sources — sort by Published if strict
// chronological order matters.
func FetchAll(ctx context.Context, client *http.Client) ([]Headline, error) {
	sources := map[string]string{
		"Economic Times Markets": FeedETMarkets,
		"Moneycontrol":           FeedMoneycontrol,
	}
	var all []Headline
	var firstErr error
	for name, url := range sources {
		items, err := FetchFeed(ctx, client, url, name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue // one feed failing shouldn't blank out the others
		}
		all = append(all, items...)
	}
	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}
