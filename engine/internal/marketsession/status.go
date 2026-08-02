// Package marketsession is the single source of truth for "is NSE cash
// market open right now" (used by both the HTTP API's /market/status and
// the Monitor that auto-pauses/resumes strategies around it), plus the
// Monitor itself.
package marketsession

import "time"

var istLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.FixedZone("IST", 5*3600+1800) // fallback: fixed +05:30, no DST in India anyway
	}
	return loc
}()

type Status struct {
	Open     bool
	Reason   string
	ISTTime  string
	NextOpen string
}

// Current reports NSE cash-market hours (09:15-15:30 IST, Mon-Fri) — the
// same hard boundary ENGINE_SPEC Sec 6 enforces on order entry. Holiday
// calendar isn't wired in yet (ENGINE_SPEC Sec 7 — future work: NSE
// holidays are available live via connectors/nse.FetchHolidays, but this
// engine module can't import the connectors module by design — see
// docs/ENGINE_SPEC.md's "engine never fetches external data itself"
// boundary), so a listed trading holiday still reports "open" here even
// though no real trading would happen.
func Current(now time.Time) Status {
	ist := now.In(istLocation)
	tod := ist.Format("15:04")
	weekday := ist.Weekday()

	if weekday == time.Saturday || weekday == time.Sunday {
		return Status{Open: false, Reason: "weekend", ISTTime: ist.Format("2006-01-02 15:04:05")}
	}
	if tod < "09:15" {
		return Status{Open: false, Reason: "before_open", ISTTime: ist.Format("2006-01-02 15:04:05"), NextOpen: "09:15 IST"}
	}
	if tod > "15:30" {
		return Status{Open: false, Reason: "after_close", ISTTime: ist.Format("2006-01-02 15:04:05"), NextOpen: "09:15 IST next trading day"}
	}
	return Status{Open: true, Reason: "regular_session", ISTTime: ist.Format("2006-01-02 15:04:05")}
}
