package marketdata

import (
	"sync"
	"time"

	"tradingengine/internal/models"
)

// OnClose is invoked once per closed candle for one (symbol, timeframe)
// pair. Callers (indicator cache, strategy runtime) never receive
// duplicate work per subscriber — the builder computes each candle once
// and fans it out (ENGINE_SPEC Sec 0.3-0.4).
type OnClose func(symbol string, timeframe models.Timeframe, candle models.Candle)

// OneMinuteBuilder is the single global entry point from ticks to candles.
// One instance handles every symbol — not one instance per symbol per
// strategy — so subscribing symbol #2 through #10 costs a map entry, not a
// new pipeline.
type OneMinuteBuilder struct {
	mu      sync.Mutex
	current map[string]*models.Candle
	onClose OnClose
}

func NewOneMinuteBuilder(onClose OnClose) *OneMinuteBuilder {
	return &OneMinuteBuilder{current: map[string]*models.Candle{}, onClose: onClose}
}

func (b *OneMinuteBuilder) OnTick(t models.Tick) {
	minute := t.Timestamp.Truncate(time.Minute)

	b.mu.Lock()
	cur, ok := b.current[t.Symbol]
	if !ok || cur.OpenTime.Before(minute) {
		if ok {
			cur.Closed = true
			cur.CloseTime = cur.OpenTime.Add(time.Minute)
			closed := *cur
			b.mu.Unlock()
			b.onClose(t.Symbol, models.TF1m, closed)
			b.mu.Lock()
		}
		cur = &models.Candle{
			Symbol: t.Symbol, Timeframe: models.TF1m,
			OpenTime: minute, Open: t.Price, High: t.Price, Low: t.Price, Close: t.Price,
		}
		b.current[t.Symbol] = cur
	}
	if t.Price.GreaterThan(cur.High) {
		cur.High = t.Price
	}
	if t.Price.LessThan(cur.Low) {
		cur.Low = t.Price
	}
	cur.Close = t.Price
	cur.Volume += t.Volume
	b.mu.Unlock()
}

// Aggregator builds one higher timeframe (5m/15m/30m/1h/4h/1d/1w) from the
// 1-minute candle-close stream. One instance per timeframe, shared by
// every symbol and every strategy referencing that timeframe.
type Aggregator struct {
	mu        sync.Mutex
	timeframe models.Timeframe
	minutes   int // 0 for session-based (1d/1w)
	current   map[string]*bucket
	onClose   OnClose
}

type bucket struct {
	candle    models.Candle
	bucketKey string
}

func NewAggregator(tf models.Timeframe, onClose OnClose) *Aggregator {
	return &Aggregator{
		timeframe: tf,
		minutes:   models.TimeframeMinutes[tf],
		current:   map[string]*bucket{},
		onClose:   onClose,
	}
}

func (a *Aggregator) bucketKeyFor(t time.Time) string {
	switch a.timeframe {
	case models.TF1d:
		return t.Format("2006-01-02")
	case models.TF1w:
		y, w := t.ISOWeek()
		return time.Date(y, 1, 1, 0, 0, 0, 0, t.Location()).Format("2006") + "-W" + itoa(w)
	default:
		// Fixed N-minute bucket, aligned to midnight so bucket boundaries
		// are deterministic regardless of session start time.
		mins := t.Hour()*60 + t.Minute()
		bucketStart := (mins / a.minutes) * a.minutes
		return t.Format("2006-01-02") + "-" + itoa(bucketStart)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// OnMinuteClose feeds one closed 1-minute candle into this timeframe's
// aggregation for its symbol.
func (a *Aggregator) OnMinuteClose(symbol string, m1 models.Candle) {
	key := a.bucketKeyFor(m1.OpenTime)

	a.mu.Lock()
	b, ok := a.current[symbol]
	if ok && b.bucketKey != key {
		closed := b.candle
		closed.Closed = true
		a.current[symbol] = nil
		delete(a.current, symbol)
		a.mu.Unlock()
		a.onClose(symbol, a.timeframe, closed)
		a.mu.Lock()
		ok = false
	}
	if !ok {
		b = &bucket{
			bucketKey: key,
			candle: models.Candle{
				Symbol: symbol, Timeframe: a.timeframe,
				OpenTime: m1.OpenTime, Open: m1.Open, High: m1.High, Low: m1.Low, Close: m1.Close,
				Volume: m1.Volume,
			},
		}
		a.current[symbol] = b
	} else {
		if m1.High.GreaterThan(b.candle.High) {
			b.candle.High = m1.High
		}
		if m1.Low.LessThan(b.candle.Low) {
			b.candle.Low = m1.Low
		}
		b.candle.Close = m1.Close
		b.candle.Volume += m1.Volume
	}
	a.mu.Unlock()
}
