package indicators

import (
	"fmt"
	"sync"
	"time"

	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

// Cache is the shared indicator pipeline (ENGINE_SPEC Sec 0.4). One
// instance per running engine. Every strategy subscribes the indicators
// its DSL tree references; on each candle close the cache updates each
// subscribed series exactly once and every strategy watching that key
// reads the same cached Signal.
type Cache struct {
	mu         sync.RWMutex
	instances  map[Key]Indicator
	latest     map[Key]dsl.Signal
	lastUpdate map[Key]time.Time
	refCount   map[Key]int
}

func NewCache() *Cache {
	return &Cache{
		instances:  map[Key]Indicator{},
		latest:     map[Key]dsl.Signal{},
		lastUpdate: map[Key]time.Time{},
		refCount:   map[Key]int{},
	}
}

// Subscribe registers interest in one (symbol, timeframe, indicator, params)
// series, creating it on first reference. Ref-counted so multiple
// strategies sharing a key don't create duplicate instances, and the
// instance can be freed when the last strategy referencing it stops.
func (c *Cache) Subscribe(symbol, timeframe, indicator string, params map[string]float64, patternName string) (Key, error) {
	key := Key{Symbol: symbol, Timeframe: timeframe, Indicator: indicator, ParamsKey: BuildParamsKey(params, patternName)}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.instances[key]; !ok {
		inst, err := New(indicator, params, patternName)
		if err != nil {
			return key, err
		}
		c.instances[key] = inst
	}
	c.refCount[key]++
	return key, nil
}

func (c *Cache) Unsubscribe(key Key) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refCount[key]--
	if c.refCount[key] <= 0 {
		delete(c.instances, key)
		delete(c.latest, key)
		delete(c.lastUpdate, key)
		delete(c.refCount, key)
	}
}

// OnCandleClose updates every subscribed indicator for this (symbol,
// timeframe) exactly once. Called by the candle builder when a candle
// closes for that pair — never per-strategy. lastUpdate records wall-
// clock time (not the candle's own timestamp) specifically so a stalled
// feed is visible as "hasn't updated in 40 minutes" even if replaying
// old/gappy data — see Runtime.LastCandleAt, built for exactly the
// "is this strategy actually alive" question.
func (c *Cache) OnCandleClose(symbol, timeframe string, candle models.Candle) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for key, inst := range c.instances {
		if key.Symbol != symbol || key.Timeframe != timeframe {
			continue
		}
		c.latest[key] = inst.Update(candle)
		c.lastUpdate[key] = now
	}
}

func (c *Cache) Get(symbol, timeframe, indicator string, params map[string]float64, patternName string) (dsl.Signal, bool) {
	key := Key{Symbol: symbol, Timeframe: timeframe, Indicator: indicator, ParamsKey: BuildParamsKey(params, patternName)}
	c.mu.RLock()
	defer c.mu.RUnlock()
	sig, ok := c.latest[key]
	return sig, ok
}

func (c *Cache) GetByKey(key Key) (dsl.Signal, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	sig, ok := c.latest[key]
	return sig, ok
}

// LastUpdateByKey returns the wall-clock time this key's indicator last
// processed a candle close — false if it's never updated yet (still
// warming up, no candle closed since subscribing).
func (c *Cache) LastUpdateByKey(key Key) (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.lastUpdate[key]
	return t, ok
}

func (c *Cache) String() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return fmt.Sprintf("indicators.Cache{series=%d}", len(c.instances))
}
