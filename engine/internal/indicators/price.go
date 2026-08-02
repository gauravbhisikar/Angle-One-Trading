package indicators

import (
	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

type ohlcField int

const (
	fieldClose ohlcField = iota
	fieldOpen
	fieldHigh
	fieldLow
)

type priceIndicator struct {
	field ohlcField
	prev  float64
}

func newPriceFactory(field ohlcField) Factory {
	return func(map[string]float64, string) (Indicator, error) {
		return &priceIndicator{field: field}, nil
	}
}

func (i *priceIndicator) Update(c models.Candle) dsl.Signal {
	var v float64
	switch i.field {
	case fieldClose:
		v = f64(c.Close)
	case fieldOpen:
		v = f64(c.Open)
	case fieldHigh:
		v = f64(c.High)
	case fieldLow:
		v = f64(c.Low)
	}
	prev := i.prev
	i.prev = v
	return dsl.Signal{Value: v, Prev: prev, Flags: map[string]bool{}}
}

// prevHigh/prevLow freeze the prior bar's high/low as a level and report
// crosses_above/crosses_below of the current close against that frozen
// level directly as flags, since these are almost always used without an
// explicit compare_to (DSL_SPEC Sec 6).
type prevLevelIndicator struct {
	isHigh    bool
	level     float64
	hasLevel  bool
	prevClose float64
	hasPrev   bool
}

func newPrevHigh(map[string]float64, string) (Indicator, error) {
	return &prevLevelIndicator{isHigh: true}, nil
}
func newPrevLow(map[string]float64, string) (Indicator, error) {
	return &prevLevelIndicator{isHigh: false}, nil
}

func (i *prevLevelIndicator) Update(c models.Candle) dsl.Signal {
	close := f64(c.Close)
	flags := map[string]bool{}
	if i.hasLevel && i.hasPrev {
		if i.isHigh {
			flags["crosses_above"] = i.prevClose <= i.level && close > i.level
		} else {
			flags["crosses_below"] = i.prevClose >= i.level && close < i.level
		}
	}
	level := i.level
	if i.isHigh {
		i.level = f64(c.High)
	} else {
		i.level = f64(c.Low)
	}
	i.hasLevel = true
	i.prevClose = close
	i.hasPrev = true
	return dsl.Signal{Value: level, Flags: flags}
}

// gapIndicator compares this bar's open against the prior bar's close.
type gapIndicator struct {
	isUp      bool
	minPct    float64
	prevClose float64
	hasPrev   bool
}

func newGapUp(params map[string]float64, _ string) (Indicator, error) {
	return &gapIndicator{isUp: true, minPct: param(params, "min_pct", 0)}, nil
}
func newGapDown(params map[string]float64, _ string) (Indicator, error) {
	return &gapIndicator{isUp: false, minPct: param(params, "min_pct", 0)}, nil
}

func (i *gapIndicator) Update(c models.Candle) dsl.Signal {
	open := f64(c.Open)
	flags := map[string]bool{}
	var gapPct float64
	if i.hasPrev && i.prevClose != 0 {
		gapPct = (open - i.prevClose) / i.prevClose * 100
		if i.isUp {
			flags["true"] = gapPct >= i.minPct
		} else {
			flags["true"] = -gapPct >= i.minPct
		}
	}
	i.prevClose = f64(c.Close)
	i.hasPrev = true
	return dsl.Signal{Value: gapPct, Flags: flags}
}

func init() {
	register("price", newPriceFactory(fieldClose))
	register("close", newPriceFactory(fieldClose))
	register("open", newPriceFactory(fieldOpen))
	register("high", newPriceFactory(fieldHigh))
	register("low", newPriceFactory(fieldLow))
	register("prev_high", newPrevHigh)
	register("prev_low", newPrevLow)
	register("gap_up", newGapUp)
	register("gap_down", newGapDown)
}
