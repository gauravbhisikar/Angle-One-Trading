package indicators

import (
	"math"

	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

type smaIndicator struct {
	r    *ring
	prev float64
}

func newSMA(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 20)
	return &smaIndicator{r: newRing(period)}, nil
}

func (i *smaIndicator) Update(c models.Candle) dsl.Signal {
	i.r.push(f64(c.Close))
	v := i.r.mean()
	prev := i.prev
	i.prev = v
	return dsl.Signal{Value: v, Prev: prev, Flags: map[string]bool{}}
}

type bollingerIndicator struct {
	r      *ring
	stdDev float64
}

func newBollinger(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 20)
	std := param(params, "std_dev", 2.0)
	return &bollingerIndicator{r: newRing(period), stdDev: std}, nil
}

func (i *bollingerIndicator) Update(c models.Candle) dsl.Signal {
	close := f64(c.Close)
	i.r.push(close)
	mean := i.r.mean()

	var variance float64
	vals := i.r.values()
	for _, v := range vals {
		d := v - mean
		variance += d * d
	}
	if len(vals) > 0 {
		variance /= float64(len(vals))
	}
	std := math.Sqrt(variance)

	upper := mean + i.stdDev*std
	lower := mean - i.stdDev*std
	var percentB float64
	if upper != lower {
		percentB = (close - lower) / (upper - lower)
	}

	flags := map[string]bool{
		"price_above_upper": close > upper,
		"price_below_lower": close < lower,
	}
	if mean != 0 {
		flags["squeeze"] = (upper-lower)/mean < 0.05
	}

	return dsl.Signal{Value: percentB, Flags: flags}
}

func init() {
	register("sma", newSMA)
	register("bollinger_bands", newBollinger)
}
