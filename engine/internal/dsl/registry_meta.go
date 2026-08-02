package dsl

// IndicatorOperators is the canonical indicator -> allowed-operators table
// from DSL_SPEC Sec 6. Used for validation (Sec 23 rules 5-6). The
// indicators package imports this so the registry and the validator never
// drift apart.
var IndicatorOperators = map[string][]string{
	"price":            {"<", "<=", ">", ">=", "==", "crosses_above", "crosses_below"},
	"close":            {"<", "<=", ">", ">=", "==", "crosses_above", "crosses_below"},
	"open":             {"<", "<=", ">", ">=", "==", "crosses_above", "crosses_below"},
	"high":             {"<", "<=", ">", ">=", "==", "crosses_above", "crosses_below"},
	"low":              {"<", "<=", ">", ">=", "==", "crosses_above", "crosses_below"},
	"ema":              {"<", "<=", ">", ">=", "crosses_above", "crosses_below"},
	"ema_cross":        {"bullish", "bearish"},
	"sma":              {"<", "<=", ">", ">=", "crosses_above", "crosses_below"},
	"vwap":             {"<", ">", "crosses_above", "crosses_below"},
	"rsi":              {"<", "<=", ">", ">=", "crosses_above", "crosses_below"},
	"stochastic_rsi":   {"<", ">", "crosses_above", "crosses_below"},
	"macd":             {"bullish", "bearish", "crosses_above", "crosses_below"},
	"adx":              {"<", ">"},
	"atr":              {"<", ">"},
	"cci":              {"<", ">", "crosses_above", "crosses_below"},
	"mfi":              {"<", ">", "crosses_above", "crosses_below"},
	"roc":              {"<", ">", "crosses_above", "crosses_below"},
	"obv":              {"crosses_above", "crosses_below", "rising", "falling"},
	"supertrend":       {"bullish", "bearish"},
	"bollinger_bands":  {"price_above_upper", "price_below_lower", "squeeze"},
	"donchian_channel": {"price_above_upper", "price_below_lower", "breakout_up", "breakout_down"},
	"highest_high":     {"crosses_above", "crosses_below", "=="},
	"lowest_low":       {"crosses_above", "crosses_below", "=="},
	"volume":           {"<", ">", "spike_pct"},
	"prev_high":        {"crosses_above"},
	"prev_low":         {"crosses_below"},
	"gap_up":           {"true"},
	"gap_down":         {"true"},
	"support":          {"crosses_above", "crosses_below", "bounce"},
	"resistance":       {"crosses_above", "crosses_below", "bounce"},
	"pattern":          {"true"},
}

func IsKnownIndicator(name string) bool {
	_, ok := IndicatorOperators[name]
	return ok
}

func IsValidOperator(indicator, operator string) bool {
	ops, ok := IndicatorOperators[indicator]
	if !ok {
		return false
	}
	for _, o := range ops {
		if o == operator {
			return true
		}
	}
	return false
}
