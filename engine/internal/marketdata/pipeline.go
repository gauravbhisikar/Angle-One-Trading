package marketdata

import (
	"context"
	"sync"

	"tradingengine/internal/models"
)

// Pipeline is the engine's single shared tick -> candle pipeline
// (ENGINE_SPEC Sec 0.2-0.3). One instance for the whole engine: one feed
// connection, one 1-minute builder, one Aggregator per referenced
// timeframe — all shared across however many of the 10 concurrent
// strategies reference the same symbol/timeframe.
type Pipeline struct {
	feed    Feed
	builder *OneMinuteBuilder

	mu                sync.Mutex
	aggregators       map[models.Timeframe]*Aggregator
	indicatorUpdate   OnClose // always runs first, before any strategy listener (see OnCandleClose)
	listeners         map[int]OnClose
	nextListenerID    int
	subscribedSymbols map[string]bool
}

func NewPipeline(feed Feed) *Pipeline {
	p := &Pipeline{
		feed:              feed,
		aggregators:       map[models.Timeframe]*Aggregator{},
		listeners:         map[int]OnClose{},
		subscribedSymbols: map[string]bool{},
	}
	p.builder = NewOneMinuteBuilder(p.onOneMinuteClose)
	return p
}

// SetIndicatorUpdater registers the ONE function that must run first on
// every candle close, before any strategy reads the cache for that same
// candle — the shared indicator cache's update hook. There is exactly one
// per engine (ENGINE_SPEC Sec 0.4); it is not a general-purpose listener
// and is not stored in the same map as strategy listeners, because map
// iteration order in Go is randomized and would otherwise make
// cache-before-read a race instead of a guarantee.
func (p *Pipeline) SetIndicatorUpdater(fn OnClose) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.indicatorUpdate = fn
}

// OnCandleClose registers a listener notified for every closed candle on
// every timeframe (1m included), after the indicator cache has already
// been updated for that candle. Strategy runtimes register here. Returns
// an id for RemoveListener, so a stopped strategy's closure doesn't linger
// forever (ENGINE_SPEC Sec 0.6).
func (p *Pipeline) OnCandleClose(fn OnClose) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.nextListenerID
	p.nextListenerID++
	p.listeners[id] = fn
	return id
}

func (p *Pipeline) RemoveListener(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.listeners, id)
}

// EnsureTimeframe lazily creates the shared aggregator for a timeframe on
// first reference. Calling it again for a timeframe already in use is a
// no-op — this is what makes strategy #10 reusing "15m" free.
func (p *Pipeline) EnsureTimeframe(tf models.Timeframe) {
	if tf == models.TF1m {
		return // the 1-minute builder always runs
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.aggregators[tf]; ok {
		return
	}
	p.aggregators[tf] = NewAggregator(tf, p.dispatch)
}

// Subscribe adds symbols to the feed's subscription list, deduped — a
// symbol already subscribed by another strategy costs nothing extra
// (ENGINE_SPEC Sec 0.2).
func (p *Pipeline) Subscribe(symbols []string) error {
	p.mu.Lock()
	var fresh []string
	for _, s := range symbols {
		if !p.subscribedSymbols[s] {
			p.subscribedSymbols[s] = true
			fresh = append(fresh, s)
		}
	}
	p.mu.Unlock()
	if len(fresh) == 0 {
		return nil
	}
	return p.feed.Subscribe(fresh)
}

func (p *Pipeline) onOneMinuteClose(symbol string, _ models.Timeframe, candle models.Candle) {
	p.dispatch(symbol, models.TF1m, candle)

	p.mu.Lock()
	aggs := make([]*Aggregator, 0, len(p.aggregators))
	for _, a := range p.aggregators {
		aggs = append(aggs, a)
	}
	p.mu.Unlock()

	for _, a := range aggs {
		a.OnMinuteClose(symbol, candle)
	}
}

func (p *Pipeline) dispatch(symbol string, tf models.Timeframe, candle models.Candle) {
	p.mu.Lock()
	indicatorUpdate := p.indicatorUpdate
	listeners := make([]OnClose, 0, len(p.listeners))
	for _, fn := range p.listeners {
		listeners = append(listeners, fn)
	}
	p.mu.Unlock()

	if indicatorUpdate != nil {
		indicatorUpdate(symbol, tf, candle)
	}
	for _, fn := range listeners {
		fn(symbol, tf, candle)
	}
}

// Run drains the feed until ctx is cancelled or the feed closes. Single
// goroutine, single event loop (ENGINE_SPEC Sec 0.1) — no per-symbol or
// per-strategy goroutines.
func (p *Pipeline) Run(ctx context.Context) {
	ticks := p.feed.Ticks()
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-ticks:
			if !ok {
				return
			}
			p.builder.OnTick(t)
		}
	}
}
