package featurestore

import (
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/indicators"
	"tradingengine/internal/models"
)

// Candle is featurestore's own input type (decoupled from models.Candle,
// which is what it converts to internally) so callers outside the engine
// module boundary aren't forced to construct engine-internal types.
type Candle struct {
	Date   time.Time
	Open   decimal.Decimal
	High   decimal.Decimal
	Low    decimal.Decimal
	Close  decimal.Decimal
	Volume int64
}

type FeatureRow struct {
	Symbol            string
	Date              string // YYYY-MM-DD
	Close             decimal.Decimal
	RSI14             decimal.Decimal
	EMA20             decimal.Decimal
	EMA50             decimal.Decimal
	EMACrossBullish   *bool
	EMACrossBearish   *bool
	MACDBullish       *bool
	MACDBearish       *bool
	ADX14             decimal.Decimal
	ATR14             decimal.Decimal
	BollingerPercentB decimal.Decimal
}

// ComputeTechnical walks candles (must be sorted oldest first, one
// symbol) once, updating one instance of each indicator per day — same
// O(1)-per-candle incremental approach the live engine uses
// (ENGINE_SPEC Sec 0.4), not a full recompute per day. Returns one
// FeatureRow per candle.
func ComputeTechnical(symbol string, candles []Candle) ([]FeatureRow, error) {
	rsi, err := indicators.New("rsi", map[string]float64{"period": 14}, "")
	if err != nil {
		return nil, err
	}
	ema20, err := indicators.New("ema", map[string]float64{"period": 20}, "")
	if err != nil {
		return nil, err
	}
	ema50, err := indicators.New("ema", map[string]float64{"period": 50}, "")
	if err != nil {
		return nil, err
	}
	emaCross, err := indicators.New("ema_cross", map[string]float64{"fast": 20, "slow": 50}, "")
	if err != nil {
		return nil, err
	}
	macd, err := indicators.New("macd", map[string]float64{"fast": 12, "slow": 26, "signal": 9}, "")
	if err != nil {
		return nil, err
	}
	adx, err := indicators.New("adx", map[string]float64{"period": 14}, "")
	if err != nil {
		return nil, err
	}
	atr, err := indicators.New("atr", map[string]float64{"period": 14}, "")
	if err != nil {
		return nil, err
	}
	bb, err := indicators.New("bollinger_bands", map[string]float64{"period": 20, "std_dev": 2}, "")
	if err != nil {
		return nil, err
	}

	out := make([]FeatureRow, 0, len(candles))
	for _, c := range candles {
		mc := models.Candle{
			Symbol: symbol, Timeframe: models.TF1d,
			OpenTime: c.Date, CloseTime: c.Date,
			Open: c.Open, High: c.High, Low: c.Low, Close: c.Close, Volume: c.Volume, Closed: true,
		}

		rsiSig := rsi.Update(mc)
		ema20Sig := ema20.Update(mc)
		ema50Sig := ema50.Update(mc)
		crossSig := emaCross.Update(mc)
		macdSig := macd.Update(mc)
		adxSig := adx.Update(mc)
		atrSig := atr.Update(mc)
		bbSig := bb.Update(mc)

		row := FeatureRow{
			Symbol: symbol, Date: c.Date.Format("2006-01-02"), Close: c.Close,
			RSI14: decimal.NewFromFloat(rsiSig.Value), EMA20: decimal.NewFromFloat(ema20Sig.Value),
			EMA50: decimal.NewFromFloat(ema50Sig.Value), ADX14: decimal.NewFromFloat(adxSig.Value),
			ATR14: decimal.NewFromFloat(atrSig.Value), BollingerPercentB: decimal.NewFromFloat(bbSig.Value),
		}
		if crossSig.Flags["bullish"] {
			t := true
			row.EMACrossBullish = &t
		}
		if crossSig.Flags["bearish"] {
			t := true
			row.EMACrossBearish = &t
		}
		if macdSig.Flags["bullish"] {
			t := true
			row.MACDBullish = &t
		}
		if macdSig.Flags["bearish"] {
			t := true
			row.MACDBearish = &t
		}
		out = append(out, row)
	}
	return out, nil
}
