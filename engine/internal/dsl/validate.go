package dsl

import (
	"fmt"
	"strings"

	"tradingengine/internal/models"
)

type ValidationError struct {
	Path   string
	Reason string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, e.Reason)
}

type ValidationResult struct {
	Errors   []ValidationError
	Warnings []ValidationError
}

func (r ValidationResult) Valid() bool {
	return len(r.Errors) == 0
}

const SupportedVersion = "1.2"

var validExitPriorityKeys = map[string]bool{
	"stop_loss": true, "take_profit": true, "trailing_sl": true, "signal": true,
}

// Validate enforces DSL_SPEC Sec 23. It never mutates the strategy and
// never partially accepts — callers check Valid() before using the result.
func Validate(s *Strategy) ValidationResult {
	var res ValidationResult
	fail := func(path, reason string) {
		res.Errors = append(res.Errors, ValidationError{Path: path, Reason: reason})
	}
	warn := func(path, reason string) {
		res.Warnings = append(res.Warnings, ValidationError{Path: path, Reason: reason})
	}

	// 1. version
	if s.Version != SupportedVersion {
		fail("$.version", fmt.Sprintf("unsupported schema version %q, engine supports %q", s.Version, SupportedVersion))
	}

	// 2. type
	if s.Type != models.StrategyIntraday && s.Type != models.StrategySwing {
		fail("$.type", "must be \"intraday\" or \"swing\"")
	}

	// 3. holding/session requirements per type
	if s.Type == models.StrategyIntraday {
		if s.Holding.ForceSquareOff == "" {
			fail("$.holding.force_square_off", "required for intraday strategies")
		}
		if s.Session == nil {
			fail("$.session", "required for intraday strategies")
		}
	}
	if s.Type == models.StrategySwing {
		if s.Holding.MaxDays <= 0 {
			fail("$.holding.max_days", "required (>0) for swing strategies")
		}
	}

	// 4. direction
	switch s.Direction {
	case models.DirectionLong, models.DirectionShort, models.DirectionBoth:
	default:
		fail("$.direction", "must be \"long\", \"short\", or \"both\"")
	}

	// 5, 6, 8: indicator names, operators, duplicate leaves
	seen := map[string]bool{}
	var walk func(c *Condition, path string)
	walk = func(c *Condition, path string) {
		if c == nil {
			return
		}
		switch {
		case c.All != nil:
			for i, kid := range c.All {
				walk(kid, fmt.Sprintf("%s.all[%d]", path, i))
			}
		case c.Any != nil:
			for i, kid := range c.Any {
				walk(kid, fmt.Sprintf("%s.any[%d]", path, i))
			}
		case c.Not != nil:
			walk(c.Not, path+".not")
		case c.Rule != nil:
			validateRule(c.Rule, path, fail)
			sig := ruleSignature(c.Rule)
			if seen[sig] {
				fail(path, "duplicate leaf rule (identical indicator/params/operator/value/timeframe already present in this tree)")
			}
			seen[sig] = true
		default:
			fail(path, "empty condition node")
		}
	}
	walk(s.Entry, "$.entry")
	walk(s.Exit, "$.exit")

	// 9. risk_based sizing requires a stop distance in exit tree
	if s.PositionSizing.Type == "risk_based" {
		if !treeHasStopDistance(s.Exit) {
			fail("$.position_sizing", "type \"risk_based\" requires a stop_loss or trailing_sl leaf in $.exit")
		}
	}

	// 10. capital deployed sanity check (best-effort, only meaningful for fixed_pct)
	if s.PositionSizing.Type == "fixed_pct" && s.Risk.MaxPositions > 0 {
		deployed := s.PositionSizing.Value * float64(s.Risk.MaxPositions)
		if deployed > 100 {
			fail("$.risk.max_positions", fmt.Sprintf("position_sizing.value (%.2f%%) * max_positions (%d) = %.2f%%, exceeds 100%% of capital", s.PositionSizing.Value, s.Risk.MaxPositions, deployed))
		} else if deployed > 60 {
			warn("$.risk.max_positions", fmt.Sprintf("%.2f%% of capital deployable at once — consider lowering position_sizing or max_positions", deployed))
		}
	}

	// 11. portfolio exposure caps
	if s.Portfolio != nil {
		if s.Portfolio.MaxSectorExposure < 0 || s.Portfolio.MaxSectorExposure > 100 {
			fail("$.portfolio.max_sector_exposure", "must be between 0 and 100")
		}
		if s.Portfolio.MaxSymbolExposure < 0 || s.Portfolio.MaxSymbolExposure > 100 {
			fail("$.portfolio.max_symbol_exposure", "must be between 0 and 100")
		}
	}

	// 12. symbols
	if len(s.Symbols) == 0 {
		fail("$.symbols", "must not be empty")
	}
	for i, sym := range s.Symbols {
		if sym == "" || sym != strings.ToUpper(sym) {
			fail(fmt.Sprintf("$.symbols[%d]", i), "must be non-empty and uppercase")
		}
	}

	// 13. benchmark
	if s.Benchmark == "" {
		fail("$.benchmark", "required")
	}

	// 14. exit_priority
	if len(s.ExitPriority) > 0 {
		count := map[string]int{}
		for i, key := range s.ExitPriority {
			if !validExitPriorityKeys[key] {
				fail(fmt.Sprintf("$.exit_priority[%d]", i), fmt.Sprintf("invalid key %q, must be one of stop_loss/take_profit/trailing_sl/signal", key))
			}
			count[key]++
			if count[key] > 1 {
				fail(fmt.Sprintf("$.exit_priority[%d]", i), fmt.Sprintf("%q listed more than once", key))
			}
		}
	}

	// 15. confirmation
	if s.Confirmation != "" && s.Confirmation != "close" && s.Confirmation != "intrabar" {
		fail("$.confirmation", "must be \"close\" or \"intrabar\"")
	}

	return res
}

func validateRule(r *Rule, path string, fail func(path, reason string)) {
	if r.TakeProfit != nil || r.StopLoss != nil || r.TrailingSL != nil {
		return // exit-only shorthand, no indicator/operator to check
	}
	if !IsKnownIndicator(r.Indicator) {
		fail(path+".indicator", fmt.Sprintf("unknown indicator %q", r.Indicator))
		return
	}
	if r.Operator == "" {
		fail(path+".operator", "required")
		return
	}
	if !IsValidOperator(r.Indicator, r.Operator) {
		fail(path+".operator", fmt.Sprintf("operator %q not valid for indicator %q", r.Operator, r.Indicator))
	}
}

func ruleSignature(r *Rule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|", r.Indicator, r.Operator)
	if r.Value != nil {
		fmt.Fprintf(&b, "%v|", *r.Value)
	}
	if r.StringValue != nil {
		fmt.Fprintf(&b, "%s|", *r.StringValue)
	}
	fmt.Fprintf(&b, "%s|", r.Timeframe)
	keys := make([]string, 0, len(r.Params))
	for k := range r.Params {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%v|", k, r.Params[k])
	}
	if r.CompareTo != nil {
		fmt.Fprintf(&b, "cmp:%s|", r.CompareTo.Indicator)
	}
	if r.TakeProfit != nil {
		fmt.Fprintf(&b, "tp:%v|", *r.TakeProfit)
	}
	if r.StopLoss != nil {
		fmt.Fprintf(&b, "sl:%v|", *r.StopLoss)
	}
	if r.TrailingSL != nil {
		fmt.Fprintf(&b, "tsl:%v|", *r.TrailingSL)
	}
	return b.String()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func treeHasStopDistance(c *Condition) bool {
	if c == nil {
		return false
	}
	switch {
	case c.All != nil:
		for _, kid := range c.All {
			if treeHasStopDistance(kid) {
				return true
			}
		}
	case c.Any != nil:
		for _, kid := range c.Any {
			if treeHasStopDistance(kid) {
				return true
			}
		}
	case c.Not != nil:
		return treeHasStopDistance(c.Not)
	case c.Rule != nil:
		return c.Rule.StopLoss != nil || c.Rule.TrailingSL != nil
	}
	return false
}
