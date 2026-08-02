package contextbuilder

import (
	"context"

	"github.com/shopspring/decimal"
)

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
		running = append(running, RunningStrategy{StrategyID: s.StrategyID, Name: s.StrategyName, Status: s.Status, PnL: s.PnL})
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
	}
	dc.PaperTrading = ptc
	return nil
}
