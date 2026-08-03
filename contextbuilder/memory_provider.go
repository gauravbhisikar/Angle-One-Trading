package contextbuilder

import (
	"context"

	"memory"
)

// MemoryProvider fills StrategyMemory and Lessons from the persistent
// memory store — "never rely on chat history" (memory/README.md's golden
// rule) applies here too: every build_strategy call queries fresh.
type MemoryProvider struct {
	mgr *memory.Manager
}

func NewMemoryProvider(mgr *memory.Manager) *MemoryProvider {
	return &MemoryProvider{mgr: mgr}
}

func (p *MemoryProvider) Name() string { return "memory" }

func (p *MemoryProvider) Load(ctx context.Context, req BuildRequest, dc *DecisionContext) error {
	var smc StrategyMemoryContext

	if successful, err := p.mgr.GetSuccessfulStrategies(ctx, 10); err == nil {
		smc.Successful = p.toStrategySummaries(ctx, successful)
	} else {
		dc.Warnings = append(dc.Warnings, "successful strategies: "+err.Error())
	}
	if failed, err := p.mgr.GetFailedStrategies(ctx, 10); err == nil {
		smc.Failed = p.toStrategySummaries(ctx, failed)
	} else {
		dc.Warnings = append(dc.Warnings, "failed strategies: "+err.Error())
	}

	if req.StrategyID != "" {
		if history, err := p.mgr.GetStrategyHistory(ctx, req.StrategyID); err == nil {
			smc.History = p.toStrategySummaries(ctx, history)
		} else {
			dc.Warnings = append(dc.Warnings, "strategy history: "+err.Error())
		}
		if backtests, err := p.mgr.GetBacktestsForStrategy(ctx, req.StrategyID); err == nil {
			for _, b := range backtests {
				smc.Backtests = append(smc.Backtests, BacktestSummary{
					Version: b.Version, CAGR: b.CAGR, Sharpe: b.Sharpe, Drawdown: b.Drawdown, TotalTrades: b.TotalTrades,
				})
			}
		} else {
			dc.Warnings = append(dc.Warnings, "backtests: "+err.Error())
		}
		if reviews, err := p.mgr.GetReviewsForStrategy(ctx, req.StrategyID); err == nil {
			for _, r := range reviews {
				smc.Reviews = append(smc.Reviews, ReviewSummary{
					ReviewDate: r.ReviewDate, Summary: r.Summary, Weaknesses: r.Weaknesses, Confidence: r.Confidence,
				})
			}
		} else {
			dc.Warnings = append(dc.Warnings, "reviews: "+err.Error())
		}
	}

	dc.StrategyMemory = smc

	if lessons, err := p.mgr.GetLessons(ctx); err == nil {
		for _, l := range lessons {
			dc.Lessons = append(dc.Lessons, LessonSummary{Key: l.Key, Description: l.Description, TimesSeen: l.TimesSeen, Confidence: l.Confidence})
		}
	} else {
		dc.Warnings = append(dc.Warnings, "lessons: "+err.Error())
	}
	return nil
}

// toStrategySummaries attaches each record's market-context-at-generation-
// time snapshot (best-effort — older rows saved before this existed, or a
// lookup error, just leave Context nil rather than fail the whole list).
func (p *MemoryProvider) toStrategySummaries(ctx context.Context, records []memory.StrategyRecord) []StrategySummary {
	out := make([]StrategySummary, 0, len(records))
	for _, r := range records {
		s := StrategySummary{
			StrategyID: r.StrategyID, Version: r.Version, Name: r.Name, Status: r.Status,
			CreatedAt: r.CreatedAt.Format("2006-01-02"),
		}
		if snap, err := p.mgr.GetContextForStrategy(ctx, r.StrategyID, r.Version); err == nil {
			s.Context = &StrategyOutcomeContext{
				Regime: snap.MarketRegime, VIX: snap.VIX, FIINet: snap.FIINet, DIINet: snap.DIINet,
				Breadth: snap.BreadthADRatio, NewsSentiment: snap.NewsSentiment, Trend: snap.Trend,
			}
		}
		out = append(out, s)
	}
	return out
}
