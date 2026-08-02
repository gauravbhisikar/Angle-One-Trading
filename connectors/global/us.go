package global

import (
	"context"
	"net/http"

	"connectors/yahoo"
)

type USMarkets struct {
	Dow    Cue
	Nasdaq Cue
	SP500  Cue
	VIX    Cue
}

// Bullish/bearish/neutral label from Dow+Nasdaq+S&P500's average change —
// VIX is reported separately (it's a fear gauge, not a directional index;
// folding it into the same average would conflate "stocks up" with "calm").
func (m USMarkets) Label() string {
	avg, n := average(m.Dow, m.Nasdaq, m.SP500)
	if n == 0 {
		return "unknown"
	}
	return bucket(avg, 0.15)
}

func FetchUSMarkets(ctx context.Context, client *http.Client) USMarkets {
	return USMarkets{
		Dow:    fetchCue(ctx, client, "Dow Jones", yahoo.SymbolDowJones),
		Nasdaq: fetchCue(ctx, client, "Nasdaq", yahoo.SymbolNasdaq),
		SP500:  fetchCue(ctx, client, "S&P 500", SymbolSP500),
		VIX:    fetchCue(ctx, client, "VIX (US)", SymbolVIXUS),
	}
}
