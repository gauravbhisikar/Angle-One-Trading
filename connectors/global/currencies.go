package global

import (
	"context"
	"net/http"

	"connectors/yahoo"
)

type Currencies struct {
	USDINR      Cue
	DollarIndex Cue
}

// "stable" / "weakening" / "strengthening" describes the RUPEE (not the
// dollar) — USDINR rising means a weaker rupee, so the sign is inverted
// relative to a plain bucket() call.
func (m Currencies) Label() string {
	if !m.USDINR.OK {
		return "unknown"
	}
	switch {
	case m.USDINR.ChangePct.GreaterThan(pct(0.2)):
		return "weakening"
	case m.USDINR.ChangePct.LessThan(pct(-0.2)):
		return "strengthening"
	default:
		return "stable"
	}
}

func FetchCurrencies(ctx context.Context, client *http.Client) Currencies {
	return Currencies{
		USDINR:      fetchCue(ctx, client, "USD/INR", yahoo.SymbolUSDINR),
		DollarIndex: fetchCue(ctx, client, "Dollar Index (DXY)", SymbolDollarIdx),
	}
}
