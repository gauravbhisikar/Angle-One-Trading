package global

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Context is the one structured object this package hands to a caller —
// individual quotes are available (Detail) for audit, but the headline
// fields are the composite labels an LLM should actually reason over, not
// 15 raw prices it would have to interpret itself.
type Context struct {
	RiskMode    string  `json:"risk_mode"`   // "risk_on" | "risk_off" | "neutral"
	Confidence  float64 `json:"confidence"`  // 0-1, see Basis
	USEquities  string  `json:"us_equities"` // bullish | bearish | neutral | unknown
	AsiaOpen    string  `json:"asia_equities"`
	Commodities string  `json:"commodities"`
	Currencies  string  `json:"currencies"`
	OverallBias string  `json:"overall_bias"`
	Summary     string  `json:"summary"`
	Session     Session `json:"session"`
	Basis       string  `json:"basis"` // the formula that produced RiskMode/Confidence, disclosed not hidden

	US         USMarkets   `json:"-"`
	Asia       AsiaMarkets `json:"-"`
	Commod     Commodities `json:"-"`
	Curr       Currencies  `json:"-"`
	DataPoints int         `json:"data_points_ok"`
	DataMax    int         `json:"data_points_total"`
}

const totalDataPoints = 9 // Dow, Nasdaq, SP500, VIX, Nikkei, HangSeng, Shanghai, Gold, Crude — DXY/Silver/USDINR feed labels but not the composite

// Compute derives the composite from already-fetched pillar data — pure
// function, no I/O, so the formula is unit-testable independent of Yahoo
// being reachable.
func Compute(us USMarkets, asia AsiaMarkets, comm Commodities, curr Currencies, sess Session) Context {
	usAvg, usN := average(us.Dow, us.Nasdaq, us.SP500)
	asiaAvg, asiaN := average(asia.Nikkei225, asia.HangSeng, asia.Shanghai)

	// Composite: equities (US+Asia) pull risk_mode up, VIX and gold
	// (both classic flight-to-safety signals) pull it down. Weights are
	// arbitrary-but-disclosed, same caveat RegimeContext.Basis carries —
	// not a validated model, a starting rule.
	composite := decimal.Zero
	n := 0
	if usN > 0 {
		composite = composite.Add(usAvg)
		n++
	}
	if asiaN > 0 {
		composite = composite.Add(asiaAvg)
		n++
	}
	if n > 0 {
		composite = composite.Div(decimal.NewFromInt(int64(n)))
	}
	if us.VIX.OK {
		composite = composite.Sub(us.VIX.ChangePct.Mul(pct(0.3)))
	}
	if comm.Gold.OK {
		composite = composite.Sub(comm.Gold.ChangePct.Mul(pct(0.2)))
	}

	riskMode := "neutral"
	f, _ := composite.Float64()
	switch {
	case f > 0.2:
		riskMode = "risk_on"
	case f < -0.2:
		riskMode = "risk_off"
	}

	dataOK := 0
	for _, c := range []Cue{us.Dow, us.Nasdaq, us.SP500, us.VIX, asia.Nikkei225, asia.HangSeng, asia.Shanghai, comm.Gold, comm.CrudeWTI} {
		if c.OK {
			dataOK++
		}
	}
	confidence := float64(dataOK) / float64(totalDataPoints)

	usLabel, asiaLabel, commLabel, currLabel := us.Label(), asia.Label(), comm.Label(), curr.Label()

	overall := "mixed"
	switch {
	case usLabel == "bullish" && asiaLabel != "bearish":
		overall = "positive"
	case usLabel == "bearish" && asiaLabel != "bullish":
		overall = "negative"
	}

	summary := fmt.Sprintf(
		"US equities %s, Asia %s, commodities %s, currencies %s. Overall bias: %s (%s, %.0f%% data available).",
		usLabel, asiaLabel, commLabel, currLabel, overall, riskMode, confidence*100,
	)

	return Context{
		RiskMode: riskMode, Confidence: confidence,
		USEquities: usLabel, AsiaOpen: asiaLabel, Commodities: commLabel, Currencies: currLabel,
		OverallBias: overall, Summary: summary, Session: sess,
		Basis: "composite = avg(US equities %chg, Asia equities %chg) - 0.3*VIX %chg - 0.2*Gold %chg; " +
			"risk_on if composite > +0.2, risk_off if < -0.2, else neutral. confidence = data points fetched / 9.",
		US: us, Asia: asia, Commod: comm, Curr: curr, DataPoints: dataOK, DataMax: totalDataPoints,
	}
}

// Now is a thin wrapper so callers don't need a direct time import just to
// get "session as of right now".
func Now() time.Time { return time.Now().UTC() }
