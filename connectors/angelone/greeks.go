package angelone

import (
	"context"
	"net/http"

	"github.com/shopspring/decimal"
)

// GetOptionGreeks calls SmartAPI's dedicated Option Greeks endpoint —
// confirmed real via Angel One's own SmartAPI forum announcement (POST
// /rest/secure/angelbroking/marketData/v1/optionGreek). Given an
// underlying + expiry it returns delta/gamma/theta/vega/IV per strike in
// one call — this is what actually answers "option IV", which the plain
// quote endpoint (optionchain.go, GetQuotes) does not carry. That
// endpoint still owns open interest, which this one does not return —
// combine both by strike for a complete chain (OI + IV + Greeks).
type OptionGreek struct {
	Name              string
	Expiry            string
	StrikePrice       decimal.Decimal
	OptionType        string // CE | PE
	Delta             decimal.Decimal
	Gamma             decimal.Decimal
	Theta             decimal.Decimal
	Vega              decimal.Decimal
	ImpliedVolatility decimal.Decimal
	TradeVolume       int64
}

type optionGreekRequest struct {
	Name   string `json:"name"`
	Expiry string `json:"expirydate"`
}

type optionGreekResponse struct {
	Status bool `json:"status"`
	Data   []struct {
		Name              string `json:"name"`
		Expiry            string `json:"expiry"`
		StrikePrice       string `json:"strikePrice"`
		OptionType        string `json:"optionType"`
		Delta             string `json:"delta"`
		Gamma             string `json:"gamma"`
		Theta             string `json:"theta"`
		Vega              string `json:"vega"`
		ImpliedVolatility string `json:"impliedVolatility"`
		TradeVolume       string `json:"tradeVolume"`
	} `json:"data"`
}

// GetOptionGreeks fetches Greeks + IV for every strike of one underlying's
// expiry. expiry format must match Angel One's convention, e.g. "28AUG2026"
// (same format as the scrip master's Expiry field).
func (c *Client) GetOptionGreeks(ctx context.Context, underlying, expiry string) ([]OptionGreek, error) {
	req := optionGreekRequest{Name: underlying, Expiry: expiry}
	var resp optionGreekResponse
	if err := c.request(ctx, http.MethodPost, "/rest/secure/angelbroking/marketData/v1/optionGreek", req, &resp); err != nil {
		return nil, err
	}

	out := make([]OptionGreek, 0, len(resp.Data))
	for _, d := range resp.Data {
		out = append(out, OptionGreek{
			Name: d.Name, Expiry: d.Expiry, OptionType: d.OptionType,
			StrikePrice:       parseDec(d.StrikePrice),
			Delta:             parseDec(d.Delta),
			Gamma:             parseDec(d.Gamma),
			Theta:             parseDec(d.Theta),
			Vega:              parseDec(d.Vega),
			ImpliedVolatility: parseDec(d.ImpliedVolatility),
			TradeVolume:       parseInt(d.TradeVolume),
		})
	}
	return out, nil
}

func parseDec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}
func parseInt(s string) int64 {
	d, _ := decimal.NewFromString(s)
	return d.IntPart()
}
