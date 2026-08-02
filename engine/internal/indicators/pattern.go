package indicators

import (
	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

type ohlc struct{ open, high, low, close float64 }

// patternIndicator detects one named single/multi-candle pattern per
// update, keeping only the last 3 bars (bounded memory, ENGINE_SPEC Sec 0.6).
type patternIndicator struct {
	name string
	hist []ohlc // most recent last
}

func newPattern(_ map[string]float64, patternName string) (Indicator, error) {
	return &patternIndicator{name: patternName}, nil
}

func (i *patternIndicator) Update(c models.Candle) dsl.Signal {
	cur := ohlc{f64(c.Open), f64(c.High), f64(c.Low), f64(c.Close)}
	i.hist = append(i.hist, cur)
	if len(i.hist) > 3 {
		i.hist = i.hist[len(i.hist)-3:]
	}

	detected := i.detect()
	return dsl.Signal{Flags: map[string]bool{"true": detected}}
}

func (i *patternIndicator) detect() bool {
	n := len(i.hist)
	cur := i.hist[n-1]
	body := abs(cur.close - cur.open)
	rng := cur.high - cur.low
	if rng == 0 {
		rng = 1e-9
	}

	switch i.name {
	case "doji":
		return body <= 0.1*rng
	case "hammer":
		lowerShadow := min2(cur.open, cur.close) - cur.low
		upperShadow := cur.high - max2(cur.open, cur.close)
		return lowerShadow >= 2*body && upperShadow <= 0.3*body && body > 0
	case "shooting_star":
		upperShadow := cur.high - max2(cur.open, cur.close)
		lowerShadow := min2(cur.open, cur.close) - cur.low
		return upperShadow >= 2*body && lowerShadow <= 0.3*body && body > 0
	case "bullish_engulfing":
		if n < 2 {
			return false
		}
		prev := i.hist[n-2]
		return prev.close < prev.open && cur.close > cur.open &&
			cur.open <= prev.close && cur.close >= prev.open
	case "bearish_engulfing":
		if n < 2 {
			return false
		}
		prev := i.hist[n-2]
		return prev.close > prev.open && cur.close < cur.open &&
			cur.open >= prev.close && cur.close <= prev.open
	case "harami":
		if n < 2 {
			return false
		}
		prev := i.hist[n-2]
		prevBody := abs(prev.close - prev.open)
		return body < prevBody && max2(cur.open, cur.close) <= max2(prev.open, prev.close) &&
			min2(cur.open, cur.close) >= min2(prev.open, prev.close)
	case "morning_star":
		if n < 3 {
			return false
		}
		first, second := i.hist[n-3], i.hist[n-2]
		firstBearish := first.close < first.open
		secondSmall := abs(second.close-second.open) < abs(first.close-first.open)*0.5
		thirdBullish := cur.close > cur.open
		closesAboveMid := cur.close > (first.open+first.close)/2
		return firstBearish && secondSmall && thirdBullish && closesAboveMid
	case "evening_star":
		if n < 3 {
			return false
		}
		first, second := i.hist[n-3], i.hist[n-2]
		firstBullish := first.close > first.open
		secondSmall := abs(second.close-second.open) < abs(first.close-first.open)*0.5
		thirdBearish := cur.close < cur.open
		closesBelowMid := cur.close < (first.open+first.close)/2
		return firstBullish && secondSmall && thirdBearish && closesBelowMid
	}
	return false
}

func min2(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func max2(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func init() {
	register("pattern", newPattern)
}
