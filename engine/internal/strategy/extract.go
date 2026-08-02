package strategy

import "tradingengine/internal/dsl"

// extractShorthand walks an exit tree for the first take_profit/stop_loss/
// trailing_sl leaf of each kind. Used only to attribute an exit's reason
// against DSL_SPEC's exit_priority (Sec 4) — the actual exit decision
// still comes from evaluating the full condition tree.
func extractShorthand(c *dsl.Condition) (takeProfit, stopLoss, trailingSL *float64) {
	if c == nil {
		return
	}
	var walk func(*dsl.Condition)
	walk = func(n *dsl.Condition) {
		if n == nil {
			return
		}
		switch {
		case n.All != nil:
			for _, k := range n.All {
				walk(k)
			}
		case n.Any != nil:
			for _, k := range n.Any {
				walk(k)
			}
		case n.Not != nil:
			walk(n.Not)
		case n.Rule != nil:
			if n.Rule.TakeProfit != nil && takeProfit == nil {
				takeProfit = n.Rule.TakeProfit
			}
			if n.Rule.StopLoss != nil && stopLoss == nil {
				stopLoss = n.Rule.StopLoss
			}
			if n.Rule.TrailingSL != nil && trailingSL == nil {
				trailingSL = n.Rule.TrailingSL
			}
		}
	}
	walk(c)
	return
}

func defaultExitPriority() []string {
	return []string{"stop_loss", "trailing_sl", "take_profit", "signal"}
}
