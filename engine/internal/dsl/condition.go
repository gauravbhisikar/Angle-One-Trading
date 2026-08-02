package dsl

import (
	"encoding/json"
	"fmt"
)

// Condition is the recursive boolean tree grammar (DSL_SPEC Sec 2-3):
// {"all":[...]}, {"any":[...]}, {"not":{...}}, or a leaf Rule.
type Condition struct {
	All  []*Condition
	Any  []*Condition
	Not  *Condition
	Rule *Rule
}

// Rule is a leaf node: either an indicator comparison or an exit-only
// shorthand (take_profit/stop_loss/trailing_sl).
type Rule struct {
	Indicator   string
	Operator    string
	Value       *float64
	StringValue *string
	CompareTo   *IndicatorRef
	Params      map[string]float64
	PatternName string
	Timeframe   string

	TakeProfit *float64
	StopLoss   *float64
	TrailingSL *float64
}

// IndicatorRef names an indicator + params, used inside compare_to.
type IndicatorRef struct {
	Indicator string
	Params    map[string]float64
}

var reservedRuleKeys = map[string]bool{
	"indicator": true, "operator": true, "value": true, "compare_to": true,
	"timeframe": true, "params": true, "take_profit": true, "stop_loss": true,
	"trailing_sl": true, "pattern_name": true,
}

func (c *Condition) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("dsl: condition must be an object: %w", err)
	}

	if v, ok := raw["all"]; ok {
		var kids []*Condition
		if err := json.Unmarshal(v, &kids); err != nil {
			return fmt.Errorf("dsl: \"all\" must be an array: %w", err)
		}
		c.All = kids
		return nil
	}
	if v, ok := raw["any"]; ok {
		var kids []*Condition
		if err := json.Unmarshal(v, &kids); err != nil {
			return fmt.Errorf("dsl: \"any\" must be an array: %w", err)
		}
		c.Any = kids
		return nil
	}
	if v, ok := raw["not"]; ok {
		var kid Condition
		if err := json.Unmarshal(v, &kid); err != nil {
			return fmt.Errorf("dsl: \"not\" must be a condition: %w", err)
		}
		c.Not = &kid
		return nil
	}

	rule, err := parseRule(raw)
	if err != nil {
		return err
	}
	c.Rule = rule
	return nil
}

func parseRule(raw map[string]json.RawMessage) (*Rule, error) {
	r := &Rule{Params: map[string]float64{}}

	if v, ok := raw["take_profit"]; ok {
		var f float64
		if err := json.Unmarshal(v, &f); err != nil {
			return nil, fmt.Errorf("dsl: take_profit must be numeric: %w", err)
		}
		r.TakeProfit = &f
		return r, nil
	}
	if v, ok := raw["stop_loss"]; ok {
		var f float64
		if err := json.Unmarshal(v, &f); err != nil {
			return nil, fmt.Errorf("dsl: stop_loss must be numeric: %w", err)
		}
		r.StopLoss = &f
		return r, nil
	}
	if v, ok := raw["trailing_sl"]; ok {
		var f float64
		if err := json.Unmarshal(v, &f); err != nil {
			return nil, fmt.Errorf("dsl: trailing_sl must be numeric: %w", err)
		}
		r.TrailingSL = &f
		return r, nil
	}

	if v, ok := raw["indicator"]; ok {
		if err := json.Unmarshal(v, &r.Indicator); err != nil {
			return nil, fmt.Errorf("dsl: indicator must be a string: %w", err)
		}
	} else {
		return nil, fmt.Errorf("dsl: leaf rule missing \"indicator\" (and no take_profit/stop_loss/trailing_sl shorthand)")
	}

	if v, ok := raw["operator"]; ok {
		if err := json.Unmarshal(v, &r.Operator); err != nil {
			return nil, fmt.Errorf("dsl: operator must be a string: %w", err)
		}
	}

	if v, ok := raw["value"]; ok {
		var num float64
		if err := json.Unmarshal(v, &num); err == nil {
			r.Value = &num
		} else {
			var s string
			if err2 := json.Unmarshal(v, &s); err2 == nil {
				r.StringValue = &s
			} else {
				return nil, fmt.Errorf("dsl: value must be number or string: %w", err)
			}
		}
	}

	if v, ok := raw["timeframe"]; ok {
		if err := json.Unmarshal(v, &r.Timeframe); err != nil {
			return nil, fmt.Errorf("dsl: timeframe must be a string: %w", err)
		}
	}

	if v, ok := raw["compare_to"]; ok {
		var cmpRaw map[string]json.RawMessage
		if err := json.Unmarshal(v, &cmpRaw); err != nil {
			return nil, fmt.Errorf("dsl: compare_to must be an object: %w", err)
		}
		ref := &IndicatorRef{Params: map[string]float64{}}
		if iv, ok := cmpRaw["indicator"]; ok {
			if err := json.Unmarshal(iv, &ref.Indicator); err != nil {
				return nil, fmt.Errorf("dsl: compare_to.indicator must be a string: %w", err)
			}
		}
		mergeParams(ref.Params, cmpRaw, map[string]bool{"indicator": true})
		r.CompareTo = ref
	}

	// pattern indicator carries a string param, not numeric.
	if v, ok := raw["pattern_name"]; ok {
		if err := json.Unmarshal(v, &r.PatternName); err != nil {
			return nil, fmt.Errorf("dsl: pattern_name must be a string: %w", err)
		}
	}

	mergeParams(r.Params, raw, reservedRuleKeys)

	return r, nil
}

// mergeParams pulls every non-reserved numeric key directly on the rule
// object into params (covers the DSL's shorthand style, e.g.
// {"indicator":"ema_cross","fast":20,"slow":50}), and flattens a nested
// "params" object if the AI used the fully-qualified form instead.
func mergeParams(dst map[string]float64, raw map[string]json.RawMessage, reserved map[string]bool) {
	for k, v := range raw {
		if k == "params" {
			var nested map[string]float64
			if err := json.Unmarshal(v, &nested); err == nil {
				for nk, nv := range nested {
					dst[nk] = nv
				}
			}
			continue
		}
		if reserved[k] {
			continue
		}
		var f float64
		if err := json.Unmarshal(v, &f); err == nil {
			dst[k] = f
		}
	}
}

// Signal is what an indicator resolver returns for one leaf evaluation:
// a numeric value (current + previous, for cross detection) plus named
// boolean flags for non-numeric indicator states (bullish, bearish,
// price_above_upper, breakout_up, pattern true/false, etc).
type Signal struct {
	Value float64
	Prev  float64
	Flags map[string]bool
}

// Resolver computes/looks up indicator signals and trade-relative exit
// shorthands. Implemented by the strategy runtime against the shared
// indicator cache (ENGINE_SPEC Sec 0.4) — the dsl package itself never
// touches market data.
type Resolver interface {
	Resolve(rule *Rule) (Signal, error)
	ResolveRef(ref *IndicatorRef, timeframe string) (Signal, error)
	TakeProfitHit(pct float64) bool
	StopLossHit(pct float64) bool
	TrailingSLHit(pct float64) bool
}

// Evaluate walks the condition tree against a Resolver bound to a specific
// symbol/candle-close event and trade context.
func (c *Condition) Evaluate(r Resolver) (bool, error) {
	if c == nil {
		return false, nil
	}
	switch {
	case c.All != nil:
		for _, kid := range c.All {
			ok, err := kid.Evaluate(r)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
		return true, nil
	case c.Any != nil:
		for _, kid := range c.Any {
			ok, err := kid.Evaluate(r)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	case c.Not != nil:
		ok, err := c.Not.Evaluate(r)
		if err != nil {
			return false, err
		}
		return !ok, nil
	case c.Rule != nil:
		return c.Rule.evaluate(r)
	}
	return false, fmt.Errorf("dsl: empty condition node")
}

func (rule *Rule) evaluate(r Resolver) (bool, error) {
	if rule.TakeProfit != nil {
		return r.TakeProfitHit(*rule.TakeProfit), nil
	}
	if rule.StopLoss != nil {
		return r.StopLossHit(*rule.StopLoss), nil
	}
	if rule.TrailingSL != nil {
		return r.TrailingSLHit(*rule.TrailingSL), nil
	}

	sig, err := r.Resolve(rule)
	if err != nil {
		return false, err
	}

	var cmpSig *Signal
	if rule.CompareTo != nil {
		s, err := r.ResolveRef(rule.CompareTo, rule.Timeframe)
		if err != nil {
			return false, err
		}
		cmpSig = &s
	}

	return matchOperator(sig, rule.Operator, rule.Value, cmpSig)
}

func matchOperator(sig Signal, operator string, literal *float64, cmp *Signal) (bool, error) {
	threshold := func() (float64, bool) {
		if cmp != nil {
			return cmp.Value, true
		}
		if literal != nil {
			return *literal, true
		}
		return 0, false
	}
	prevThreshold := func() (float64, bool) {
		if cmp != nil {
			return cmp.Prev, true
		}
		if literal != nil {
			return *literal, true
		}
		return 0, false
	}

	switch operator {
	case "<", "<=", ">", ">=", "==", "spike_pct":
		t, ok := threshold()
		if !ok {
			return false, fmt.Errorf("dsl: operator %q needs a value or compare_to", operator)
		}
		switch operator {
		case "<":
			return sig.Value < t, nil
		case "<=":
			return sig.Value <= t, nil
		case ">", "spike_pct":
			return sig.Value > t, nil
		case ">=":
			return sig.Value >= t, nil
		case "==":
			return sig.Value == t, nil
		}
	case "crosses_above":
		t, ok := threshold()
		if !ok {
			// No literal/compare_to given — indicator resolves its own
			// cross (e.g. macd vs its signal line, prev_high vs close).
			return sig.Flags["crosses_above"], nil
		}
		pt, _ := prevThreshold()
		return sig.Prev <= pt && sig.Value > t, nil
	case "crosses_below":
		t, ok := threshold()
		if !ok {
			return sig.Flags["crosses_below"], nil
		}
		pt, _ := prevThreshold()
		return sig.Prev >= pt && sig.Value < t, nil
	default:
		// Named boolean flags: bullish, bearish, true, bounce, breakout_up,
		// breakout_down, rising, falling, price_above_upper,
		// price_below_lower, squeeze.
		return sig.Flags[operator], nil
	}
	return false, fmt.Errorf("dsl: unhandled operator %q", operator)
}
