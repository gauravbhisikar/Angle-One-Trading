package global

import "time"

// Session reports which major markets are open right now, from fixed
// trading-hour windows in each market's local time. Deliberately not
// holiday-aware — connectors is a separate Go module from engine/, which
// owns the one real holiday-aware session calculator
// (internal/marketsession, built specifically for NSE); duplicating that
// precision here for four more markets is out of scope. Treat this as
// "roughly open", good enough to decide whether a given overnight cue is
// still live or already stale.
type Session struct {
	USOpen    bool `json:"us_open"`
	JapanOpen bool `json:"japan_open"`
	HKOpen    bool `json:"hk_open"`
	ChinaOpen bool `json:"china_open"`
	IndiaOpen bool `json:"india_open"`
}

func inWindow(t time.Time, loc *time.Location, startH, startM, endH, endM int) bool {
	local := t.In(loc)
	if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
		return false
	}
	start := time.Date(local.Year(), local.Month(), local.Day(), startH, startM, 0, 0, loc)
	end := time.Date(local.Year(), local.Month(), local.Day(), endH, endM, 0, 0, loc)
	return !local.Before(start) && !local.After(end)
}

func FetchSession(now time.Time) Session {
	ny, _ := time.LoadLocation("America/New_York")
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	hk, _ := time.LoadLocation("Asia/Hong_Kong")
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	ist, _ := time.LoadLocation("Asia/Kolkata")

	return Session{
		USOpen:    inWindow(now, ny, 9, 30, 16, 0),
		JapanOpen: inWindow(now, tokyo, 9, 0, 15, 0),
		HKOpen:    inWindow(now, hk, 9, 30, 16, 0),
		ChinaOpen: inWindow(now, shanghai, 9, 30, 15, 0),
		IndiaOpen: inWindow(now, ist, 9, 15, 15, 30),
	}
}
