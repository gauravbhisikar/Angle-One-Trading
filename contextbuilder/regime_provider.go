package contextbuilder

import "context"

// RegimeProvider classifies bull/bear/sideways from a fixed, disclosed
// rule over dc.Market — not an invented ML score. Must run after
// MarketProvider (see taskSections in provider.go).
type RegimeProvider struct{}

func NewRegimeProvider() *RegimeProvider { return &RegimeProvider{} }

func (p *RegimeProvider) Name() string { return "regime" }

func (p *RegimeProvider) Load(ctx context.Context, req BuildRequest, dc *DecisionContext) error {
	trend := dc.Market.Trend
	adx := parseF(dc.Market.ADX14)
	vix := parseF(dc.Market.VIX)

	var regime, basis string
	var confidence float64

	switch {
	case trend == "up" && adx >= 20:
		regime, basis = "bull", "EMA20>EMA50 (uptrend) with ADX>=20 (trending, not choppy)"
		confidence = clamp01(adx / 40)
	case trend == "down" && adx >= 20:
		regime, basis = "bear", "EMA20<EMA50 (downtrend) with ADX>=20 (trending, not choppy)"
		confidence = clamp01(adx / 40)
	default:
		regime, basis = "sideways", "ADX<20 (weak/no trend) or EMA20/EMA50 flat — no directional edge detected"
		confidence = clamp01(1 - adx/20)
	}

	// High VIX reduces confidence in any regime call — volatility spikes
	// invalidate trend-following assumptions faster than the EMA/ADX
	// signals update, a widely-used discount, not a hidden fudge factor.
	if vix > 20 {
		confidence *= 0.7
		basis += "; discounted for VIX>20"
	}

	dc.Regime = RegimeContext{Regime: regime, Confidence: round2(confidence), Basis: basis}
	return nil
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
func round2(f float64) float64 {
	return float64(int(f*100)) / 100
}
