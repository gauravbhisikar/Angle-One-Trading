package api

import (
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/analytics"
	"tradingengine/internal/evalcutoff"
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
	// CurrentValue is cash + the live mark-to-market value of any open
	// position (see the openValue comment in handleListStrategies) — the
	// number to show as "what this strategy is worth right now," as
	// opposed to Cash, which understates it while a position is open.
	CurrentValue string `json:"current_value"`
	PnL          string `json:"pnl"`
	WinRate         float64  `json:"win_rate"`
	ProfitFactor    float64  `json:"profit_factor"`
	CompletedTrades int      `json:"completed_trades"`

	// Evaluation-period progress toward evalcutoff's auto-pause threshold
	// (30 days for intraday, 7 completed trades for swing) — so the
	// dashboard can show "how close is this to a real judgeable sample"
	// before the user commits real money. EvalLimit is the threshold
	// itself (days or trades depending on Type), so the UI never has to
	// hardcode evalcutoff's constants separately.
	EvalProgress      int  `json:"eval_progress"` // days running (intraday) or completed trades (swing)
	EvalLimit         int  `json:"eval_limit"`    // IntradayMaxAge in days, or SwingMaxExitTrades
	EvalCutoffReached bool `json:"eval_cutoff_reached"`

	// DataPurged distinguishes "never traded" from "traded, but raw
	// trades/orders/logs were reclaimed after 90 idle days" — see
	// internal/retention. CompletedTrades/WinRate/ProfitFactor read as 0
	// either way, which would otherwise look identical to a strategy that
	// simply never ran.
	DataPurged bool `json:"data_purged"`

	// IsExperiment marks a strategy deployed from a candidate that FAILED
	// quality gates (nodes/rank.py) — the user explicitly chose to paper-
	// trade it anyway despite the agent never recommending it. Driven by
	// the DSL's own tags field ("experiment"), not a separate table, so
	// it survives exactly like any other strategy (versioning, purge,
	// etc.) — just flagged for the dashboard to keep it out of combined
	// portfolio totals and its own section, never mixed with strategies
	// the agent actually recommended.
	IsExperiment bool `json:"is_experiment"`

	// LastCandleAt is the most recent wall-clock time ANY of this
	// strategy's subscribed indicators processed a real candle close —
	// "is it actually alive right now" reduced to one timestamp, shown
	// directly on the card. Empty string if it's never processed one yet
	// (just started, or genuinely stuck) — the dashboard renders that
	// distinctly from a real recent timestamp rather than guessing.
	LastCandleAt string `json:"last_candle_at,omitempty"`
}

func hasExperimentTag(tags []string) bool {
	for _, t := range tags {
		if t == "experiment" {
			return true
		}
	}
	return false
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
		lastCandleAt := ""
		if rt, ok := s.Engine.Get(id); ok {
			openPositions = rt.OpenPositionCount()
			if t, ok := rt.LastCandleAt(); ok {
				lastCandleAt = t.UTC().Format(time.RFC3339)
			}
		}

		// cash alone understates true equity while a position is open: buying
		// debits cash by the full cost basis immediately (portfolio.Ledger.
		// ApplyBuy), so cash-startingCapital looks like a loss roughly equal
		// to whatever was just spent, even if the holding is worth about the
		// same. openValue is that holding's live mark-to-market value (using
		// the same PriceLookup the paper broker fills against), added back so
		// currentValue/pnl reflect real live equity, not just the cash side
		// of it — this is also what portfolioSummary's combined card sums.
		cash := s.DefaultStartingCapital
		openValue := decimal.Zero
		if ledger, ok := s.peekLedger(id); ok {
			cash = ledger.Cash()
			if len(strat.Symbols) > 0 {
				if pos, held := ledger.Position(strat.Symbols[0]); held {
					price := pos.AvgPrice
					if s.PriceLookup != nil {
						if p, ok := s.PriceLookup(strat.Symbols[0]); ok {
							price = p
						}
					}
					openValue = price.Mul(decimal.NewFromInt(int64(pos.Quantity)))
				}
			}
		}
		currentValue := cash.Add(openValue)

		trades, _ := s.Trades.ListByStrategy(strat.StrategyID, strat.StrategyVersion)
		m := analytics.Compute(trades, s.DefaultStartingCapital, 0)
		totalPnL := currentValue.Sub(s.DefaultStartingCapital)
		completed := 0
		for _, t := range trades {
			if t.State == models.TradeClosed || t.State == models.TradeStopped || t.State == models.TradeTargetHit {
				completed++
			}
		}

		winRate, profitFactor, pnl := m.WinRate, m.ProfitFactor, totalPnL.String()
		currentValueStr := currentValue.String()

		// A purged strategy has no raw trades left to compute from — fall
		// back to the snapshot retention.Monitor took right before deleting
		// them, so the card still shows how it actually did instead of
		// zeros indistinguishable from "never traded."
		_, dataPurged, _ := s.Strategies.GetPurgedAt(id)
		if dataPurged {
			if final, ok, err := s.Strategies.GetFinalPerformance(id); err == nil && ok {
				winRate, profitFactor, completed, pnl = final.WinRate, final.ProfitFactor, final.CompletedTrades, final.PnL
				if finalPnL, err := decimal.NewFromString(final.PnL); err == nil {
					currentValueStr = s.DefaultStartingCapital.Add(finalPnL).String()
				}
			}
		}

		evalProgress, evalLimit, evalReached := 0, 0, false
		switch strat.Type {
		case models.StrategyIntraday:
			evalLimit = int(evalcutoff.IntradayMaxAge.Hours() / 24)
			if firstRun, ok, err := s.Strategies.GetFirstRunAt(id); err == nil && ok {
				evalProgress = int(time.Since(firstRun).Hours() / 24)
				evalReached = evalProgress >= evalLimit
			}
		case models.StrategySwing:
			evalLimit = evalcutoff.SwingMaxExitTrades
			evalProgress = completed
			evalReached = evalProgress >= evalLimit
		}

		out = append(out, StrategySummary{
			StrategyID: strat.StrategyID, StrategyName: strat.StrategyName, StrategyVersion: strat.StrategyVersion,
			Type: string(strat.Type), AssetType: string(strat.AssetType), Symbols: strat.Symbols,
			Timeframe: string(strat.Timeframe), Benchmark: strat.Benchmark,
			Status: status, OpenPositions: openPositions,
			StartingCapital: s.DefaultStartingCapital.String(), Cash: cash.String(), CurrentValue: currentValueStr, PnL: pnl,
			WinRate: winRate, ProfitFactor: profitFactor, CompletedTrades: completed,
			EvalProgress: evalProgress, EvalLimit: evalLimit, EvalCutoffReached: evalReached,
			DataPurged: dataPurged, IsExperiment: hasExperimentTag(strat.Tags),
			LastCandleAt: lastCandleAt,
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
