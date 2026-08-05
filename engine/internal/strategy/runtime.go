// Package strategy compiles one DSL document into a running unit bound to
// the shared indicator cache (ENGINE_SPEC Sec 0.4-0.5): market data is
// shared, but portfolio/risk/order state stays fully isolated per
// strategy.
package strategy

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/dsl"
	"tradingengine/internal/execution"
	"tradingengine/internal/indicators"
	"tradingengine/internal/models"
	"tradingengine/internal/portfolio"
	"tradingengine/internal/portfolio/cost"
	"tradingengine/internal/risk"
)

type Hooks struct {
	OnOrder func(models.Order)
	OnTrade func(models.Trade)
	OnLog   func(strategyID, level, message string)
}

type Deps struct {
	Cache          *indicators.Cache
	Broker         execution.BrokerAdapter
	Ledger         *portfolio.Ledger
	Risk           *risk.State
	PortfolioGuard *risk.PortfolioGuard
	Cost           cost.Model
	LotSize        func(symbol string) int
	TickSize       func(symbol string) decimal.Decimal
	IsHoliday      func(t time.Time) bool
	Regime         func() string
	Now            func() time.Time
	Hooks          Hooks
}

type RunState int32

const (
	StateRunning RunState = iota
	StatePaused           // no new entries; existing trades still managed
	StateStopped          // nothing processed, including exits
)

type Runtime struct {
	Strategy *dsl.Strategy
	deps     Deps

	state      int32
	openCount  int32 // atomic mirror of len(trades), safe to read from API handler goroutines
	barIndex   int
	tradingDay string
	trades     map[string]*models.Trade
	entryTime  map[string]time.Time
	subKeys    []indicators.Key
}

func (rt *Runtime) SetState(s RunState) { atomic.StoreInt32(&rt.state, int32(s)) }
func (rt *Runtime) State() RunState     { return RunState(atomic.LoadInt32(&rt.state)) }

func (s RunState) String() string {
	switch s {
	case StateRunning:
		return "running"
	case StatePaused:
		return "paused"
	case StateStopped:
		return "stopped"
	}
	return "unknown"
}

// OpenPositionCount reports how many symbols currently have an active
// trade — safe to call from any goroutine (e.g. an API handler), unlike
// reading the trades map directly, which is only ever touched by the
// single event-loop goroutine (ENGINE_SPEC Sec 0.1).
func (rt *Runtime) OpenPositionCount() int {
	return int(atomic.LoadInt32(&rt.openCount))
}

// RestoreOpenTrade seeds a freshly-constructed Runtime with a trade that
// was already open before this process started — without this, a
// restarted engine (redeploy, crash) has no memory of it: OnCandleClose
// would call tryEntry (not manageOpenTrade) for that symbol, silently
// abandoning the real position (never exits, take-profit/stop-loss never
// checked again) and risking a second, duplicate entry on top of it. Must
// be called once per symbol, before the engine starts processing candles
// for this runtime again (see api.Server.startStrategy). Not meant for
// any other caller — a live Runtime already tracks this itself in
// tryEntry.
func (rt *Runtime) RestoreOpenTrade(symbol string, trade *models.Trade) {
	rt.trades[symbol] = trade
	rt.entryTime[symbol] = trade.EntryTime
	atomic.AddInt32(&rt.openCount, 1)
}

func NewRuntime(s *dsl.Strategy, deps Deps) *Runtime {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.IsHoliday == nil {
		deps.IsHoliday = func(time.Time) bool { return false }
	}
	if deps.Regime == nil {
		deps.Regime = func() string { return "" }
	}
	if deps.LotSize == nil {
		deps.LotSize = func(string) int { return 0 }
	}
	if deps.TickSize == nil {
		deps.TickSize = func(string) decimal.Decimal { return decimal.NewFromFloat(0.05) }
	}
	return &Runtime{
		Strategy:  s,
		deps:      deps,
		trades:    map[string]*models.Trade{},
		entryTime: map[string]time.Time{},
	}
}

type leafSpec struct {
	indicator, timeframe, patternName string
	params                            map[string]float64
}

func collectLeaves(c *dsl.Condition, defaultTF string, out map[string]leafSpec) {
	if c == nil {
		return
	}
	switch {
	case c.All != nil:
		for _, k := range c.All {
			collectLeaves(k, defaultTF, out)
		}
	case c.Any != nil:
		for _, k := range c.Any {
			collectLeaves(k, defaultTF, out)
		}
	case c.Not != nil:
		collectLeaves(c.Not, defaultTF, out)
	case c.Rule != nil:
		r := c.Rule
		if r.TakeProfit != nil || r.StopLoss != nil || r.TrailingSL != nil {
			return // no indicator cache subscription needed
		}
		tf := r.Timeframe
		if tf == "" {
			tf = defaultTF
		}
		key := fmt.Sprintf("%s|%s|%s", r.Indicator, tf, indicators.BuildParamsKey(r.Params, r.PatternName))
		out[key] = leafSpec{indicator: r.Indicator, timeframe: tf, patternName: r.PatternName, params: r.Params}
		if r.CompareTo != nil {
			ctf := tf
			ckey := fmt.Sprintf("%s|%s|%s", r.CompareTo.Indicator, ctf, indicators.BuildParamsKey(r.CompareTo.Params, ""))
			out[ckey] = leafSpec{indicator: r.CompareTo.Indicator, timeframe: ctf, params: r.CompareTo.Params}
		}
	}
}

// RequiredTimeframes returns every timeframe this strategy's condition
// trees reference (its default plus any per-rule override), so the
// caller can ensure a shared Aggregator exists for each before ticks
// start flowing (ENGINE_SPEC Sec 0.3).
func (rt *Runtime) RequiredTimeframes() []models.Timeframe {
	leaves := map[string]leafSpec{}
	collectLeaves(rt.Strategy.Entry, string(rt.Strategy.Timeframe), leaves)
	collectLeaves(rt.Strategy.Exit, string(rt.Strategy.Timeframe), leaves)

	seen := map[models.Timeframe]bool{rt.Strategy.Timeframe: true}
	out := []models.Timeframe{rt.Strategy.Timeframe}
	for _, l := range leaves {
		tf := models.Timeframe(l.timeframe)
		if !seen[tf] {
			seen[tf] = true
			out = append(out, tf)
		}
	}
	return out
}

// Subscribe registers every indicator this strategy's DSL references
// against the shared cache, for every symbol it trades. Ref-counted by
// the cache, so overlapping subscriptions across strategies are free
// (ENGINE_SPEC Sec 0.4).
func (rt *Runtime) Subscribe() error {
	leaves := map[string]leafSpec{}
	collectLeaves(rt.Strategy.Entry, string(rt.Strategy.Timeframe), leaves)
	collectLeaves(rt.Strategy.Exit, string(rt.Strategy.Timeframe), leaves)

	for _, symbol := range rt.Strategy.Symbols {
		for _, l := range leaves {
			key, err := rt.deps.Cache.Subscribe(symbol, l.timeframe, l.indicator, l.params, l.patternName)
			if err != nil {
				return fmt.Errorf("strategy %s: %w", rt.Strategy.StrategyID, err)
			}
			rt.subKeys = append(rt.subKeys, key)
		}
	}
	return nil
}

// Unsubscribe releases every indicator series this strategy referenced,
// freeing the shared cache entry once no other strategy needs it
// (ENGINE_SPEC Sec 0.4 ref-counting).
func (rt *Runtime) Unsubscribe() {
	for _, key := range rt.subKeys {
		rt.deps.Cache.Unsubscribe(key)
	}
	rt.subKeys = nil
}

// LiveIndicator is one indicator's real current reading — what the
// strategy's own condition tree is actually evaluating against right now,
// not a description of what it's supposed to do. Exists specifically so
// "why hasn't this taken a trade yet" is answerable by looking at real
// numbers instead of trusting an explanation.
type LiveIndicator struct {
	Indicator  string
	Timeframe  string
	Params     string // human-readable, e.g. "period=14;"
	Value      float64
	Prev       float64
	Flags      map[string]bool
	Known      bool       // false if the cache has no value yet (still warming up)
	LastUpdate *time.Time // wall-clock time of the last candle close processed, nil if never
}

// LiveIndicators reports the current cached value of every indicator this
// strategy subscribed to (Subscribe already registered these against the
// shared cache — this just reads back what's there right now).
func (rt *Runtime) LiveIndicators() []LiveIndicator {
	out := make([]LiveIndicator, 0, len(rt.subKeys))
	for _, key := range rt.subKeys {
		sig, ok := rt.deps.Cache.GetByKey(key)
		li := LiveIndicator{
			Indicator: key.Indicator, Timeframe: key.Timeframe, Params: key.ParamsKey,
			Value: sig.Value, Prev: sig.Prev, Flags: sig.Flags, Known: ok,
		}
		if t, tok := rt.deps.Cache.LastUpdateByKey(key); tok {
			li.LastUpdate = &t
		}
		out = append(out, li)
	}
	return out
}

// LastCandleAt is the single most recent candle-close time across every
// indicator this strategy subscribed to — "is this strategy actually
// alive" reduced to one number for a strategy card, without needing the
// full per-indicator breakdown. Returns ok=false if nothing has ever
// updated (e.g. still warming up, or a symbol/timeframe with no data
// flowing at all — the exact "something's actually stuck" signal a user
// can't otherwise distinguish from "just hasn't fired yet").
func (rt *Runtime) LastCandleAt() (time.Time, bool) {
	var latest time.Time
	found := false
	for _, key := range rt.subKeys {
		t, ok := rt.deps.Cache.LastUpdateByKey(key)
		if ok && (!found || t.After(latest)) {
			latest = t
			found = true
		}
	}
	return latest, found
}

func (rt *Runtime) symbolTracked(symbol string) bool {
	for _, s := range rt.Strategy.Symbols {
		if s == symbol {
			return true
		}
	}
	return false
}

// OnCandleClose is registered against the shared pipeline (one call per
// closed candle, engine-wide). It filters to its own symbols and primary
// timeframe — the "confirmation" clock (DSL_SPEC Sec 5) — cheaply.
func (rt *Runtime) OnCandleClose(ctx context.Context, symbol string, tf models.Timeframe, candle models.Candle) {
	if rt.State() == StateStopped {
		return
	}
	if tf != rt.Strategy.Timeframe || !rt.symbolTracked(symbol) {
		return
	}

	day := candle.OpenTime.Format("2006-01-02")
	if day != rt.tradingDay {
		rt.tradingDay = day
		rt.deps.Risk.RollDay(day)
		for _, trade := range rt.trades {
			trade.HoldingDays++
		}
	}
	rt.barIndex++

	if rt.deps.IsHoliday(candle.OpenTime) {
		return
	}
	if regime := rt.deps.Regime(); regime != "" && len(rt.Strategy.MarketRegime) > 0 && !containsStr(rt.Strategy.MarketRegime, regime) {
		if rt.trades[symbol] == nil {
			return // regime blocks new entries only, not managing existing trades
		}
	}

	if trade, open := rt.trades[symbol]; open {
		rt.manageOpenTrade(ctx, symbol, candle, trade)
		return
	}
	rt.tryEntry(ctx, symbol, candle)
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (rt *Runtime) log(level, msg string) {
	if rt.deps.Hooks.OnLog != nil {
		rt.deps.Hooks.OnLog(rt.Strategy.StrategyID, level, msg)
	}
}
