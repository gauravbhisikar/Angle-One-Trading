package contextbuilder

import (
	"context"
	"net/http"

	"connectors/global"
	"connectors/httpx"
)

// GlobalProvider fills GlobalMarketContext — US/Asia equities, commodities,
// currencies, session awareness, and curated global-event headlines,
// reduced to a disclosed risk_mode/confidence composite (connectors/global
// carries the actual formula) rather than raw quotes.
type GlobalProvider struct {
	client *http.Client
}

func NewGlobalProvider() *GlobalProvider {
	return &GlobalProvider{client: httpx.New()}
}

func (p *GlobalProvider) Name() string { return "global" }

func (p *GlobalProvider) Load(ctx context.Context, req BuildRequest, dc *DecisionContext) error {
	us := global.FetchUSMarkets(ctx, p.client)
	asia := global.FetchAsiaMarkets(ctx, p.client)
	comm := global.FetchCommodities(ctx, p.client)
	curr := global.FetchCurrencies(ctx, p.client)
	sess := global.FetchSession(global.Now())

	gc := global.Compute(us, asia, comm, curr, sess)

	events, err := global.FetchGlobalEvents(ctx, p.client, 5)
	var warnings []string
	if err != nil {
		warnings = append(warnings, "global events: "+err.Error())
	}

	dc.GlobalMarket = GlobalMarketContext{
		RiskMode: gc.RiskMode, Confidence: gc.Confidence,
		USEquities: gc.USEquities, AsiaEquities: gc.AsiaOpen,
		Commodities: gc.Commodities, Currencies: gc.Currencies,
		OverallBias: gc.OverallBias, Summary: gc.Summary,
		Session: GlobalSession{
			USOpen: gc.Session.USOpen, JapanOpen: gc.Session.JapanOpen,
			HKOpen: gc.Session.HKOpen, ChinaOpen: gc.Session.ChinaOpen, IndiaOpen: gc.Session.IndiaOpen,
		},
		Basis: gc.Basis,
	}
	for _, e := range events {
		dc.GlobalMarket.Events = append(dc.GlobalMarket.Events, GlobalEvent{
			Title: e.Title, Source: e.Source, PublishedAt: e.Published.Format("2006-01-02T15:04:05Z07:00"), URL: e.Link,
		})
	}

	dc.Warnings = append(dc.Warnings, warnings...)
	return nil // partial data is still useful, recorded via Warnings not a hard failure
}
