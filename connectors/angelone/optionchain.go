package angelone

import (
	"context"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

type StrikeData struct {
	Strike  decimal.Decimal
	CallOI  int64
	PutOI   int64
	CallLTP decimal.Decimal
	PutLTP  decimal.Decimal
}

type OptionChain struct {
	Expiry  string
	Strikes []StrikeData
	PCR     decimal.Decimal // sum(put OI) / sum(call OI) — >1 bullish-leaning positioning, <1 bearish-leaning
	MaxPain decimal.Decimal // strike where option writers collectively lose the least
}

// FetchNiftyOptionChain builds the full NIFTY option chain for one expiry:
// looks up every CE/PE instrument for that expiry from the free scrip
// master, batch-fetches live OI+LTP via the authenticated quote API, and
// derives PCR + max pain. This is the option-chain/sentiment data point
// Angel One can serve directly — no NSE scraping needed.
func (c *Client) FetchNiftyOptionChain(ctx context.Context, instruments []Instrument, expiry string) (OptionChain, error) {
	opts := FindOptionsByExpiry(instruments, expiry)
	if len(opts) == 0 {
		return OptionChain{}, fmt.Errorf("angelone: no NIFTY options found for expiry %q (check ListNiftyExpiries)", expiry)
	}

	tokens := make([]string, 0, len(opts))
	tokenToInst := map[string]Instrument{}
	for _, o := range opts {
		tokens = append(tokens, o.Token)
		tokenToInst[o.Token] = o
	}

	byStrike := map[string]*StrikeData{}
	// Angel One's quote API caps tokens per call; chunk to stay safe.
	const chunkSize = 50
	for i := 0; i < len(tokens); i += chunkSize {
		end := i + chunkSize
		if end > len(tokens) {
			end = len(tokens)
		}
		quotes, err := c.GetQuotes(ctx, "NFO", tokens[i:end])
		if err != nil {
			return OptionChain{}, fmt.Errorf("angelone: quote chunk %d-%d: %w", i, end, err)
		}
		for _, q := range quotes {
			inst := tokenToInst[q.Token]
			key := inst.Strike.String()
			row, ok := byStrike[key]
			if !ok {
				row = &StrikeData{Strike: inst.Strike}
				byStrike[key] = row
			}
			isCall := strings.HasSuffix(inst.Symbol, "CE")
			if isCall {
				row.CallOI, row.CallLTP = q.OpenInterest, q.LTP
			} else {
				row.PutOI, row.PutLTP = q.OpenInterest, q.LTP
			}
		}
	}

	chain := OptionChain{Expiry: expiry}
	var totalCallOI, totalPutOI int64
	for _, row := range byStrike {
		chain.Strikes = append(chain.Strikes, *row)
		totalCallOI += row.CallOI
		totalPutOI += row.PutOI
	}
	if totalCallOI > 0 {
		chain.PCR = decimal.NewFromInt(totalPutOI).Div(decimal.NewFromInt(totalCallOI))
	}
	chain.MaxPain = computeMaxPain(chain.Strikes)
	return chain, nil
}

// computeMaxPain finds the strike where total option-writer payout
// (across all strikes' CE+PE open interest) is minimized — the classical
// "max pain" theory that price gravitates there by expiry.
func computeMaxPain(strikes []StrikeData) decimal.Decimal {
	if len(strikes) == 0 {
		return decimal.Zero
	}
	best := strikes[0].Strike
	bestPain := decimal.NewFromInt(1<<62 - 1)

	for _, candidate := range strikes {
		pain := decimal.Zero
		for _, s := range strikes {
			if candidate.Strike.GreaterThan(s.Strike) {
				// Settlement above this strike: the call is ITM, writer pays (settlement - strike) * OI.
				pain = pain.Add(candidate.Strike.Sub(s.Strike).Mul(decimal.NewFromInt(s.CallOI)))
			}
			if candidate.Strike.LessThan(s.Strike) {
				// Settlement below this strike: the put is ITM, writer pays (strike - settlement) * OI.
				pain = pain.Add(s.Strike.Sub(candidate.Strike).Mul(decimal.NewFromInt(s.PutOI)))
			}
		}
		if pain.LessThan(bestPain) {
			bestPain = pain
			best = candidate.Strike
		}
	}
	return best
}
