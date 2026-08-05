package strategy_test

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/dsl"
	"tradingengine/internal/execution"
	"tradingengine/internal/indicators"
	"tradingengine/internal/models"
	"tradingengine/internal/portfolio"
	"tradingengine/internal/portfolio/cost"
	"tradingengine/internal/risk"
	"tradingengine/internal/strategy"
)

const smokeDSL = `{
  "version": "1.2",
  "strategy_id": "smoke-1",
  "strategy_name": "Smoke Test",
  "strategy_version": 1,
  "type": "swing",
  "asset_type": "ETF",
  "direction": "long",
  "enabled": true,
  "timeframe": "1m",
  "symbols": ["TESTSTOCK"],
  "entry": { "all": [ { "indicator": "close", "operator": ">", "value": 1 } ] },
  "exit": { "any": [ { "take_profit": 5 }, { "stop_loss": 5 } ] },
  "position_sizing": { "type": "fixed_pct", "value": 10 },
  "execution": { "mode": "paper", "broker": "angel", "exchange": "NSE", "product": "CNC",
    "order_type": "MARKET", "entry": "market", "slippage_pct": 0 },
  "risk": { "max_daily_loss": 5, "max_positions": 5 },
  "holding": { "max_days": 15 },
  "cost_model": "angel_equity",
  "benchmark": "NIFTYBEES"
}`

// candle builds a 1m candle at a fixed intraday time (within NSE market
// hours) on a given day offset, so the test never depends on wall-clock
// time — unlike the live mock-feed path, which correctly refuses to trade
// outside 09:15-15:30 (ENGINE_SPEC Sec 6).
func candle(dayOffset int, price float64) models.Candle {
	base := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC).AddDate(0, 0, dayOffset)
	p := decimal.NewFromFloat(price)
	return models.Candle{
		Symbol: "TESTSTOCK", Timeframe: models.TF1m,
		OpenTime: base, CloseTime: base.Add(time.Minute),
		Open: p, High: p, Low: p, Close: p, Volume: 1000, Closed: true,
	}
}

func TestRuntimeEntryAndExit(t *testing.T) {
	s, err := dsl.Parse([]byte(smokeDSL))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res := dsl.Validate(s); !res.Valid() {
		t.Fatalf("validate: %+v", res.Errors)
	}

	cache := indicators.NewCache()
	ledger := portfolio.NewLedger(decimal.NewFromInt(100000))
	riskState := risk.NewState(s.Risk.MaxPositions, s.Risk.MaxDailyLoss, ledger.Cash())
	guard := risk.NewPortfolioGuard(decimal.NewFromInt(100000), nil)
	costModel, err := cost.Get(s.CostModel)
	if err != nil {
		t.Fatalf("cost.Get: %v", err)
	}

	price := decimal.NewFromInt(100)
	broker := execution.NewPaperBroker(execution.FillBasic, func(string) (decimal.Decimal, bool) { return price, true })

	var loggedOrders []models.Order
	var loggedTrades []models.Trade
	hooks := strategy.Hooks{
		OnOrder: func(o models.Order) { loggedOrders = append(loggedOrders, o) },
		OnTrade: func(tr models.Trade) { loggedTrades = append(loggedTrades, tr) },
	}

	rt := strategy.NewRuntime(s, strategy.Deps{
		Cache: cache, Broker: broker, Ledger: ledger, Risk: riskState, PortfolioGuard: guard,
		Cost: costModel, Hooks: hooks,
	})
	if err := rt.Subscribe(); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	ctx := context.Background()

	// Day 0 at price 100: entry condition (close > 1) is true immediately,
	// no warmup needed — should open a trade.
	c0 := candle(0, 100)
	cache.OnCandleClose("TESTSTOCK", string(models.TF1m), c0)
	rt.OnCandleClose(ctx, "TESTSTOCK", models.TF1m, c0)
	if len(loggedTrades) != 1 {
		t.Fatalf("expected 1 trade opened on day 0, got %d (orders=%d)", len(loggedTrades), len(loggedOrders))
	}
	if loggedTrades[0].State != models.TradeActive {
		t.Fatalf("expected trade ACTIVE after entry, got %s", loggedTrades[0].State)
	}
	if cash := ledger.Cash(); cash.GreaterThanOrEqual(decimal.NewFromInt(100000)) {
		t.Fatalf("expected cash to decrease after buy, got %s", cash)
	}

	// Day 1 at price 110 (+10%): take_profit(5%) should fire and close it.
	price = decimal.NewFromInt(110)
	c1 := candle(1, 110)
	cache.OnCandleClose("TESTSTOCK", string(models.TF1m), c1)
	rt.OnCandleClose(ctx, "TESTSTOCK", models.TF1m, c1)

	last := loggedTrades[len(loggedTrades)-1]
	if last.State != models.TradeTargetHit {
		t.Fatalf("expected TARGET_HIT after take_profit, got %s (reason=%s)", last.State, last.CloseReason)
	}
	if last.CloseReason != "take_profit" {
		t.Fatalf("expected close reason take_profit, got %s", last.CloseReason)
	}
	if !last.PnL.GreaterThan(decimal.Zero) {
		t.Fatalf("expected positive PnL on a 10%% favorable move, got %s", last.PnL)
	}

	if err := ledger.Reconcile(); err != nil {
		t.Fatalf("ledger failed to reconcile after full round trip: %v", err)
	}

	t.Logf("orders=%d trades=%d final_cash=%s final_pnl=%s", len(loggedOrders), len(loggedTrades), ledger.Cash(), last.PnL)
}

// TestEntryGateConvertsToIST is the regression test for a real bug found
// live 2026-08-05: tryEntry's market-hours gate compared candle.OpenTime's
// raw formatted time-of-day directly against IST wall-clock constants
// ("09:15"/"15:30") with no timezone conversion — internal/marketsession
// does this correctly (converts to IST first), internal/strategy did not.
// A UTC candle timestamp during real IST market hours got silently
// rejected (06:26 UTC = 11:56 IST, but "06:26" < "09:15" as a bare
// string), and — because it happened to number-match — a UTC timestamp
// clearly AFTER real IST market close was silently allowed (12:00 UTC =
// 17:30 IST, well past close, but "12:00" reads as within the window as a
// bare string). The existing smoke test above never caught this because
// its fixed 10:00 UTC candle coincidentally falls inside the window either
// way, with or without IST conversion.
func TestEntryGateConvertsToIST(t *testing.T) {
	newRuntime := func() (*strategy.Runtime, *indicators.Cache, *[]models.Trade) {
		s, err := dsl.Parse([]byte(smokeDSL))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		cache := indicators.NewCache()
		ledger := portfolio.NewLedger(decimal.NewFromInt(100000))
		riskState := risk.NewState(s.Risk.MaxPositions, s.Risk.MaxDailyLoss, ledger.Cash())
		costModel, err := cost.Get(s.CostModel)
		if err != nil {
			t.Fatalf("cost.Get: %v", err)
		}
		broker := execution.NewPaperBroker(execution.FillBasic, func(string) (decimal.Decimal, bool) {
			return decimal.NewFromInt(100), true
		})
		var trades []models.Trade
		rt := strategy.NewRuntime(s, strategy.Deps{
			Cache: cache, Broker: broker, Ledger: ledger, Risk: riskState,
			Cost: costModel, Hooks: strategy.Hooks{OnTrade: func(tr models.Trade) { trades = append(trades, tr) }},
		})
		if err := rt.Subscribe(); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		return rt, cache, &trades
	}
	buildCandle := func(t time.Time) models.Candle {
		p := decimal.NewFromInt(100)
		return models.Candle{
			Symbol: "TESTSTOCK", Timeframe: models.TF1m, OpenTime: t, CloseTime: t.Add(time.Minute),
			Open: p, High: p, Low: p, Close: p, Volume: 1000, Closed: true,
		}
	}

	t.Run("UTC timestamp during real IST market hours must enter", func(t *testing.T) {
		rt, cache, trades := newRuntime()
		// 06:26 UTC == 11:56 IST — squarely inside 09:15-15:30 IST.
		c := buildCandle(time.Date(2026, 8, 5, 6, 26, 0, 0, time.UTC))
		cache.OnCandleClose("TESTSTOCK", string(models.TF1m), c)
		rt.OnCandleClose(context.Background(), "TESTSTOCK", models.TF1m, c)
		if len(*trades) != 1 {
			t.Fatalf("expected 1 trade during real IST market hours, got %d — market-hours gate is comparing the wrong timezone", len(*trades))
		}
	})

	t.Run("UTC timestamp after real IST market close must not enter", func(t *testing.T) {
		rt, cache, trades := newRuntime()
		// 12:00 UTC == 17:30 IST — well after the 15:30 IST close, even
		// though "12:00" reads as inside the 09:15-15:30 window as a bare
		// string (the exact false-allow the old bug produced).
		c := buildCandle(time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC))
		cache.OnCandleClose("TESTSTOCK", string(models.TF1m), c)
		rt.OnCandleClose(context.Background(), "TESTSTOCK", models.TF1m, c)
		if len(*trades) != 0 {
			t.Fatalf("expected 0 trades after real IST market close, got %d — market-hours gate let a post-close candle through", len(*trades))
		}
	})
}

func TestConcurrencyLimitIsRealCap(t *testing.T) {
	// Sanity check that ResolveQuantity rejects rather than rounds up when
	// capital is too small for one share (ENGINE_SPEC Sec 1.1) — the other
	// half of "well optimized, low resource" is also "correct by construction",
	// not just fast.
	_, err := execution.ResolveQuantity(dsl.PositionSizing{Type: "fixed_amount", Value: 5}, decimal.NewFromInt(100000), decimal.NewFromInt(100), models.AssetETF, 0, 0)
	if err != execution.ErrZeroQuantity {
		t.Fatalf("expected ErrZeroQuantity for amount smaller than one share's price, got %v", err)
	}
}
