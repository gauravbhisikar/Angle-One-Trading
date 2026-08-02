package api

import (
	"math/rand"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"tradingengine/internal/dsl"
	"tradingengine/internal/models"
	"tradingengine/internal/portfolio"
	"tradingengine/internal/portfolio/cost"
	"tradingengine/internal/scheduler"
	"tradingengine/internal/strategy"
)

// demoStrategies are illustrative DSL documents only — created by
// POST /dev/seed to populate the dashboard with realistic-looking cards
// before any real trading has happened. Never invoked automatically.
var demoStrategies = []string{
	`{
	  "version": "1.2", "strategy_id": "demo-ema-momentum", "strategy_name": "EMA Momentum",
	  "strategy_version": 1, "type": "swing", "asset_type": "ETF", "direction": "long", "enabled": true,
	  "timeframe": "1d", "symbols": ["NIFTYBEES"],
	  "entry": {"all": [{"indicator": "ema_cross", "fast": 20, "slow": 50, "operator": "bullish"}, {"indicator": "rsi", "operator": "<", "value": 35}]},
	  "exit": {"any": [{"indicator": "ema_cross", "operator": "bearish"}, {"take_profit": 10}, {"stop_loss": 5}]},
	  "position_sizing": {"type": "fixed_pct", "value": 10},
	  "execution": {"mode": "paper", "broker": "angel", "exchange": "NSE", "product": "CNC", "order_type": "MARKET", "entry": "market", "slippage_pct": 0.05},
	  "risk": {"max_daily_loss": 5, "max_positions": 5}, "holding": {"max_days": 15},
	  "cost_model": "angel_equity", "benchmark": "NIFTYBEES",
	  "metadata": {"author": "AI", "description": "EMA(20/50) bullish cross with RSI oversold filter"}
	}`,
	`{
	  "version": "1.2", "strategy_id": "demo-vwap-reversion", "strategy_name": "VWAP Reversion",
	  "strategy_version": 1, "type": "intraday", "asset_type": "STOCK", "direction": "long", "enabled": true,
	  "timeframe": "5m", "symbols": ["RELIANCE"],
	  "entry": {"all": [{"indicator": "vwap", "operator": "crosses_above"}, {"indicator": "volume", "operator": "spike_pct", "value": 150}]},
	  "exit": {"any": [{"take_profit": 1.5}, {"stop_loss": 0.8}]},
	  "position_sizing": {"type": "risk_based", "value": 1},
	  "execution": {"mode": "paper", "broker": "zerodha", "exchange": "NSE", "product": "MIS", "order_type": "MARKET", "entry": "market", "slippage_pct": 0.05},
	  "session": {"entry_start": "09:20", "entry_end": "14:45"},
	  "risk": {"max_daily_loss": 3, "max_positions": 3}, "holding": {"force_square_off": "15:20"},
	  "cost_model": "zerodha_equity", "benchmark": "NIFTYBEES",
	  "metadata": {"author": "AI", "description": "VWAP reversion scalp on volume spike"}
	}`,
	`{
	  "version": "1.2", "strategy_id": "demo-breakout-hunter", "strategy_name": "Breakout Hunter",
	  "strategy_version": 1, "type": "swing", "asset_type": "STOCK", "direction": "long", "enabled": true,
	  "timeframe": "1d", "symbols": ["TATASTEEL"],
	  "entry": {"all": [{"indicator": "donchian_channel", "operator": "breakout_up"}, {"indicator": "adx", "operator": ">", "value": 20}]},
	  "exit": {"any": [{"indicator": "supertrend", "operator": "bearish"}, {"take_profit": 8}, {"stop_loss": 4}]},
	  "position_sizing": {"type": "fixed_pct", "value": 8},
	  "execution": {"mode": "paper", "broker": "dhan", "exchange": "NSE", "product": "CNC", "order_type": "MARKET", "entry": "market", "slippage_pct": 0.05},
	  "risk": {"max_daily_loss": 5, "max_positions": 4}, "holding": {"max_days": 15},
	  "cost_model": "dhan_equity", "benchmark": "NIFTYBEES",
	  "metadata": {"author": "AI", "description": "Donchian breakout with ADX trend filter"}
	}`,
}

// seedPlan controls each demo strategy's fabricated trade history: how
// many trades, its win rate, and what run state the dashboard should show.
type seedPlan struct {
	trades    int
	winRate   float64
	basePrice float64
	runState  string // running | paused | stopped
}

var seedPlans = map[string]seedPlan{
	"demo-ema-momentum":    {trades: 9, winRate: 0.75, basePrice: 245, runState: "running"},
	"demo-vwap-reversion":  {trades: 14, winRate: 0.5, basePrice: 2950, runState: "paused"},
	"demo-breakout-hunter": {trades: 8, winRate: 0.3, basePrice: 165, runState: "stopped"},
}

func (s *Server) handleSeedDemo(w http.ResponseWriter, r *http.Request) {
	created := []string{}

	for seedIdx, raw := range demoStrategies {
		// Independent rng per strategy (not one shared sequential stream) so
		// each strategy's realized win rate reliably tracks its target
		// instead of drifting based on draw order across all three.
		rng := rand.New(rand.NewSource(int64(100 + seedIdx)))
		strat, err := dsl.Parse([]byte(raw))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "seed parse: "+err.Error())
			return
		}
		if res := dsl.Validate(strat); !res.Valid() {
			writeError(w, http.StatusInternalServerError, "seed validate failed for "+strat.StrategyID)
			return
		}
		if err := s.Strategies.SaveVersion(strat, []byte(raw)); err != nil {
			writeError(w, http.StatusInternalServerError, "seed save: "+err.Error())
			return
		}

		plan := seedPlans[strat.StrategyID]
		ledger := s.getLedger(strat.StrategyID)
		costModel, _ := cost.Get(strat.CostModel)
		s.fabricateTrades(strat, ledger, costModel, plan, rng)

		hooks := strategy.Hooks{
			OnOrder: func(o models.Order) { s.Orders.Insert(o) },
			OnTrade: func(t models.Trade) { s.Trades.Upsert(t) },
			OnLog:   func(id, level, msg string) { s.Logs.Insert(id, level, msg) },
		}
		if _, err := s.Engine.RunStrategy(strat, ledger, s.Broker, hooks); err != nil && err != scheduler.ErrConcurrencyLimitReached {
			writeError(w, http.StatusInternalServerError, "seed run: "+err.Error())
			return
		}
		s.setStatus(strat.StrategyID, "running")
		switch plan.runState {
		case "paused":
			s.Engine.PauseStrategy(strat.StrategyID)
			s.setStatus(strat.StrategyID, "paused")
		case "stopped":
			s.Engine.StopStrategy(strat.StrategyID)
			s.setStatus(strat.StrategyID, "stopped")
		}

		s.Logs.Insert(strat.StrategyID, "info", "Seeded with demo trade history for dashboard preview")
		created = append(created, strat.StrategyID)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"seeded": created})
}

// fabricateTrades writes plan.trades closed trades spread over the past
// two weeks directly into the ledger and DB, so the dashboard has
// something realistic to chart before any live trading has happened.
func (s *Server) fabricateTrades(strat *dsl.Strategy, ledger *portfolio.Ledger, costModel cost.Model, plan seedPlan, rng *rand.Rand) {
	symbol := strat.Symbols[0]
	price := plan.basePrice
	now := time.Now().Add(-15 * 24 * time.Hour)

	for i := 0; i < plan.trades; i++ {
		entryTime := now.Add(time.Duration(i) * 30 * time.Hour)
		win := rng.Float64() < plan.winRate
		movePct := 1.5 + rng.Float64()*3
		if !win {
			movePct = -(0.8 + rng.Float64()*2.5)
		}
		entryPrice := decimal.NewFromFloat(price).Round(2)
		exitPrice := decimal.NewFromFloat(price * (1 + movePct/100)).Round(2)
		// Keep per-trade notional in a modest, price-independent band
		// (~₹8,000-14,000) so a run of losses on an expensive symbol like
		// RELIANCE can't exhaust the ₹100,000 demo ledger.
		qty := int(8000/price) + rng.Intn(4) + 1

		entryCosts := costModel.Compute(models.SideBuy, strat.Execution.Product, entryPrice, qty)
		exitCosts := costModel.Compute(models.SideSell, strat.Execution.Product, exitPrice, qty)

		if err := ledger.ApplyBuy(symbol, qty, entryPrice, entryCosts.Total); err != nil {
			continue // ran out of fabricated cash — stop adding more for this strategy
		}
		realized, err := ledger.ApplySell(symbol, qty, exitPrice, exitCosts.Total)
		if err != nil {
			continue
		}

		totalCosts := entryCosts.Total.Add(exitCosts.Total)
		reason := "signal"
		state := models.TradeClosed
		if win {
			reason, state = "take_profit", models.TradeTargetHit
		} else {
			reason, state = "stop_loss", models.TradeStopped
		}

		trade := models.Trade{
			ID: uuid.NewString(), StrategyID: strat.StrategyID, StrategyVersion: strat.StrategyVersion,
			Symbol: symbol, Direction: strat.Direction, Quantity: qty,
			EntryPrice: entryPrice, ExitPrice: exitPrice, HighWaterMark: exitPrice,
			State: state, CloseReason: reason,
			EntryTime: entryTime, ExitTime: entryTime.Add(6 * time.Hour),
			HoldingDays: 1, PnL: realized.Sub(totalCosts), Costs: totalCosts,
		}
		s.Trades.Upsert(trade)

		price = exitPrice.InexactFloat64()
	}
}
