package indicators

import (
	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

type highestHighIndicator struct {
	r    *ring
	prev float64
}

func newHighestHigh(params map[string]float64, _ string) (Indicator, error) {
	return &highestHighIndicator{r: newRing(paramInt(params, "lookback", 20))}, nil
}

func (i *highestHighIndicator) Update(c models.Candle) dsl.Signal {
	i.r.push(f64(c.High))
	v := i.r.max()
	prev := i.prev
	i.prev = v
	return dsl.Signal{Value: v, Prev: prev, Flags: map[string]bool{}}
}

type lowestLowIndicator struct {
	r    *ring
	prev float64
}

func newLowestLow(params map[string]float64, _ string) (Indicator, error) {
	return &lowestLowIndicator{r: newRing(paramInt(params, "lookback", 20))}, nil
}

func (i *lowestLowIndicator) Update(c models.Candle) dsl.Signal {
	i.r.push(f64(c.Low))
	v := i.r.min()
	prev := i.prev
	i.prev = v
	return dsl.Signal{Value: v, Prev: prev, Flags: map[string]bool{}}
}

type donchianIndicator struct {
	highs     *ring
	lows      *ring
	prevUpper float64
	prevLower float64
	hasPrev   bool
}

func newDonchian(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 20)
	return &donchianIndicator{highs: newRing(period), lows: newRing(period)}, nil
}

func (i *donchianIndicator) Update(c models.Candle) dsl.Signal {
	high, low, close := f64(c.High), f64(c.Low), f64(c.Close)

	flags := map[string]bool{
		"price_above_upper": i.hasPrev && close > i.prevUpper,
		"price_below_lower": i.hasPrev && close < i.prevLower,
		"breakout_up":       i.hasPrev && high > i.prevUpper,
		"breakout_down":     i.hasPrev && low < i.prevLower,
	}

	i.highs.push(high)
	i.lows.push(low)
	upper, lower := i.highs.max(), i.lows.min()
	i.prevUpper, i.prevLower = upper, lower
	i.hasPrev = true

	return dsl.Signal{Value: (upper + lower) / 2, Flags: flags}
}

// supportResistanceIndicator: lookback swing low/high with an approximate
// "bounce" heuristic (price touched near the level and closed back away
// from it). A pragmatic approximation, not classical pivot-point detection
// — refine iteratively per DSL_SPEC Sec 6's "pluggable" design.
type supportResistanceIndicator struct {
	isSupport bool
	r         *ring
	prev      float64
}

func newSupport(params map[string]float64, _ string) (Indicator, error) {
	return &supportResistanceIndicator{isSupport: true, r: newRing(paramInt(params, "lookback", 20))}, nil
}
func newResistance(params map[string]float64, _ string) (Indicator, error) {
	return &supportResistanceIndicator{isSupport: false, r: newRing(paramInt(params, "lookback", 20))}, nil
}

func (i *supportResistanceIndicator) Update(c models.Candle) dsl.Signal {
	high, low, open, close := f64(c.High), f64(c.Low), f64(c.Open), f64(c.Close)

	var level float64
	if i.isSupport {
		level = i.r.min()
	} else {
		level = i.r.max()
	}

	flags := map[string]bool{}
	const tolerance = 0.003
	if level != 0 {
		if i.isSupport {
			touched := (low-level)/level <= tolerance
			flags["bounce"] = touched && close > open
		} else {
			touched := (level-high)/level <= tolerance
			flags["bounce"] = touched && close < open
		}
	}

	if i.isSupport {
		i.r.push(low)
	} else {
		i.r.push(high)
	}
	prev := i.prev
	i.prev = level
	return dsl.Signal{Value: level, Prev: prev, Flags: flags}
}

func init() {
	register("highest_high", newHighestHigh)
	register("lowest_low", newLowestLow)
	register("donchian_channel", newDonchian)
	register("support", newSupport)
	register("resistance", newResistance)
}
