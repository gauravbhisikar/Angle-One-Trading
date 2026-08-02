// Package rbi covers RBI monetary policy awareness — the one macro data
// point on the original wishlist with no clean free API for a *forward*
// calendar. What IS free and confirmed working: RBI's press-release RSS
// feed, which announces policy decisions the day they happen (reactive,
// not predictive). RBI does publish the year's MPC meeting dates in
// advance each February, but only as a page/PDF with no stable structured
// endpoint — safer to hardcode that short list once a year (6 dates) and
// update it manually than to pretend a scraper for it is reliable.
package rbi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"connectors/news"
)

const pressReleaseFeed = "https://rbi.org.in/pressreleases_rss.xml"

// FetchPolicyAnnouncements filters RBI's press-release RSS feed down to
// items that look like monetary policy releases (repo rate decisions,
// MPC statements). Reactive only — tells you a decision just happened,
// not that one is coming up.
func FetchPolicyAnnouncements(ctx context.Context, client *http.Client) ([]news.Headline, error) {
	all, err := news.FetchFeed(ctx, client, pressReleaseFeed, "RBI Press Releases")
	if err != nil {
		return nil, err
	}
	var out []news.Headline
	for _, h := range all {
		lower := strings.ToLower(h.Title)
		if strings.Contains(lower, "monetary policy") || strings.Contains(lower, "repo rate") || strings.Contains(lower, "mpc") {
			out = append(out, h)
		}
	}
	return out, nil
}

// MPCMeetingDate is a forward-looking calendar entry. RBI has no stable
// free API for this — populate/update this slice manually once a year
// from RBI's published MPC calendar (rbi.org.in, published every
// February for the following ~12 months). Treat entries beyond the
// current known list as "unknown", not "no meeting."
type MPCMeetingDate struct {
	Date time.Time
	Note string
}

// KnownMPCDates is intentionally empty here — fill it in from RBI's
// current published calendar rather than trusting a hardcoded guess to
// stay accurate across years. See package doc for why this can't be a
// live fetch.
var KnownMPCDates []MPCMeetingDate
