package contextbuilder

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"
)

// recentLogLines formats up to n newest-first engine log entries as short
// strings for the AI — enough to know WHY a strategy is in its current
// state (auto-paused: evaluation window reached, an error, etc.), not a
// full audit trail.
func recentLogLines(entries []engineLogEntry, n int) []string {
	if len(entries) > n {
		entries = entries[:n]
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, fmt.Sprintf("[%s] %s: %s", e.CreatedAt, e.Level, e.Message))
	}
	return out
}

// PortfolioProvider pulls current running-strategy state from the
// engine's own API — this is live process state, never duplicated
// locally (same reasoning as engineClient itself).
type PortfolioProvider struct {
	engine *engineClient
}

func NewPortfolioProvider(engineBaseURL string) *PortfolioProvider {
	return &PortfolioProvider{engine: newEngineClient(engineBaseURL)}
}

func (p *PortfolioProvider) Name() string { return "portfolio" }

func (p *PortfolioProvider) Load(ctx context.Context, req BuildRequest, dc *DecisionContext) error {
	strategies, err := p.engine.listStrategies(ctx)
	if err != nil {
		dc.Warnings = append(dc.Warnings, "portfolio: "+err.Error())
		return nil
	}

	pc := PortfolioContext{}
	totalCash, totalPnL := decimal.Zero, decimal.Zero
	var running []RunningStrategy
	for _, s := range strategies {
		pc.StrategyIDs = append(pc.StrategyIDs, s.StrategyID)
		if s.Status == "running" {
			pc.RunningStrategies++
		}
		if cash, err := decimal.NewFromString(s.Cash); err == nil {
			totalCash = totalCash.Add(cash)
		}
		if pnl, err := decimal.NewFromString(s.PnL); err == nil {
			totalPnL = totalPnL.Add(pnl)
		}
		var logLines []string
		if entries, err := p.engine.logs(ctx, s.StrategyID); err == nil {
			logLines = recentLogLines(entries, 3)
		} else {
			dc.Warnings = append(dc.Warnings, "logs for "+s.StrategyID+": "+err.Error())
		}
		running = append(running, RunningStrategy{
			StrategyID: s.StrategyID, Name: s.StrategyName, Status: s.Status, PnL: s.PnL, RecentLogs: logLines,
		})
	}
	pc.TotalCash = totalCash.String()
	pc.TotalPnL = totalPnL.String()
	dc.Portfolio = pc

	ptc := PaperTradingContext{Running: running}
	if req.StrategyID != "" {
		trades, err := p.engine.trades(ctx, req.StrategyID)
		if err != nil {
			dc.Warnings = append(dc.Warnings, "recent trades: "+err.Error())
		} else {
			limit := len(trades)
			if limit > 20 {
				limit = 20
			}
			for _, t := range trades[len(trades)-limit:] {
				ptc.RecentTrades = append(ptc.RecentTrades, TradeSummary{
					Symbol: t.Symbol, EntryTime: t.EntryTime, ExitTime: t.ExitTime,
					PnL: t.PnL, State: t.State, CloseReason: t.CloseReason,
				})
			}
		}
		if entries, err := p.engine.logs(ctx, req.StrategyID); err != nil {
			dc.Warnings = append(dc.Warnings, "recent logs: "+err.Error())
		} else {
			ptc.RecentLogs = recentLogLines(entries, 20)
		}
	}
	dc.PaperTrading = ptc
	return nil
}
