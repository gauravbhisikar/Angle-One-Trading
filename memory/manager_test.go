package memory_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"memory"
)

func TestMemoryFullLifecycle(t *testing.T) {
	ctx := context.Background()
	m, err := memory.Open(ctx, filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer m.Close()

	// 1. Strategy versioning — never overwritten, new row per version.
	v1 := memory.StrategyRecord{
		StrategyID: "momentum-v1", Version: 1, Name: "Momentum Breakout",
		DSLJSON: `{"strategy_name":"Momentum Breakout"}`, Objective: "beat_nifty",
		Style: "swing", Risk: "moderate", Status: "backtest",
	}
	if err := m.SaveStrategy(ctx, v1); err != nil {
		t.Fatalf("SaveStrategy v1: %v", err)
	}
	v2 := memory.StrategyRecord{
		StrategyID: "momentum-v1", Version: 2, ParentStrategyID: "momentum-v1", Name: "Momentum Breakout",
		DSLJSON: `{"strategy_name":"Momentum Breakout v2"}`, Objective: "beat_nifty",
		Style: "swing", Risk: "moderate", Status: "paper",
		ChangeReason: "widened stop-loss from 5% to 7% after high-VIX whipsaw losses",
	}
	if err := m.SaveStrategy(ctx, v2); err != nil {
		t.Fatalf("SaveStrategy v2: %v", err)
	}
	history, err := m.GetStrategyHistory(ctx, "momentum-v1")
	if err != nil {
		t.Fatalf("GetStrategyHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(history))
	}
	if history[1].ChangeReason == "" {
		t.Fatal("expected v2's change reason to survive round-trip")
	}

	// 2. Context snapshot — what the AI saw when building this version.
	if err := m.SaveContext(ctx, memory.ContextSnapshot{
		StrategyID: "momentum-v1", Version: 1, MarketRegime: "bull", VIX: "12.4",
		FIINet: "2450000000", DIINet: "-800000000", BreadthADRatio: "2.94",
		NewsSentiment: "bullish", NewsScore: "0.62", PCR: "0.87", RSI: "58", Trend: "up", VolumeRegime: "normal",
	}); err != nil {
		t.Fatalf("SaveContext: %v", err)
	}
	got, err := m.GetContextForStrategy(ctx, "momentum-v1", 1)
	if err != nil {
		t.Fatalf("GetContextForStrategy: %v", err)
	}
	if got.VIX != "12.4" || got.MarketRegime != "bull" {
		t.Fatalf("context round-trip mismatch: %+v", got)
	}

	// 3. Backtests — v1 loses, v2 wins, drives GetSuccessful/GetFailed.
	if err := m.SaveBacktest(ctx, memory.BacktestRecord{
		StrategyID: "momentum-v1", Version: 1, CAGR: "-2.1", Sharpe: "0.3", TotalTrades: 9,
	}); err != nil {
		t.Fatalf("SaveBacktest v1: %v", err)
	}
	if err := m.SaveBacktest(ctx, memory.BacktestRecord{
		StrategyID: "momentum-v1", Version: 2, CAGR: "8.4", Sharpe: "1.7", TotalTrades: 11,
	}); err != nil {
		t.Fatalf("SaveBacktest v2: %v", err)
	}

	// 4. Deployment lifecycle.
	dep := memory.Deployment{DeploymentID: "dep-1", StrategyID: "momentum-v1", Version: 2, Mode: "paper", Status: "running"}
	if err := m.SaveDeployment(ctx, dep); err != nil {
		t.Fatalf("SaveDeployment: %v", err)
	}
	current, err := m.GetCurrentDeployments(ctx)
	if err != nil || len(current) != 1 {
		t.Fatalf("expected 1 current deployment, got %d (err=%v)", len(current), err)
	}
	if err := m.UpdateDeploymentStatus(ctx, "dep-1", "momentum-v1", "paused"); err != nil {
		t.Fatalf("UpdateDeploymentStatus: %v", err)
	}

	// 5. Trade lifecycle — open then close, same ID, upsert not duplicate.
	entryTime := time.Now().Add(-48 * time.Hour)
	if err := m.SaveTrade(ctx, memory.TradeRecord{
		ID: "trade-1", DeploymentID: "dep-1", StrategyID: "momentum-v1", Symbol: "NIFTYBEES",
		EntryTime: entryTime, EntryPrice: "250.50", Quantity: 40,
	}); err != nil {
		t.Fatalf("SaveTrade open: %v", err)
	}
	exitTime := time.Now()
	if err := m.SaveTrade(ctx, memory.TradeRecord{
		ID: "trade-1", DeploymentID: "dep-1", StrategyID: "momentum-v1", Symbol: "NIFTYBEES",
		EntryTime: entryTime, ExitTime: &exitTime, EntryPrice: "250.50", ExitPrice: "262.10",
		Quantity: 40, PnL: "464.00", HoldingDays: 2, ExitReason: "take_profit",
	}); err != nil {
		t.Fatalf("SaveTrade close: %v", err)
	}
	trades, err := m.GetTradesForStrategy(ctx, "momentum-v1")
	if err != nil || len(trades) != 1 {
		t.Fatalf("expected exactly 1 trade (upserted, not duplicated), got %d (err=%v)", len(trades), err)
	}
	if trades[0].ExitReason != "take_profit" {
		t.Fatalf("expected the close to have overwritten the open row, got %+v", trades[0])
	}

	// 6. Daily snapshot.
	if err := m.SaveDailySnapshot(ctx, memory.DailySnapshot{
		DeploymentID: "dep-1", Date: time.Now().Format("2006-01-02"),
		PortfolioValue: "100464.00", TodayReturnPct: "0.46", TotalReturnPct: "0.46",
		OpenPositions: 0, MarketRegime: "bull",
	}); err != nil {
		t.Fatalf("SaveDailySnapshot: %v", err)
	}
	snaps, err := m.GetDailySnapshots(ctx, "dep-1")
	if err != nil || len(snaps) != 1 {
		t.Fatalf("expected 1 daily snapshot, got %d (err=%v)", len(snaps), err)
	}

	// 7. Review — reasoning, not numbers.
	if err := m.SaveReview(ctx, memory.Review{
		StrategyID: "momentum-v1", Version: 2, ReviewDate: time.Now().Format("2006-01-02"),
		Summary:   "Strategy performing in line with backtest expectations.",
		Strengths: []string{"Low drawdown", "Consistent entries"}, Weaknesses: []string{"Underperforms in sideways markets"},
		RecommendedChanges: []string{"Add ADX filter to avoid low-trend regimes"}, Confidence: "0.81",
	}); err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	reviews, err := m.GetReviewsForStrategy(ctx, "momentum-v1")
	if err != nil || len(reviews) != 1 || len(reviews[0].Strengths) != 2 {
		t.Fatalf("review round-trip failed: %+v (err=%v)", reviews, err)
	}

	// 8. Lessons — aggregated experience across many strategies.
	for i := 0; i < 12; i++ {
		success := i%4 != 0 // 9 success, 3 fail out of 12
		if err := m.RecordLesson(ctx, "high_vix_ema_crossover", "EMA crossover strategies during high VIX periods", success); err != nil {
			t.Fatalf("RecordLesson: %v", err)
		}
	}
	lessons, err := m.GetLessons(ctx)
	if err != nil || len(lessons) != 1 {
		t.Fatalf("expected 1 lesson, got %d (err=%v)", len(lessons), err)
	}
	if lessons[0].TimesSeen != 12 || lessons[0].TimesSuccess != 9 || lessons[0].TimesFailed != 3 {
		t.Fatalf("lesson counters wrong: %+v", lessons[0])
	}
	if lessons[0].Confidence < 0.7 || lessons[0].Confidence > 0.8 {
		t.Fatalf("expected confidence ~0.75, got %v", lessons[0].Confidence)
	}

	// 9. GetSuccessfulStrategies / GetFailedStrategies driven by latest backtest CAGR.
	successful, err := m.GetSuccessfulStrategies(ctx, 10)
	if err != nil {
		t.Fatalf("GetSuccessfulStrategies: %v", err)
	}
	if len(successful) != 1 || successful[0].Version != 2 {
		t.Fatalf("expected v2 (positive CAGR) as the successful version, got %+v", successful)
	}

	// 10. Full audit trail — every event replayable in order.
	events, err := m.EventsForStrategy(ctx, "momentum-v1")
	if err != nil {
		t.Fatalf("EventsForStrategy: %v", err)
	}
	if len(events) < 6 {
		t.Fatalf("expected at least 6 events (created x2, backtested x2, deployed, status change, trade open/close, review), got %d", len(events))
	}
	t.Logf("full lifecycle recorded %d immutable events for momentum-v1", len(events))
}
