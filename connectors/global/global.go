// Package global answers "what's happening in the rest of the world that
// matters for NIFTYBEES right now" as a small set of structured labels
// (risk_mode, per-pillar bullish/bearish/neutral, a disclosed confidence),
// not as 15 raw ticker quotes an LLM would have to interpret itself —
// same "disclosed rule, not a hidden score" principle contextbuilder's
// RegimeContext already uses (see regime_provider.go).
//
// All quotes come from yahoo.FetchQuote — confirmed live against these
// exact symbols (2026-08-02): ^GSPC, ^VIX, ^N225, ^HSI, 000001.SS, GC=F,
// SI=F, DX-Y.NYB, plus the existing ^DJI/^IXIC/CL=F/INR=X.
package global

import (
	"context"
	"net/http"

	"github.com/shopspring/decimal"

	"connectors/yahoo"
)

// Additional symbols beyond what connectors/yahoo already declares
// (^DJI, ^IXIC, CL=F, INR=X, ^INDIAVIX cover the existing overnight-cues
// path — these are the ones this package adds).
const (
	SymbolSP500     = "%5EGSPC"
	SymbolVIXUS     = "%5EVIX"
	SymbolNikkei225 = "%5EN225"
	SymbolHangSeng  = "%5EHSI"
	SymbolShanghai  = "000001.SS"
	SymbolGold      = "GC=F"
	SymbolSilver    = "SI=F"
	SymbolDollarIdx = "DX-Y.NYB"
)

// Cue is one quote reduced to what a composite actually needs: the % move.
type Cue struct {
	Name      string
	Price     decimal.Decimal
	ChangePct decimal.Decimal
	OK        bool // false if the fetch failed — excluded from composites, not treated as zero
}

func fetchCue(ctx context.Context, client *http.Client, name, symbol string) Cue {
	q, err := yahoo.FetchQuote(ctx, client, symbol)
	if err != nil {
		return Cue{Name: name, OK: false}
	}
	return Cue{Name: name, Price: q.Price, ChangePct: q.ChangePct(), OK: true}
}

// bucket labels a set of cues' average change as bullish/bearish/neutral
// against a fixed threshold — the same kind of plain rule RegimeContext
// uses, disclosed via the caller's Basis field rather than hidden here.
func bucket(avgChangePct decimal.Decimal, thresholdPct float64) string {
	t := decimal.NewFromFloat(thresholdPct)
	switch {
	case avgChangePct.GreaterThan(t):
		return "bullish"
	case avgChangePct.LessThan(t.Neg()):
		return "bearish"
	default:
		return "neutral"
	}
}

func pct(f float64) decimal.Decimal { return decimal.NewFromFloat(f) }

func average(cues ...Cue) (decimal.Decimal, int) {
	sum := decimal.Zero
	n := 0
	for _, c := range cues {
		if !c.OK {
			continue
		}
		sum = sum.Add(c.ChangePct)
		n++
	}
	if n == 0 {
		return decimal.Zero, 0
	}
	return sum.Div(decimal.NewFromInt(int64(n))), n
}
