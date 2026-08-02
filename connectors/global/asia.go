package global

import (
	"context"
	"net/http"
)

type AsiaMarkets struct {
	Nikkei225 Cue
	HangSeng  Cue
	Shanghai  Cue
}

func (m AsiaMarkets) Label() string {
	avg, n := average(m.Nikkei225, m.HangSeng, m.Shanghai)
	if n == 0 {
		return "unknown"
	}
	return bucket(avg, 0.15)
}

func FetchAsiaMarkets(ctx context.Context, client *http.Client) AsiaMarkets {
	return AsiaMarkets{
		Nikkei225: fetchCue(ctx, client, "Nikkei 225", SymbolNikkei225),
		HangSeng:  fetchCue(ctx, client, "Hang Seng", SymbolHangSeng),
		Shanghai:  fetchCue(ctx, client, "Shanghai Composite", SymbolShanghai),
	}
}
