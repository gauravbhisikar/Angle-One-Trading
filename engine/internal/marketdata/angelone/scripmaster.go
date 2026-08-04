package angelone

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// scripMasterURL is Angel One's full instrument master — free, no auth,
// ~35MB JSON. Deliberately duplicated from connectors/angelone/scripmaster.go
// (see the duplication note on Client above) rather than shared, since this
// engine module has zero dependency on the connectors module by design.
const scripMasterURL = "https://margincalculator.angelbroking.com/OpenAPI_File/files/OpenAPIScripMaster.json"

// niftybeesTokenFallback is Angel One's confirmed real token for the
// NIFTYBEES-EQ cash instrument (exchange NSE), verified 2026-08 via
// connectors/angelone/scripmaster.go's own live schema check. Used only if
// the scrip-master fetch fails or overruns the boot timeout — this engine
// only ever needs this one symbol, so falling all the way back to the mock
// feed over a slow 35MB download would be a worse outcome than trusting a
// token confirmed working earlier the same week.
const niftybeesTokenFallback = "10576"

type scripInstrument struct {
	Token          string `json:"token"`
	Symbol         string `json:"symbol"`
	Name           string `json:"name"`
	InstrumentType string `json:"instrumenttype"`
	ExchSeg        string `json:"exch_seg"`
}

// exchangeSegmentToType maps the scrip master's string exchange segment to
// the numeric ExchangeType WSFeed's Instrument (and Angel One's WebSocket
// subscribe frame) expects.
func exchangeSegmentToType(seg string) (int, bool) {
	switch seg {
	case "NSE":
		return 1, true
	case "NFO":
		return 2, true
	case "BSE":
		return 3, true
	case "BFO":
		return 4, true
	default:
		return 0, false
	}
}

// fetchScripMaster downloads and parses the full instrument list. Large
// (~35MB) — call once at boot, not per request.
func fetchScripMaster(ctx context.Context, client *http.Client) ([]scripInstrument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scripMasterURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("angelone: fetch scrip master: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("angelone: read scrip master: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("angelone: scrip master fetch -> HTTP %d", resp.StatusCode)
	}

	var raw []scripInstrument
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("angelone: decode scrip master: %w", err)
	}
	return raw, nil
}

// findEquity looks up a cash-market symbol (e.g. "NIFTYBEES" -> the
// "NIFTYBEES-EQ" row). Exact match on Name, exchange NSE, no F&O/options row.
func findEquity(instruments []scripInstrument, name string) (scripInstrument, bool) {
	for _, i := range instruments {
		if i.ExchSeg == "NSE" && i.InstrumentType == "" && strings.EqualFold(i.Name, name) {
			return i, true
		}
	}
	return scripInstrument{}, false
}

// ResolveNIFTYBEES returns the WSFeed Instrument for NIFTYBEES, fetching the
// live scrip master first and falling back to the last-confirmed hardcoded
// token if that fetch fails or ctx expires — this engine is NIFTYBEES-only,
// so there is exactly one instrument to resolve, ever.
func ResolveNIFTYBEES(ctx context.Context, client *http.Client) Instrument {
	instruments, err := fetchScripMaster(ctx, client)
	if err == nil {
		if inst, ok := findEquity(instruments, "NIFTYBEES"); ok {
			if exType, ok := exchangeSegmentToType(inst.ExchSeg); ok {
				return Instrument{ExchangeType: exType, Token: inst.Token}
			}
		}
	}
	return Instrument{ExchangeType: 1, Token: niftybeesTokenFallback}
}
