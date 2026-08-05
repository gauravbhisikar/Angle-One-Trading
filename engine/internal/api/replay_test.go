package api

import (
	"testing"

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

const replayTestDSL = `{
  "version": "1.2",
  "strategy_id": "replay-test-1",
  "strategy_name": "Replay Test",
  "strategy_version": 1,
  "type": "swing",
  "asset_type": "ETF",
  "direction": "long",
  "enabled": true,
  "timeframe": "1d",
  "symbols": ["NIFTYBEES"],
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

// TestReplayTradesReconstructsLedgerAndRuntime is the regression test for a
// real bug found live 2026-08-05: after a redeploy, a strategy with an
// open position came back showing full starting cash (as if nothing had
// ever been bought) and 0 open positions on the card — the in-memory
// ledger/runtime had no idea a real trade existed, risking a duplicate
// entry on the next candle and an orphaned position that would never get
// its exits checked again. replayTrades must reconstruct both from the
// persisted trade history alone.
func TestReplayTradesReconstructsLedgerAndRuntime(t *testing.T) {
	s, err := dsl.Parse([]byte(replayTestDSL))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	startingCapital := decimal.NewFromInt(100000)
	ledger := portfolio.NewLedger(startingCapital)
	costModel, err := cost.Get(s.CostModel)
	if err != nil {
		t.Fatalf("cost.Get: %v", err)
	}
	broker := execution.NewPaperBroker(execution.FillBasic, func(string) (decimal.Decimal, bool) {
		return decimal.NewFromInt(280), true
	})
	rt := strategy.NewRuntime(s, strategy.Deps{
		Cache: indicators.NewCache(), Broker: broker, Ledger: ledger,
		Risk: risk.NewState(s.Risk.MaxPositions, s.Risk.MaxDailyLoss, ledger.Cash()),
		Cost: costModel,
	})
	if err := rt.Subscribe(); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// A trade closed BEFORE this process started: bought 10 @ 100 (cost 20
	// total, entry+exit combined per lifecycle.go's accounting), sold 10 @
	// 110 for a 100 gross profit.
	closed := models.Trade{
		ID: "closed-1", Symbol: "NIFTYBEES", Quantity: 10,
		EntryPrice: decimal.NewFromInt(100), ExitPrice: decimal.NewFromInt(110),
		Costs: decimal.NewFromInt(20), State: models.TradeClosed,
	}
	// A trade still open when this process started: bought 17 @ 280.26,
	// cost 7.07 -- exactly the real numbers from the live bug report.
	open := models.Trade{
		ID: "open-1", Symbol: "NIFTYBEES", Quantity: 17,
		EntryPrice: decimal.NewFromFloat(280.26), Costs: decimal.NewFromFloat(7.07),
		State: models.TradeActive,
	}

	if err := replayTrades([]models.Trade{closed, open}, ledger, rt); err != nil {
		t.Fatalf("replayTrades: %v", err)
	}

	// Expected cash: start -20 (closed trade's total cost) +100 (gross
	// profit on the closed trade) - (17*280.26 + 7.07) (open trade's cost
	// basis, still tied up).
	openCostBasis := decimal.NewFromFloat(280.26).Mul(decimal.NewFromInt(17)).Add(decimal.NewFromFloat(7.07))
	expectedCash := startingCapital.Sub(decimal.NewFromInt(20)).Add(decimal.NewFromInt(100)).Sub(openCostBasis)
	if !ledger.Cash().Equal(expectedCash) {
		t.Fatalf("Cash() = %s, want %s (closed trade's PnL should be realized, open trade's cost basis should be tied up)", ledger.Cash(), expectedCash)
	}

	pos, held := ledger.Position("NIFTYBEES")
	if !held {
		t.Fatal("expected NIFTYBEES to still be held after replay (the open trade)")
	}
	if pos.Quantity != 17 {
		t.Fatalf("held quantity = %d, want 17 (the closed trade's 10 shares should have been fully sold out of the ledger)", pos.Quantity)
	}

	if got := rt.OpenPositionCount(); got != 1 {
		t.Fatalf("OpenPositionCount() = %d, want 1 — the runtime must know about the still-open trade or it will re-enter on the next candle", got)
	}
}
