package contextbuilder_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"contextbuilder"
	"memory"
)

const engineURL = "http://localhost:8099"

func engineReachable() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(engineURL + "/market/status")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func TestBuildStrategyContext(t *testing.T) {
	if testing.Short() {
		t.Skip("network + live-engine test")
	}
	if !engineReachable() {
		t.Skip("engine not running at " + engineURL + " — start it before running this test (see .claude/skills/run-engine)")
	}

	ctx := context.Background()
	mgr, err := memory.Open(ctx, filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	defer mgr.Close()

	// Seed enough memory data that MemoryProvider and RecommendationProvider
	// have something real to react to.
	if err := mgr.SaveStrategy(ctx, memory.StrategyRecord{
		StrategyID: "seed-momentum", Version: 1, Name: "Seed Momentum", Status: "paper", DSLJSON: "{}",
	}); err != nil {
		t.Fatalf("SaveStrategy: %v", err)
	}
	if err := mgr.SaveBacktest(ctx, memory.BacktestRecord{StrategyID: "seed-momentum", Version: 1, CAGR: "9.1", Sharpe: "1.4", TotalTrades: 12}); err != nil {
		t.Fatalf("SaveBacktest: %v", err)
	}
	for i := 0; i < 8; i++ {
		success := i < 2 // 2 success, 6 fail: confidence 0.25, below the 0.35 threshold
		if err := mgr.RecordLesson(ctx, "high_vix_momentum", "momentum strategies underperform when VIX is elevated", success); err != nil {
			t.Fatalf("RecordLesson: %v", err)
		}
	}

	builder := contextbuilder.NewBuilder(
		contextbuilder.NewMarketProvider(engineURL),
		contextbuilder.NewPortfolioProvider(engineURL),
		contextbuilder.NewMemoryProvider(mgr),
		contextbuilder.NewRegimeProvider(),
		contextbuilder.NewRecommendationProvider(),
	)

	dc, err := builder.Build(ctx, contextbuilder.BuildRequest{
		Task: contextbuilder.TaskBuildStrategy, Symbol: "NIFTYBEES", EngineBaseURL: engineURL,
		UserPreferences: contextbuilder.UserContext{Style: "swing", Risk: "moderate", Objective: "beat_nifty", Capital: 100000},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	t.Logf("market: price=%s vix=%s trend=%s rsi=%s overnight=%s(%s, conf=%.2f)",
		dc.Market.Price, dc.Market.VIX, dc.Market.Trend, dc.Market.RSI14, dc.Market.Overnight, dc.Market.OvernightChangePct, dc.Market.OvernightConfidence)
	t.Logf("regime: %s (confidence %.2f) — %s", dc.Regime.Regime, dc.Regime.Confidence, dc.Regime.Basis)
	t.Logf("recommendations: recommend=%v avoid=%v", dc.Recommendations.RecommendedStyles, dc.Recommendations.Avoid)
	t.Logf("lessons loaded: %d", len(dc.Lessons))
	t.Logf("portfolio: running=%d cash=%s", dc.Portfolio.RunningStrategies, dc.Portfolio.TotalCash)
	if len(dc.Warnings) > 0 {
		t.Logf("warnings (expected for best-effort NSE endpoints in this sandbox): %v", dc.Warnings)
	}

	if dc.Market.Symbol != "NIFTYBEES" {
		t.Fatalf("expected symbol NIFTYBEES, got %s", dc.Market.Symbol)
	}
	if dc.Regime.Regime == "" {
		t.Fatal("expected a non-empty regime classification")
	}
	if len(dc.Lessons) == 0 {
		t.Fatal("expected the seeded lesson to come back")
	}
	// The seeded lesson (confidence 0.25, momentum underperforms in high
	// VIX) should have flipped "momentum" from recommended to avoided,
	// UNLESS the regime itself already avoided momentum (e.g. sideways
	// regime playbook already excludes it) — either way momentum must not
	// be silently left in "recommended" while the lesson explicitly warns
	// against it.
	for _, s := range dc.Recommendations.RecommendedStyles {
		if s == "momentum" {
			t.Fatalf("expected momentum to be moved to avoid given the seeded low-confidence lesson, recommendations=%+v", dc.Recommendations)
		}
	}
}
