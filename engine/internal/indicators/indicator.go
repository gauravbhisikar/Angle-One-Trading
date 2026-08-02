// Package indicators computes every technical indicator locally from
// candles the engine builds itself. Never fetched from a broker
// (ENGINE_SPEC/DSL_SPEC: "Angel One does not provide indicators").
//
// Indicator math runs in float64, not Decimal. Candle OHLC (the financial
// source of truth used for fills/PnL/brokerage) stays Decimal end to end
// per ENGINE_SPEC Sec 2 — but indicator values are comparison signals, not
// settlement amounts, and several (stddev, sqrt-based) have no natural
// Decimal implementation. Converting once at the indicator boundary is the
// pragmatic tradeoff.
package indicators

import (
	"fmt"
	"sort"
	"strings"

	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
)

// Indicator is one stateful, incremental (streaming) series instance bound
// to a single (symbol, timeframe, indicator, params) key. Update is called
// once per closed candle for that key and must be O(1) amortized — never
// recompute over full history (ENGINE_SPEC Sec 0.4).
type Indicator interface {
	Update(c models.Candle) dsl.Signal
}

// Factory builds a fresh Indicator instance for one subscription.
type Factory func(params map[string]float64, patternName string) (Indicator, error)

var registry = map[string]Factory{}

func register(name string, f Factory) {
	registry[name] = f
}

func New(name string, params map[string]float64, patternName string) (Indicator, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("indicators: unknown indicator %q", name)
	}
	return f(params, patternName)
}

// Key uniquely identifies one shared series across every strategy that
// references it, so 10 strategies using the same ema_cross(20,50) on the
// same symbol/timeframe hit one cached instance, not ten (ENGINE_SPEC Sec 0.4).
type Key struct {
	Symbol    string
	Timeframe string
	Indicator string
	ParamsKey string
}

func BuildParamsKey(params map[string]float64, patternName string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%v;", k, params[k])
	}
	if patternName != "" {
		fmt.Fprintf(&b, "pattern=%s;", patternName)
	}
	return b.String()
}

func param(params map[string]float64, key string, def float64) float64 {
	if v, ok := params[key]; ok {
		return v
	}
	return def
}

func paramInt(params map[string]float64, key string, def int) int {
	if v, ok := params[key]; ok {
		return int(v)
	}
	return def
}

func f64(d interface{ InexactFloat64() float64 }) float64 {
	return d.InexactFloat64()
}
