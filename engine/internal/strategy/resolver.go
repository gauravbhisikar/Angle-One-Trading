package strategy

import (
	"fmt"

	"github.com/shopspring/decimal"

	"tradingengine/internal/dsl"
	"tradingengine/internal/indicators"
	"tradingengine/internal/models"
)

// ruleResolver binds one candle-close evaluation (a specific symbol, at a
// specific price) to the shared indicator cache, implementing
// dsl.Resolver. Cheap to construct — one per evaluation, no state kept
// between candles (the indicator Cache and the Trade own all the state).
type ruleResolver struct {
	cache            *indicators.Cache
	symbol           string
	defaultTimeframe string
	currentPrice     decimal.Decimal
	trade            *models.Trade // nil during entry evaluation (no open trade yet)
}

func (r *ruleResolver) Resolve(rule *dsl.Rule) (dsl.Signal, error) {
	tf := rule.Timeframe
	if tf == "" {
		tf = r.defaultTimeframe
	}
	sig, ok := r.cache.Get(r.symbol, tf, rule.Indicator, rule.Params, rule.PatternName)
	if !ok {
		return dsl.Signal{}, fmt.Errorf("strategy: no cached value yet for %s on %s/%s (still warming up)", rule.Indicator, r.symbol, tf)
	}
	return sig, nil
}

func (r *ruleResolver) ResolveRef(ref *dsl.IndicatorRef, timeframe string) (dsl.Signal, error) {
	tf := timeframe
	if tf == "" {
		tf = r.defaultTimeframe
	}
	sig, ok := r.cache.Get(r.symbol, tf, ref.Indicator, ref.Params, "")
	if !ok {
		return dsl.Signal{}, fmt.Errorf("strategy: no cached value yet for compare_to %s on %s/%s", ref.Indicator, r.symbol, tf)
	}
	return sig, nil
}

func (r *ruleResolver) TakeProfitHit(pct float64) bool {
	if r.trade == nil || r.trade.EntryPrice.IsZero() {
		return false
	}
	target := r.trade.EntryPrice.Mul(decimal.NewFromFloat(1 + pct/100))
	return r.currentPrice.GreaterThanOrEqual(target)
}

func (r *ruleResolver) StopLossHit(pct float64) bool {
	if r.trade == nil || r.trade.EntryPrice.IsZero() {
		return false
	}
	target := r.trade.EntryPrice.Mul(decimal.NewFromFloat(1 - pct/100))
	return r.currentPrice.LessThanOrEqual(target)
}

func (r *ruleResolver) TrailingSLHit(pct float64) bool {
	if r.trade == nil || r.trade.HighWaterMark.IsZero() {
		return false
	}
	target := r.trade.HighWaterMark.Mul(decimal.NewFromFloat(1 - pct/100))
	return r.currentPrice.LessThanOrEqual(target)
}
