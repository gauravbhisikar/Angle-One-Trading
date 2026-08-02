package indicators

import (
	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

// vwapIndicator resets at the start of each trading session (day) — VWAP
// is a session-cumulative measure, not a rolling one.
type vwapIndicator struct {
	day    int
	cumPV  float64
	cumVol float64
	prev   float64
}

func newVWAP(map[string]float64, string) (Indicator, error) {
	return &vwapIndicator{day: -1}, nil
}

func (i *vwapIndicator) Update(c models.Candle) dsl.Signal {
	day := c.OpenTime.YearDay() + c.OpenTime.Year()*1000
	if day != i.day {
		i.day = day
		i.cumPV = 0
		i.cumVol = 0
	}

	tp := (f64(c.High) + f64(c.Low) + f64(c.Close)) / 3
	i.cumPV += tp * float64(c.Volume)
	i.cumVol += float64(c.Volume)

	var vwap float64
	if i.cumVol > 0 {
		vwap = i.cumPV / i.cumVol
	}
	prev := i.prev
	i.prev = vwap
	return dsl.Signal{Value: vwap, Prev: prev, Flags: map[string]bool{}}
}

func init() {
	register("vwap", newVWAP)
}
