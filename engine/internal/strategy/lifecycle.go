package strategy

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"tradingengine/internal/execution"
	"tradingengine/internal/models"
)

const marketOpen = "09:15"
const marketClose = "15:30"

// istLocation matches internal/marketsession's own copy exactly (this
// package can't import that one — marketsession -> scheduler -> strategy
// would cycle back here) — same tiny, deliberately duplicated snippet used
// in internal/marketdata/angelone for the identical reason.
var istLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.FixedZone("IST", 5*3600+1800) // fallback: fixed +05:30, no DST in India anyway
	}
	return loc
}()

// timeOfDay converts to IST before formatting — marketOpen/marketClose
// (and Session.EntryStart/EntryEnd, ForceSquareOff) are all IST wall-clock
// values (NSE's real 09:15-15:30 IST hours). Without this conversion, a
// candle timestamp in any other zone (UTC ticks from the real Angel One
// feed, or a server whose system clock isn't IST) gets compared against
// IST constants directly — real bug found 2026-08-05: this silently
// blocked every intraday entry whenever the server's local time-of-day
// didn't happen to already read as IST, with no log line at all (tryEntry
// returns early, before anything worth logging).
func timeOfDay(t time.Time) string {
	return t.In(istLocation).Format("15:04")
}

func (rt *Runtime) tryEntry(ctx context.Context, symbol string, candle models.Candle) {
	if rt.State() == StatePaused {
		return // paused: no new entries, but existing trades keep being managed
	}
	if rt.Strategy.Direction == models.DirectionShort {
		return // short direction unsupported in v1.2, see DSL_SPEC Sec 8
	}

	tod := timeOfDay(candle.OpenTime)
	if tod < marketOpen || tod > marketClose {
		return // NSE market hours hard boundary, ENGINE_SPEC Sec 6 — overrides nothing, extends nothing
	}
	if rt.Strategy.Type == models.StrategyIntraday && rt.Strategy.Session != nil {
		if tod < rt.Strategy.Session.EntryStart || tod > rt.Strategy.Session.EntryEnd {
			return
		}
	}

	resolver := &ruleResolver{cache: rt.deps.Cache, symbol: symbol, defaultTimeframe: string(rt.Strategy.Timeframe), currentPrice: candle.Close}
	hit, err := rt.Strategy.Entry.Evaluate(resolver)
	if err != nil {
		return // indicators still warming up
	}
	if !hit {
		return
	}

	cooldownBars := 0
	if rt.Strategy.Cooldown != nil {
		cooldownBars = rt.Strategy.Cooldown.Bars
	}
	reentryAllowed, maxReentries := true, 0
	if rt.Strategy.Reentry != nil {
		reentryAllowed = rt.Strategy.Reentry.Allowed
		maxReentries = rt.Strategy.Reentry.MaxReentries
	}
	if ok, reason := rt.deps.Risk.CanEnter(symbol, rt.barIndex, cooldownBars, reentryAllowed, maxReentries); !ok {
		rt.log("info", fmt.Sprintf("entry blocked for %s: %s", symbol, reason))
		return
	}

	capital := rt.deps.Ledger.Cash()
	lotSize := rt.deps.LotSize(symbol)
	_, stopLoss, trailingSL := extractShorthand(rt.Strategy.Exit)
	stopPct := 0.0
	if stopLoss != nil {
		stopPct = *stopLoss
	} else if trailingSL != nil {
		stopPct = *trailingSL
	}

	qty, err := execution.ResolveQuantity(rt.Strategy.PositionSizing, capital, candle.Close, rt.Strategy.AssetType, lotSize, stopPct)
	if err != nil {
		rt.log("warn", fmt.Sprintf("sizing rejected for %s: %v", symbol, err))
		return
	}

	amount := candle.Close.Mul(decimal.NewFromInt(int64(qty)))
	if rt.Strategy.Portfolio != nil && rt.deps.PortfolioGuard != nil {
		if ok, reason := rt.deps.PortfolioGuard.CanDeploy(symbol, amount, rt.Strategy.Portfolio.MaxSymbolExposure, rt.Strategy.Portfolio.MaxSectorExposure); !ok {
			rt.log("info", fmt.Sprintf("entry blocked for %s: %s", symbol, reason))
			return
		}
	}

	limitPrice := candle.Close
	if rt.Strategy.Execution.Entry != "market" {
		limitPrice = execution.RoundToTick(candle.Close, rt.deps.TickSize(symbol))
	}

	fill, err := rt.deps.Broker.PlaceOrder(ctx, execution.OrderRequest{
		Symbol: symbol, Side: models.SideBuy, Product: rt.Strategy.Execution.Product,
		OrderType: rt.Strategy.Execution.OrderType, Quantity: qty, LimitPrice: limitPrice,
		SlippagePct: rt.Strategy.Execution.SlippagePct,
	})
	if err != nil || fill == nil {
		rt.log("error", fmt.Sprintf("order placement failed for %s: %v", symbol, err))
		return
	}

	order := models.Order{
		ID: uuid.NewString(), StrategyID: rt.Strategy.StrategyID, StrategyVersion: rt.Strategy.StrategyVersion,
		Symbol: symbol, Side: models.SideBuy, Product: rt.Strategy.Execution.Product, OrderType: rt.Strategy.Execution.OrderType,
		Quantity: qty, LimitPrice: limitPrice, FilledQuantity: fill.FilledQuantity, AvgFillPrice: fill.AvgPrice,
		State: fill.State, RejectReason: fill.RejectReason, CreatedAt: candle.OpenTime, UpdatedAt: candle.OpenTime,
	}
	if rt.deps.Hooks.OnOrder != nil {
		rt.deps.Hooks.OnOrder(order)
	}

	if fill.State != models.OrderFilled && fill.State != models.OrderPartial {
		return
	}

	costModel := rt.deps.Cost
	costs := costModel.Compute(models.SideBuy, rt.Strategy.Execution.Product, fill.AvgPrice, fill.FilledQuantity)
	if err := rt.deps.Ledger.ApplyBuy(symbol, fill.FilledQuantity, fill.AvgPrice, costs.Total); err != nil {
		rt.log("error", fmt.Sprintf("ledger buy failed for %s: %v", symbol, err))
		return
	}

	trade := &models.Trade{
		ID: uuid.NewString(), StrategyID: rt.Strategy.StrategyID, StrategyVersion: rt.Strategy.StrategyVersion,
		Symbol: symbol, Direction: rt.Strategy.Direction, EntryOrderID: order.ID,
		Quantity: fill.FilledQuantity, EntryPrice: fill.AvgPrice, HighWaterMark: fill.AvgPrice,
		State: models.TradeActive, EntryTime: candle.OpenTime, Costs: costs.Total,
	}
	rt.trades[symbol] = trade
	rt.entryTime[symbol] = candle.OpenTime
	atomic.AddInt32(&rt.openCount, 1)
	rt.deps.Risk.RecordEntry(symbol)
	if rt.deps.PortfolioGuard != nil {
		rt.deps.PortfolioGuard.RecordDeploy(symbol, amount)
	}
	if rt.deps.Hooks.OnTrade != nil {
		rt.deps.Hooks.OnTrade(*trade)
	}
}

func (rt *Runtime) manageOpenTrade(ctx context.Context, symbol string, candle models.Candle, trade *models.Trade) {
	if candle.High.GreaterThan(trade.HighWaterMark) {
		trade.HighWaterMark = candle.High
	}

	if rt.Strategy.Type == models.StrategyIntraday {
		if rt.Strategy.Holding.ForceSquareOff != "" && timeOfDay(candle.OpenTime) >= rt.Strategy.Holding.ForceSquareOff {
			rt.closeTrade(ctx, symbol, candle, trade, "force_square_off")
			return
		}
		if rt.Strategy.Holding.MaxOpenTradeDurationMins > 0 {
			elapsed := candle.OpenTime.Sub(rt.entryTime[symbol])
			if elapsed >= time.Duration(rt.Strategy.Holding.MaxOpenTradeDurationMins)*time.Minute {
				rt.closeTrade(ctx, symbol, candle, trade, "max_open_trade_duration")
				return
			}
		}
	}

	resolver := &ruleResolver{cache: rt.deps.Cache, symbol: symbol, defaultTimeframe: string(rt.Strategy.Timeframe), currentPrice: candle.Close, trade: trade}
	exitHit, err := rt.Strategy.Exit.Evaluate(resolver)
	if err != nil {
		return
	}

	if exitHit {
		reason := rt.attributeExitReason(resolver)
		rt.closeTrade(ctx, symbol, candle, trade, reason)
		return
	}

	if rt.Strategy.Type == models.StrategySwing && rt.Strategy.Holding.MaxDays > 0 && trade.HoldingDays >= rt.Strategy.Holding.MaxDays {
		if trade.State != models.TradeExpired {
			trade.State = models.TradeExpired
			if rt.deps.Hooks.OnTrade != nil {
				rt.deps.Hooks.OnTrade(*trade)
			}
		}
	}
}

// attributeExitReason picks which exit fired using exit_priority
// (DSL_SPEC Sec 4) — a best-effort attribution against the resolver's
// current state, since the generic condition tree only reports pass/fail,
// not which leaf fired.
func (rt *Runtime) attributeExitReason(resolver *ruleResolver) string {
	takeProfit, stopLoss, trailingSL := extractShorthand(rt.Strategy.Exit)
	priority := rt.Strategy.ExitPriority
	if len(priority) == 0 {
		priority = defaultExitPriority()
	}
	for _, key := range priority {
		switch key {
		case "stop_loss":
			if stopLoss != nil && resolver.StopLossHit(*stopLoss) {
				return "stop_loss"
			}
		case "trailing_sl":
			if trailingSL != nil && resolver.TrailingSLHit(*trailingSL) {
				return "trailing_sl"
			}
		case "take_profit":
			if takeProfit != nil && resolver.TakeProfitHit(*takeProfit) {
				return "take_profit"
			}
		}
	}
	return "signal"
}

func (rt *Runtime) closeTrade(ctx context.Context, symbol string, candle models.Candle, trade *models.Trade, reason string) {
	limitPrice := candle.Close
	if rt.Strategy.Execution.Entry != "market" {
		limitPrice = execution.RoundToTick(candle.Close, rt.deps.TickSize(symbol))
	}

	fill, err := rt.deps.Broker.PlaceOrder(ctx, execution.OrderRequest{
		Symbol: symbol, Side: models.SideSell, Product: rt.Strategy.Execution.Product,
		OrderType: rt.Strategy.Execution.OrderType, Quantity: trade.Quantity, LimitPrice: limitPrice,
		SlippagePct: rt.Strategy.Execution.SlippagePct,
	})
	if err != nil || fill == nil {
		rt.log("error", fmt.Sprintf("exit order failed for %s: %v", symbol, err))
		return
	}
	if fill.RejectReason == "circuit_limit_hit" {
		// EXIT_BLOCKED (ENGINE_SPEC Sec 3): trade stays ACTIVE, not closed.
		// The strategy's exit intent is real; the market refused the fill.
		rt.log("warn", fmt.Sprintf("exit blocked for %s: circuit_limit_hit", symbol))
		return
	}

	order := models.Order{
		ID: uuid.NewString(), StrategyID: rt.Strategy.StrategyID, StrategyVersion: rt.Strategy.StrategyVersion,
		Symbol: symbol, Side: models.SideSell, Product: rt.Strategy.Execution.Product, OrderType: rt.Strategy.Execution.OrderType,
		Quantity: trade.Quantity, LimitPrice: limitPrice, FilledQuantity: fill.FilledQuantity, AvgFillPrice: fill.AvgPrice,
		State: fill.State, RejectReason: fill.RejectReason, CreatedAt: candle.OpenTime, UpdatedAt: candle.OpenTime,
	}
	if rt.deps.Hooks.OnOrder != nil {
		rt.deps.Hooks.OnOrder(order)
	}
	if fill.State != models.OrderFilled && fill.State != models.OrderPartial {
		return
	}

	costs := rt.deps.Cost.Compute(models.SideSell, rt.Strategy.Execution.Product, fill.AvgPrice, fill.FilledQuantity)
	realized, err := rt.deps.Ledger.ApplySell(symbol, fill.FilledQuantity, fill.AvgPrice, costs.Total)
	if err != nil {
		rt.log("error", fmt.Sprintf("ledger sell failed for %s: %v", symbol, err))
		return
	}

	trade.ExitPrice = fill.AvgPrice
	trade.ExitTime = candle.OpenTime
	trade.CloseReason = reason
	trade.Costs = trade.Costs.Add(costs.Total)
	trade.PnL = realized.Sub(trade.Costs)
	trade.State = stateForReason(reason)

	cooldownBars := 0
	if rt.Strategy.Cooldown != nil {
		cooldownBars = rt.Strategy.Cooldown.Bars
	}
	rt.deps.Risk.RecordExit(symbol, rt.barIndex, cooldownBars, realized)
	if rt.deps.PortfolioGuard != nil {
		amount := trade.EntryPrice.Mul(decimal.NewFromInt(int64(trade.Quantity)))
		rt.deps.PortfolioGuard.RecordRelease(symbol, amount)
	}
	delete(rt.trades, symbol)
	atomic.AddInt32(&rt.openCount, -1)
	delete(rt.entryTime, symbol)

	if rt.deps.Hooks.OnTrade != nil {
		rt.deps.Hooks.OnTrade(*trade)
	}
}

func stateForReason(reason string) models.TradeState {
	switch reason {
	case "stop_loss", "trailing_sl":
		return models.TradeStopped
	case "take_profit":
		return models.TradeTargetHit
	default:
		return models.TradeClosed
	}
}
