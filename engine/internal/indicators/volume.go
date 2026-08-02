package indicators

import (
	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

// volumeIndicator's Value is the current bar's volume expressed as a
// percent of the trailing average (e.g. 150 = 1.5x average volume) — this
// is what both "<"/">" and "spike_pct" operators compare against.
type volumeIndicator struct {
	r    *ring
	prev float64
}

func newVolume(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 20)
	return &volumeIndicator{r: newRing(period)}, nil
}

func (i *volumeIndicator) Update(c models.Candle) dsl.Signal {
	vol := float64(c.Volume)
	var pct float64
	if avg := i.r.mean(); avg > 0 {
		pct = vol / avg * 100
	}
	i.r.push(vol)
	prev := i.prev
	i.prev = pct
	return dsl.Signal{Value: pct, Prev: prev, Flags: map[string]bool{}}
}

type obvIndicator struct {
	obv       float64
	prevClose float64
	hasPrev   bool
	prevOBV   float64
}

func newOBV(map[string]float64, string) (Indicator, error) {
	return &obvIndicator{}, nil
}

func (i *obvIndicator) Update(c models.Candle) dsl.Signal {
	close := f64(c.Close)
	if i.hasPrev {
		if close > i.prevClose {
			i.obv += float64(c.Volume)
		} else if close < i.prevClose {
			i.obv -= float64(c.Volume)
		}
	}
	i.hasPrev = true
	i.prevClose = close

	flags := map[string]bool{
		"rising":  i.obv > i.prevOBV,
		"falling": i.obv < i.prevOBV,
	}
	prev := i.prevOBV
	i.prevOBV = i.obv
	return dsl.Signal{Value: i.obv, Prev: prev, Flags: flags}
}

func init() {
	register("volume", newVolume)
	register("obv", newOBV)
}
