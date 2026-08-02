package angelone

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"
)

type FuturesData struct {
	Expiry       string
	SpotPrice    decimal.Decimal
	FuturePrice  decimal.Decimal
	OpenInterest int64
	Basis        decimal.Decimal // future - spot; positive = contango, negative = backwardation
	BasisPct     decimal.Decimal // basis / spot * 100
}

// FetchNiftyFutures gets the NIFTY future's LTP+OI for one expiry plus the
// NIFTY 50 spot LTP, and computes basis — completing the "NIFTY futures
// OI + basis" data point (previously only had instrument lookup, no
// actual fetch+calc). spotToken is NIFTY 50's index token from the scrip
// master (name "NIFTY", exch_seg "NSE", instrumenttype "AMXIDX").
func (c *Client) FetchNiftyFutures(ctx context.Context, instruments []Instrument, expiry, spotToken string) (FuturesData, error) {
	fut, ok := FindFuturesByExpiry(instruments, expiry)
	if !ok {
		return FuturesData{}, fmt.Errorf("angelone: no NIFTY future found for expiry %q", expiry)
	}

	quotes, err := c.GetQuotes(ctx, "NFO", []string{fut.Token})
	if err != nil {
		return FuturesData{}, fmt.Errorf("angelone: fetch future quote: %w", err)
	}
	if len(quotes) == 0 {
		return FuturesData{}, fmt.Errorf("angelone: empty quote response for future token %s", fut.Token)
	}
	futureQuote := quotes[0]

	spotQuotes, err := c.GetQuotes(ctx, "NSE", []string{spotToken})
	if err != nil {
		return FuturesData{}, fmt.Errorf("angelone: fetch spot quote: %w", err)
	}
	if len(spotQuotes) == 0 {
		return FuturesData{}, fmt.Errorf("angelone: empty quote response for spot token %s", spotToken)
	}
	spot := spotQuotes[0].LTP

	basis := futureQuote.LTP.Sub(spot)
	var basisPct decimal.Decimal
	if !spot.IsZero() {
		basisPct = basis.Div(spot).Mul(decimal.NewFromInt(100))
	}

	return FuturesData{
		Expiry: expiry, SpotPrice: spot, FuturePrice: futureQuote.LTP,
		OpenInterest: futureQuote.OpenInterest, Basis: basis, BasisPct: basisPct,
	}, nil
}
