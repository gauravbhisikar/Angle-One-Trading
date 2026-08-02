package contextbuilder

import (
	"context"
	"fmt"
	"net/http"

	"connectors/httpx"
	"connectors/news"
	"connectors/nse"
	"connectors/overnight"
	"connectors/sentiment"
	"connectors/yahoo"
)

// MarketProvider pulls live price/technical/macro/sentiment context.
// Technical fields (RSI/EMA/ADX/trend) prefer the engine's Feature Store
// when it has a row for today — falls back to nothing (not a guess) if
// the feature store hasn't computed today's row yet, since recomputing
// RSI here would be a fifth reimplementation of the same math.
type MarketProvider struct {
	client *http.Client
	engine *engineClient
}

func NewMarketProvider(engineBaseURL string) *MarketProvider {
	return &MarketProvider{client: httpx.New(), engine: newEngineClient(engineBaseURL)}
}

func (p *MarketProvider) Name() string { return "market" }

func (p *MarketProvider) Load(ctx context.Context, req BuildRequest, dc *DecisionContext) error {
	symbol := req.Symbol
	if symbol == "" {
		symbol = "NIFTYBEES"
	}
	mc := MarketContext{Symbol: symbol}
	var warnings []string

	if quote, err := yahoo.FetchQuote(ctx, p.client, yahoo.SymbolNiftyBees); err == nil {
		mc.Price = quote.Price.String()
	} else {
		warnings = append(warnings, "yahoo price: "+err.Error())
	}

	if vix, err := yahoo.FetchQuote(ctx, p.client, yahoo.SymbolIndiaVIX); err == nil {
		mc.VIX = vix.Price.String()
	} else {
		warnings = append(warnings, "vix: "+err.Error())
	}

	if row, ok, err := p.engine.latestFeatures(ctx, symbol); err == nil && ok {
		mc.RSI14, mc.EMA20, mc.EMA50, mc.ADX14 = row.RSI14, row.EMA20, row.EMA50, row.ADX14
		mc.Trend = trendFromEMAs(row.EMA20, row.EMA50)
	} else if err != nil {
		warnings = append(warnings, "feature store: "+err.Error())
	}

	if breadth, err := nse.FetchMarketBreadth(ctx, p.client); err == nil {
		mc.Breadth = breadth.AdvanceDecline.String()
	} else {
		warnings = append(warnings, "market breadth (best-effort, may need a non-datacenter IP): "+err.Error())
	}

	if flows, err := nse.FetchFIIDII(ctx, p.client); err == nil {
		for _, f := range flows {
			if f.Category == "FII/FPI" || f.Category == "FII" {
				mc.FIINet = fmt.Sprintf("%.2f", f.NetValue)
			}
			if f.Category == "DII" {
				mc.DIINet = fmt.Sprintf("%.2f", f.NetValue)
			}
		}
	} else {
		warnings = append(warnings, "fii/dii (best-effort, may need a non-datacenter IP): "+err.Error())
	}

	if headlines, err := news.FetchAll(ctx, p.client); err == nil {
		agg := sentiment.ScoreHeadlines(headlines)
		mc.NewsSentiment = string(agg.Label)
		mc.NewsScore = agg.AverageScore
	} else {
		warnings = append(warnings, "news sentiment: "+err.Error())
	}

	sig, err := overnight.Fetch(ctx, p.client, nil, nil, nil, "", "")
	if err == nil {
		mc.Overnight = string(sig.Source)
		mc.OvernightChangePct = sig.ChangePct.String()
		mc.OvernightConfidence = sig.Confidence
	} else {
		warnings = append(warnings, "overnight indicator: "+err.Error())
	}

	dc.Market = mc
	dc.Warnings = append(dc.Warnings, warnings...)
	return nil // partial data is still useful — recorded via Warnings, not a hard failure
}

func trendFromEMAs(ema20, ema50 string) string {
	e20, e50 := parseF(ema20), parseF(ema50)
	if e20 == 0 || e50 == 0 {
		return ""
	}
	switch {
	case e20 > e50:
		return "up"
	case e20 < e50:
		return "down"
	default:
		return "flat"
	}
}

func parseF(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}
