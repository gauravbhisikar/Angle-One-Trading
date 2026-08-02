package indicators

import (
	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

type emaState struct {
	period    int
	alpha     float64
	value     float64
	prev      float64
	seeded    bool
	seedSum   float64
	seedCount int
}

func newEMAState(period int) *emaState {
	return &emaState{period: period, alpha: 2.0 / (float64(period) + 1.0)}
}

func (e *emaState) update(price float64) (value, prev float64) {
	prev = e.value
	if !e.seeded {
		e.seedSum += price
		e.seedCount++
		if e.seedCount >= e.period {
			e.value = e.seedSum / float64(e.seedCount)
			e.seeded = true
		} else {
			e.value = e.seedSum / float64(e.seedCount) // running mean until fully seeded
		}
		e.prev = e.value
		return e.value, e.value
	}
	e.value = prev + e.alpha*(price-prev)
	return e.value, prev
}

type emaIndicator struct{ s *emaState }

func newEMA(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 20)
	return &emaIndicator{s: newEMAState(period)}, nil
}

func (i *emaIndicator) Update(c models.Candle) dsl.Signal {
	price := f64(c.Close)
	v, p := i.s.update(price)
	return dsl.Signal{Value: v, Prev: p, Flags: map[string]bool{}}
}

type emaCrossIndicator struct {
	fast, slow         *emaState
	prevFast, prevSlow float64
	hasPrev            bool
}

func newEMACross(params map[string]float64, _ string) (Indicator, error) {
	fast := paramInt(params, "fast", 20)
	slow := paramInt(params, "slow", 50)
	return &emaCrossIndicator{fast: newEMAState(fast), slow: newEMAState(slow)}, nil
}

func (i *emaCrossIndicator) Update(c models.Candle) dsl.Signal {
	price := f64(c.Close)
	fv, _ := i.fast.update(price)
	sv, _ := i.slow.update(price)

	flags := map[string]bool{}
	if i.hasPrev {
		flags["bullish"] = i.prevFast <= i.prevSlow && fv > sv
		flags["bearish"] = i.prevFast >= i.prevSlow && fv < sv
	}
	diff := fv - sv
	prevDiff := i.prevFast - i.prevSlow
	i.prevFast, i.prevSlow = fv, sv
	i.hasPrev = true
	return dsl.Signal{Value: diff, Prev: prevDiff, Flags: flags}
}

type macdIndicator struct {
	fast, slow, signal *emaState
	prevMACD, prevSig  float64
	hasPrev            bool
}

func newMACD(params map[string]float64, _ string) (Indicator, error) {
	fast := paramInt(params, "fast", 12)
	slow := paramInt(params, "slow", 26)
	sig := paramInt(params, "signal", 9)
	return &macdIndicator{fast: newEMAState(fast), slow: newEMAState(slow), signal: newEMAState(sig)}, nil
}

func (i *macdIndicator) Update(c models.Candle) dsl.Signal {
	price := f64(c.Close)
	fv, _ := i.fast.update(price)
	sv, _ := i.slow.update(price)
	macdLine := fv - sv
	sigLine, _ := i.signal.update(macdLine)

	flags := map[string]bool{}
	if i.hasPrev {
		flags["bullish"] = i.prevMACD <= i.prevSig && macdLine > sigLine
		flags["bearish"] = i.prevMACD >= i.prevSig && macdLine < sigLine
		flags["crosses_above"] = flags["bullish"]
		flags["crosses_below"] = flags["bearish"]
	}
	prevMACD := i.prevMACD
	i.prevMACD, i.prevSig = macdLine, sigLine
	i.hasPrev = true
	return dsl.Signal{Value: macdLine, Prev: prevMACD, Flags: flags}
}

func init() {
	register("ema", newEMA)
	register("ema_cross", newEMACross)
	register("macd", newMACD)
}
