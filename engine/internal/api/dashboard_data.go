package api

import (
	"net/http"

	"tradingengine/internal/analytics"
	"tradingengine/internal/models"
)

type StrategySummary struct {
	StrategyID      string   `json:"strategy_id"`
	StrategyName    string   `json:"strategy_name"`
	StrategyVersion int      `json:"strategy_version"`
	Type            string   `json:"type"`
	AssetType       string   `json:"asset_type"`
	Symbols         []string `json:"symbols"`
	Timeframe       string   `json:"timeframe"`
	Benchmark       string   `json:"benchmark"`
	Status          string   `json:"status"` // running | paused | stopped | not_started
	OpenPositions   int      `json:"open_positions"`
	StartingCapital string   `json:"starting_capital"`
	Cash            string   `json:"cash"`
	PnL             string   `json:"pnl"`
	WinRate         float64  `json:"win_rate"`
	ProfitFactor    float64  `json:"profit_factor"`
	CompletedTrades int      `json:"completed_trades"`
}

// handleListStrategies is the dashboard's single overview call: every
// strategy the AI has ever created, its current run status, and enough
// live metrics to render a card without a second round trip per strategy.
func (s *Server) handleListStrategies(w http.ResponseWriter, r *http.Request) {
	ids, err := s.Strategies.ListStrategyIDs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]StrategySummary, 0, len(ids))
	for _, id := range ids {
		strat, _, err := s.Strategies.GetLatestVersion(id)
		if err != nil {
			continue
		}

		status := s.statusOf(id)
		openPositions := 0
		if rt, ok := s.Engine.Get(id); ok {
			openPositions = rt.OpenPositionCount()
		}

		cash := s.DefaultStartingCapital
		if ledger, ok := s.peekLedger(id); ok {
			cash = ledger.Cash()
		}

		trades, _ := s.Trades.ListByStrategy(strat.StrategyID, strat.StrategyVersion)
		m := analytics.Compute(trades, s.DefaultStartingCapital, 0)
		totalPnL := cash.Sub(s.DefaultStartingCapital)
		completed := 0
		for _, t := range trades {
			if t.State == models.TradeClosed || t.State == models.TradeStopped || t.State == models.TradeTargetHit {
				completed++
			}
		}

		out = append(out, StrategySummary{
			StrategyID: strat.StrategyID, StrategyName: strat.StrategyName, StrategyVersion: strat.StrategyVersion,
			Type: string(strat.Type), AssetType: string(strat.AssetType), Symbols: strat.Symbols,
			Timeframe: string(strat.Timeframe), Benchmark: strat.Benchmark,
			Status: status, OpenPositions: openPositions,
			StartingCapital: s.DefaultStartingCapital.String(), Cash: cash.String(), PnL: totalPnL.String(),
			WinRate: m.WinRate, ProfitFactor: m.ProfitFactor, CompletedTrades: completed,
		})
	}

	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleEquityCurve(w http.ResponseWriter, r *http.Request) {
	strat, _, err := s.Strategies.GetLatestVersion(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "strategy not found")
		return
	}
	trades, err := s.Trades.ListByStrategy(strat.StrategyID, strat.StrategyVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, analytics.EquityCurve(trades, s.DefaultStartingCapital))
}
