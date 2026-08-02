package indicators

import (
	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

// atrState is Wilder-smoothed average true range, shared by the atr,
// adx, and supertrend indicators.
type atrState struct {
	period    int
	prevClose float64
	hasPrev   bool
	seedR     *ring
	seeded    bool
	value     float64
}

func newATRState(period int) *atrState {
	return &atrState{period: period, seedR: newRing(period)}
}

func (a *atrState) trueRange(high, low, close float64) float64 {
	if !a.hasPrev {
		return high - low
	}
	tr := high - low
	if d := abs(high - a.prevClose); d > tr {
		tr = d
	}
	if d := abs(low - a.prevClose); d > tr {
		tr = d
	}
	return tr
}

func (a *atrState) update(high, low, close float64) float64 {
	tr := a.trueRange(high, low, close)
	a.hasPrev = true
	a.prevClose = close

	if !a.seeded {
		a.seedR.push(tr)
		a.value = a.seedR.mean()
		if a.seedR.full() {
			a.seeded = true
		}
		return a.value
	}
	a.value = (a.value*float64(a.period-1) + tr) / float64(a.period)
	return a.value
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

type atrIndicator struct {
	s    *atrState
	prev float64
}

func newATR(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 14)
	return &atrIndicator{s: newATRState(period)}, nil
}

func (i *atrIndicator) Update(c models.Candle) dsl.Signal {
	v := i.s.update(f64(c.High), f64(c.Low), f64(c.Close))
	prev := i.prev
	i.prev = v
	return dsl.Signal{Value: v, Prev: prev, Flags: map[string]bool{}}
}

// adxIndicator implements Wilder's ADX: smoothed +DM/-DM feed +DI/-DI,
// DX is smoothed into ADX.
type adxIndicator struct {
	period            int
	atr               *atrState
	prevHigh, prevLow float64
	hasPrev           bool
	avgPlusDM         float64
	avgMinusDM        float64
	dmSeedPlus        *ring
	dmSeedMinus       *ring
	seeded            bool
	adxR              *ring
	adx               float64
	prevADX           float64
}

func newADX(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 14)
	return &adxIndicator{
		period:      period,
		atr:         newATRState(period),
		dmSeedPlus:  newRing(period),
		dmSeedMinus: newRing(period),
		adxR:        newRing(period),
	}, nil
}

func (i *adxIndicator) Update(c models.Candle) dsl.Signal {
	high, low, close := f64(c.High), f64(c.Low), f64(c.Close)
	atr := i.atr.update(high, low, close)

	var plusDM, minusDM float64
	if i.hasPrev {
		upMove := high - i.prevHigh
		downMove := i.prevLow - low
		if upMove > downMove && upMove > 0 {
			plusDM = upMove
		}
		if downMove > upMove && downMove > 0 {
			minusDM = downMove
		}
	}
	i.hasPrev = true
	i.prevHigh, i.prevLow = high, low

	if !i.seeded {
		i.dmSeedPlus.push(plusDM)
		i.dmSeedMinus.push(minusDM)
		if i.dmSeedPlus.full() {
			i.avgPlusDM = i.dmSeedPlus.mean()
			i.avgMinusDM = i.dmSeedMinus.mean()
			i.seeded = true
		}
	} else {
		i.avgPlusDM = (i.avgPlusDM*float64(i.period-1) + plusDM) / float64(i.period)
		i.avgMinusDM = (i.avgMinusDM*float64(i.period-1) + minusDM) / float64(i.period)
	}

	var plusDI, minusDI, dx float64
	if atr != 0 {
		plusDI = 100 * i.avgPlusDM / atr
		minusDI = 100 * i.avgMinusDM / atr
	}
	if plusDI+minusDI != 0 {
		dx = 100 * abs(plusDI-minusDI) / (plusDI + minusDI)
	}

	i.adxR.push(dx)
	if i.adxR.full() && i.adx == 0 {
		i.adx = i.adxR.mean()
	} else if i.adx != 0 {
		i.adx = (i.adx*float64(i.period-1) + dx) / float64(i.period)
	}

	prev := i.prevADX
	i.prevADX = i.adx
	return dsl.Signal{Value: i.adx, Prev: prev, Flags: map[string]bool{}}
}

// superTrendIndicator: standard SuperTrend using ATR bands with trend
// continuation rules.
type superTrendIndicator struct {
	atr        *atrState
	multiplier float64
	finalUpper float64
	finalLower float64
	trendUp    bool
	hasPrev    bool
	prevClose  float64
}

func newSuperTrend(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 10)
	mult := param(params, "multiplier", 3.0)
	return &superTrendIndicator{atr: newATRState(period), multiplier: mult, trendUp: true}, nil
}

func (i *superTrendIndicator) Update(c models.Candle) dsl.Signal {
	high, low, close := f64(c.High), f64(c.Low), f64(c.Close)
	atr := i.atr.update(high, low, close)
	mid := (high + low) / 2
	basicUpper := mid + i.multiplier*atr
	basicLower := mid - i.multiplier*atr

	flags := map[string]bool{}
	if !i.hasPrev {
		i.finalUpper = basicUpper
		i.finalLower = basicLower
		i.hasPrev = true
		i.prevClose = close
		return dsl.Signal{Value: 0, Flags: flags}
	}

	if basicUpper < i.finalUpper || i.prevClose > i.finalUpper {
		i.finalUpper = basicUpper
	}
	if basicLower > i.finalLower || i.prevClose < i.finalLower {
		i.finalLower = basicLower
	}

	wasUp := i.trendUp
	if i.trendUp && close < i.finalLower {
		i.trendUp = false
	} else if !i.trendUp && close > i.finalUpper {
		i.trendUp = true
	}

	flags["bullish"] = i.trendUp && !wasUp
	flags["bearish"] = !i.trendUp && wasUp

	i.prevClose = close
	value := i.finalLower
	if !i.trendUp {
		value = i.finalUpper
	}
	return dsl.Signal{Value: value, Flags: flags}
}

func init() {
	register("atr", newATR)
	register("adx", newADX)
	register("supertrend", newSuperTrend)
}
