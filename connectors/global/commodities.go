package global

import (
	"context"
	"net/http"

	"connectors/yahoo"
)

type Commodities struct {
	CrudeWTI Cue
	Gold     Cue
	Silver   Cue
}

// Gold/Silver rising is conventionally a risk-off (flight-to-safety)
// signal, not "commodities bullish" in the equities sense — Label()
// reports crude's direction only, since crude's move is the one that
// actually feeds India-specific inflation/import-bill concerns. Gold's
// risk-off read is folded into risk.go's composite instead, not hidden
// inside this label.
func (m Commodities) Label() string {
	if !m.CrudeWTI.OK {
		return "unknown"
	}
	return bucket(m.CrudeWTI.ChangePct, 0.3)
}

func FetchCommodities(ctx context.Context, client *http.Client) Commodities {
	return Commodities{
		CrudeWTI: fetchCue(ctx, client, "Crude Oil (WTI)", yahoo.SymbolCrudeWTI),
		Gold:     fetchCue(ctx, client, "Gold", SymbolGold),
		Silver:   fetchCue(ctx, client, "Silver", SymbolSilver),
	}
}
