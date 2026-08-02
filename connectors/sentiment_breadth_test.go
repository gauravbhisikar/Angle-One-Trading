package connectors_test

import (
	"context"
	"testing"
	"time"

	"connectors/httpx"
	"connectors/news"
	"connectors/nse"
	"connectors/sentiment"
)

func TestSentimentScoring(t *testing.T) {
	bullish := sentiment.Score("Nifty rallies to record high as FII inflows surge, markets upbeat")
	if bullish.Label != sentiment.Bullish {
		t.Fatalf("expected bullish, got %s (score=%.2f)", bullish.Label, bullish.Score)
	}

	bearish := sentiment.Score("Sensex crashes on recession fears, FII outflows weigh on sentiment")
	if bearish.Label != sentiment.Bearish {
		t.Fatalf("expected bearish, got %s (score=%.2f)", bearish.Label, bearish.Score)
	}

	negated := sentiment.Score("Market not bullish despite early gains")
	t.Logf("negation test: %q -> score=%.2f label=%s", "Market not bullish despite early gains", negated.Score, negated.Label)

	neutral := sentiment.Score("NSE announces trading calendar for next month")
	if neutral.Label != sentiment.Neutral {
		t.Fatalf("expected neutral, got %s (score=%.2f)", neutral.Label, neutral.Score)
	}
}

func TestSentimentOnRealNews(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := httpx.New()

	headlines, err := news.FetchAll(ctx, client)
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	agg := sentiment.ScoreHeadlines(headlines)
	t.Logf("scored %d real headlines: bullish=%d neutral=%d bearish=%d avg_score=%.3f overall=%s",
		len(headlines), agg.Bullish, agg.Neutral, agg.Bearish, agg.AverageScore, agg.Label)
	if len(agg.Scored) != len(headlines) {
		t.Fatalf("expected every headline scored, got %d of %d", len(agg.Scored), len(headlines))
	}
}

func TestNSEMarketBreadth(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := httpx.New()

	breadth, err := nse.FetchMarketBreadth(ctx, client)
	if err != nil {
		t.Skipf("NSE market breadth unreachable from this environment (expected to work from a normal connection): %v", err)
	}
	if breadth.Advances+breadth.Declines == 0 {
		t.Fatal("expected non-zero advances+declines")
	}
	t.Logf("market breadth: advances=%d declines=%d unchanged=%d A/D=%s new_highs=%d new_lows=%d timestamp=%s",
		breadth.Advances, breadth.Declines, breadth.Unchanged, breadth.AdvanceDecline,
		breadth.NewHighs, breadth.NewLows, breadth.Timestamp.Format("2006-01-02 15:04:05"))
}
