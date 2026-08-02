package indicators

import (
	"math"

	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

type rsiIndicator struct {
	period    int
	prevClose float64
	hasPrev   bool
	avgGain   float64
	avgLoss   float64
	seeded    bool
	gains     *ring
	losses    *ring
	prevRSI   float64
}

func newRSI(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 14)
	return &rsiIndicator{period: period, gains: newRing(period), losses: newRing(period)}, nil
}

func (i *rsiIndicator) step(close float64) float64 {
	if !i.hasPrev {
		i.prevClose = close
		i.hasPrev = true
		return 50
	}
	change := close - i.prevClose
	i.prevClose = close

	gain, loss := 0.0, 0.0
	if change > 0 {
		gain = change
	} else {
		loss = -change
	}

	if !i.seeded {
		i.gains.push(gain)
		i.losses.push(loss)
		if i.gains.full() {
			i.avgGain = i.gains.mean()
			i.avgLoss = i.losses.mean()
			i.seeded = true
		} else {
			return 50
		}
	} else {
		i.avgGain = (i.avgGain*float64(i.period-1) + gain) / float64(i.period)
		i.avgLoss = (i.avgLoss*float64(i.period-1) + loss) / float64(i.period)
	}

	if i.avgLoss == 0 {
		return 100
	}
	rs := i.avgGain / i.avgLoss
	return 100 - (100 / (1 + rs))
}

func (i *rsiIndicator) Update(c models.Candle) dsl.Signal {
	rsi := i.step(f64(c.Close))
	prev := i.prevRSI
	i.prevRSI = rsi
	return dsl.Signal{Value: rsi, Prev: prev, Flags: map[string]bool{}}
}

type stochRSIIndicator struct {
	rsi   *rsiIndicator
	rsiR  *ring
	kR    *ring
	prevD float64
}

func newStochRSI(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 14)
	k := paramInt(params, "k", 3)
	return &stochRSIIndicator{
		rsi:  &rsiIndicator{period: period, gains: newRing(period), losses: newRing(period)},
		rsiR: newRing(period),
		kR:   newRing(k),
	}, nil
}

func (i *stochRSIIndicator) Update(c models.Candle) dsl.Signal {
	rsi := i.rsi.step(f64(c.Close))
	i.rsiR.push(rsi)

	lo, hi := i.rsiR.min(), i.rsiR.max()
	percentK := 50.0
	if hi != lo {
		percentK = (rsi - lo) / (hi - lo) * 100
	}
	i.kR.push(percentK)
	percentD := i.kR.mean()

	prev := i.prevD
	i.prevD = percentD
	return dsl.Signal{Value: percentD, Prev: prev, Flags: map[string]bool{}}
}

type rocIndicator struct {
	r    *ring
	prev float64
}

func newROC(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 12)
	return &rocIndicator{r: newRing(period)}, nil
}

func (i *rocIndicator) Update(c models.Candle) dsl.Signal {
	close := f64(c.Close)
	var roc float64
	if i.r.full() {
		nAgo := i.r.oldest()
		if nAgo != 0 {
			roc = (close - nAgo) / nAgo * 100
		}
	}
	i.r.push(close)
	prev := i.prev
	i.prev = roc
	return dsl.Signal{Value: roc, Prev: prev, Flags: map[string]bool{}}
}

type cciIndicator struct {
	r    *ring
	prev float64
}

func newCCI(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 20)
	return &cciIndicator{r: newRing(period)}, nil
}

func (i *cciIndicator) Update(c models.Candle) dsl.Signal {
	tp := (f64(c.High) + f64(c.Low) + f64(c.Close)) / 3
	i.r.push(tp)
	mean := i.r.mean()

	var meanDev float64
	vals := i.r.values()
	for _, v := range vals {
		meanDev += math.Abs(v - mean)
	}
	if len(vals) > 0 {
		meanDev /= float64(len(vals))
	}

	var cci float64
	if meanDev != 0 {
		cci = (tp - mean) / (0.015 * meanDev)
	}
	prev := i.prev
	i.prev = cci
	return dsl.Signal{Value: cci, Prev: prev, Flags: map[string]bool{}}
}

type mfiIndicator struct {
	period  int
	prevTP  float64
	hasPrev bool
	posFlow *ring
	negFlow *ring
	prevMFI float64
}

func newMFI(params map[string]float64, _ string) (Indicator, error) {
	period := paramInt(params, "period", 14)
	return &mfiIndicator{period: period, posFlow: newRing(period), negFlow: newRing(period)}, nil
}

func (i *mfiIndicator) Update(c models.Candle) dsl.Signal {
	tp := (f64(c.High) + f64(c.Low) + f64(c.Close)) / 3
	rawMF := tp * float64(c.Volume)

	pos, neg := 0.0, 0.0
	if i.hasPrev {
		if tp > i.prevTP {
			pos = rawMF
		} else if tp < i.prevTP {
			neg = rawMF
		}
	}
	i.hasPrev = true
	i.prevTP = tp
	i.posFlow.push(pos)
	i.negFlow.push(neg)

	mfi := 50.0
	if i.negFlow.total() != 0 {
		ratio := i.posFlow.total() / i.negFlow.total()
		mfi = 100 - 100/(1+ratio)
	} else if i.posFlow.total() > 0 {
		mfi = 100
	}

	prev := i.prevMFI
	i.prevMFI = mfi
	return dsl.Signal{Value: mfi, Prev: prev, Flags: map[string]bool{}}
}

func init() {
	register("rsi", newRSI)
	register("stochastic_rsi", newStochRSI)
	register("roc", newROC)
	register("cci", newCCI)
	register("mfi", newMFI)
}
