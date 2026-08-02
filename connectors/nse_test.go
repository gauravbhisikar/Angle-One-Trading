package connectors_test

import (
	"context"
	"testing"
	"time"

	"connectors/httpx"
	"connectors/nse"
)

func TestNSEHolidaysFIIDIICorporateActions(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := httpx.New()

	holidays, err := nse.FetchHolidays(ctx, client)
	if err != nil {
		t.Errorf("FetchHolidays: %v (best-effort, may need a non-datacenter IP)", err)
	} else if len(holidays) == 0 {
		t.Error("FetchHolidays returned 0 rows — expected a real holiday list")
	} else {
		t.Logf("holidays: %d entries, first=%s (%s)", len(holidays), holidays[0].Date.Format("2006-01-02"), holidays[0].Description)
	}

	flows, err := nse.FetchFIIDII(ctx, client)
	if err != nil {
		t.Errorf("FetchFIIDII: %v (best-effort, may need a non-datacenter IP)", err)
	} else if len(flows) == 0 {
		t.Error("FetchFIIDII returned 0 rows — expected FII/DII data")
	} else {
		for _, f := range flows {
			t.Logf("flow: category=%s date=%s net=%.2f", f.Category, f.Date.Format("2006-01-02"), f.NetValue)
		}
	}

	actions, err := nse.FetchCorporateActions(ctx, client, "NIFTYBEES")
	if err != nil {
		t.Errorf("FetchCorporateActions: %v (best-effort, may need a non-datacenter IP)", err)
	} else if len(actions) == 0 {
		t.Error("FetchCorporateActions returned 0 rows for NIFTYBEES — expected real corporate action history (e.g. the 2019 face-value split)")
	} else {
		t.Logf("corporate actions: %d entries, first purpose=%q ex_date=%s", len(actions), actions[0].Purpose, actions[0].ExDate.Format("2006-01-02"))
		if actions[0].Purpose == "" {
			t.Error("Purpose field is empty — field-name mapping likely wrong again")
		}
	}

	// NIFTYBEES itself (an ETF) has no board meetings/earnings, so use a
	// real company to verify the parser actually extracts data, not just
	// that it doesn't error on an empty array.
	announcements, err := nse.FetchAnnouncements(ctx, client, "RELIANCE")
	if err != nil {
		t.Errorf("FetchAnnouncements: %v (best-effort, may need a non-datacenter IP)", err)
	} else if len(announcements) == 0 {
		t.Error("FetchAnnouncements returned 0 rows for RELIANCE — expected real announcements")
	} else {
		t.Logf("announcements: %d entries, first subject=%q date=%s", len(announcements), announcements[0].Subject, announcements[0].Date.Format("2006-01-02"))
		if announcements[0].Subject == "" {
			t.Error("Subject field is empty — field-name mapping likely wrong")
		}
	}
}
