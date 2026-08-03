// Package contextbuilder answers one question: "given this task, what
// information does the AI need right now?" It is task-aware on purpose —
// it does not dump every available data point into every request. A
// "build_strategy" task and a "review_strategy" task load different
// sections, so prompts stay small and relevant instead of growing
// unbounded as more connectors/memory tables get added.
//
// This ties together the three services built so far: connectors/
// (external market data), memory/ (persistent learning), and engine/
// (portfolio state, feature store) — none of which know about each
// other. contextbuilder is the first thing that does, which is exactly
// why it isn't built until all three exist.
package contextbuilder

import "time"

type Task string

const (
	TaskBuildStrategy    Task = "build_strategy"
	TaskReviewStrategy   Task = "review_strategy"
	TaskOptimizeStrategy Task = "optimize_strategy"
)

// BuildRequest is the caller's ask. UserPreferences is supplied by the
// caller (e.g. the Strategy Lab wizard's answers) rather than loaded from
// a stored-preferences table — no such table exists yet (see BACKLOG.md,
// same decision memory/BACKLOG.md already made about User Preference
// Memory: no consumer/UI for persisted preferences exists yet).
type BuildRequest struct {
	Task            Task        `json:"task"`
	Symbol          string      `json:"symbol"`          // "NIFTYBEES" — this build's only supported instrument
	StrategyID      string      `json:"strategy_id"`     // required for review_strategy / optimize_strategy
	EngineBaseURL   string      `json:"engine_base_url"` // e.g. "http://localhost:9080"
	UserPreferences UserContext `json:"user_preferences"`
}

// DecisionContext is the one structured object a caller gets back —
// never every section populated at once, only what the requested Task
// actually needs (see taskSections in provider.go).
type DecisionContext struct {
	BuiltAt         time.Time             `json:"built_at"`
	Task            Task                  `json:"task"`
	User            UserContext           `json:"user,omitempty"`
	Market          MarketContext         `json:"market,omitempty"`
	GlobalMarket    GlobalMarketContext   `json:"global_market,omitempty"`
	Portfolio       PortfolioContext      `json:"portfolio,omitempty"`
	StrategyMemory  StrategyMemoryContext `json:"strategy_memory,omitempty"`
	PaperTrading    PaperTradingContext   `json:"paper_trading,omitempty"`
	Lessons         []LessonSummary       `json:"lessons,omitempty"`
	Regime          RegimeContext         `json:"regime,omitempty"`
	Recommendations RecommendationContext `json:"recommendations,omitempty"`

	// Warnings records partial-failure, not silent gaps — e.g. "NSE
	// breadth unreachable from this environment" still returns a usable
	// context, but the caller (and the AI) knows a section is missing
	// rather than assuming a zero value means "no breadth signal."
	Warnings []string `json:"warnings,omitempty"`
}

type UserContext struct {
	Style     string  `json:"style,omitempty"`     // swing | intraday
	Risk      string  `json:"risk,omitempty"`      // conservative | moderate | aggressive
	Objective string  `json:"objective,omitempty"` // beat_nifty | lowest_drawdown | grow_capital
	Capital   float64 `json:"capital,omitempty"`
}

// MarketContext prefers Feature Store data (engine/internal/featurestore)
// for technical fields when available — computing them independently
// here would be a fifth reimplementation of RSI/EMA math. Falls back to
// nothing (fields empty, not guessed) if the feature store has no row
// for today yet.
type MarketContext struct {
	Symbol              string  `json:"symbol"`
	Price               string  `json:"price,omitempty"`
	RSI14               string  `json:"rsi14,omitempty"`
	EMA20               string  `json:"ema20,omitempty"`
	EMA50               string  `json:"ema50,omitempty"`
	ADX14               string  `json:"adx14,omitempty"`
	Trend               string  `json:"trend,omitempty"` // up | down | flat, derived from EMA20 vs EMA50
	VIX                 string  `json:"vix,omitempty"`
	Breadth             string  `json:"breadth,omitempty"`        // advance/decline ratio
	NewsSentiment       string  `json:"news_sentiment,omitempty"` // bullish | neutral | bearish
	NewsScore           float64 `json:"news_score,omitempty"`
	FIINet              string  `json:"fii_net,omitempty"`
	DIINet              string  `json:"dii_net,omitempty"`
	Overnight           string  `json:"overnight,omitempty"` // overnight.SourceName that answered
	OvernightChangePct  string  `json:"overnight_change_pct,omitempty"`
	OvernightConfidence float64 `json:"overnight_confidence,omitempty"`
}

// GlobalMarketContext is a structured summary (not 15 raw ticker quotes)
// of what's happening in the rest of the world right now — same
// "disclosed rule, not a hidden score" principle RegimeContext uses. See
// connectors/global for the composite formula (GlobalMarketContext.Basis
// mirrors it verbatim).
type GlobalMarketContext struct {
	RiskMode     string        `json:"risk_mode,omitempty"` // risk_on | risk_off | neutral
	Confidence   float64       `json:"confidence,omitempty"`
	USEquities   string        `json:"us_equities,omitempty"`
	AsiaEquities string        `json:"asia_equities,omitempty"`
	Commodities  string        `json:"commodities,omitempty"`
	Currencies   string        `json:"currencies,omitempty"`
	OverallBias  string        `json:"overall_bias,omitempty"`
	Summary      string        `json:"summary,omitempty"`
	Session      GlobalSession `json:"session,omitempty"`
	Events       []GlobalEvent `json:"events,omitempty"`
	Basis        string        `json:"basis,omitempty"`
}

type GlobalSession struct {
	USOpen    bool `json:"us_open"`
	JapanOpen bool `json:"japan_open"`
	HKOpen    bool `json:"hk_open"`
	ChinaOpen bool `json:"china_open"`
	IndiaOpen bool `json:"india_open"`
}

// GlobalEvent is a raw headline, not an interpreted one — see
// connectors/global/events.go for why this package never fabricates an
// "Impact: Moderately Bullish" style score.
type GlobalEvent struct {
	Title       string `json:"title"`
	Source      string `json:"source"`
	PublishedAt string `json:"published_at,omitempty"`
	URL         string `json:"url,omitempty"`
}

type PortfolioContext struct {
	RunningStrategies int      `json:"running_strategies"`
	TotalCash         string   `json:"total_cash,omitempty"`
	TotalPnL          string   `json:"total_pnl,omitempty"`
	StrategyIDs       []string `json:"strategy_ids,omitempty"`
}

type StrategyMemoryContext struct {
	Successful []StrategySummary `json:"successful,omitempty"`
	Failed     []StrategySummary `json:"failed,omitempty"`
	// Populated only for review_strategy/optimize_strategy (StrategyID given).
	History   []StrategySummary `json:"history,omitempty"`
	Backtests []BacktestSummary `json:"backtests,omitempty"`
	Reviews   []ReviewSummary   `json:"reviews,omitempty"`
}

type StrategySummary struct {
	StrategyID string `json:"strategy_id"`
	Version    int    `json:"version"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`

	// Context is what the market actually looked like when THIS version
	// was generated (regime/VIX/FII-DII/sentiment/trend) — a snapshot
	// memory_update.py already saves per candidate but nothing previously
	// read back. Lets the planner reason about regime-conditioned
	// patterns ("this archetype's failures cluster around high VIX")
	// instead of treating an archetype's win-rate as regime-independent.
	// Omitted (not zero-valued) when no snapshot was ever recorded for
	// this version, so absence isn't confused with "no signal that day."
	Context *StrategyOutcomeContext `json:"context,omitempty"`
}

type StrategyOutcomeContext struct {
	Regime        string `json:"regime,omitempty"`
	VIX           string `json:"vix,omitempty"`
	FIINet        string `json:"fii_net,omitempty"`
	DIINet        string `json:"dii_net,omitempty"`
	Breadth       string `json:"breadth,omitempty"`
	NewsSentiment string `json:"news_sentiment,omitempty"`
	Trend         string `json:"trend,omitempty"`
}

type BacktestSummary struct {
	Version     int    `json:"version"`
	CAGR        string `json:"cagr"`
	Sharpe      string `json:"sharpe"`
	Drawdown    string `json:"drawdown"`
	TotalTrades int    `json:"total_trades"`
}

type ReviewSummary struct {
	ReviewDate string   `json:"review_date"`
	Summary    string   `json:"summary"`
	Weaknesses []string `json:"weaknesses,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
}

type PaperTradingContext struct {
	// Populated for build_strategy: a quick "what's already running"
	// cross-check. Populated for review_strategy/optimize_strategy: the
	// specific strategy's own recent trades and logs (deeper than the
	// few-line RunningStrategy.RecentLogs summary every strategy gets).
	Running      []RunningStrategy `json:"running,omitempty"`
	RecentTrades []TradeSummary    `json:"recent_trades,omitempty"`
	RecentLogs   []string          `json:"recent_logs,omitempty"`
}

type RunningStrategy struct {
	StrategyID string   `json:"strategy_id"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	PnL        string   `json:"pnl"`
	// RecentLogs is the last few real engine log lines for this strategy
	// (auto-pause/resume, evaluation-cutoff reached, errors) — so the AI
	// sees not just a status label but why it's in that state, per
	// strategy, every time it plans/reviews. Newest first.
	RecentLogs []string `json:"recent_logs,omitempty"`
}

type TradeSummary struct {
	Symbol      string `json:"symbol"`
	EntryTime   string `json:"entry_time"`
	ExitTime    string `json:"exit_time,omitempty"`
	PnL         string `json:"pnl,omitempty"`
	State       string `json:"state"`
	CloseReason string `json:"close_reason,omitempty"`
}

type LessonSummary struct {
	Key         string  `json:"key,omitempty"` // e.g. "momentum_swing" — matches memory.Lesson.Key verbatim, lets a caller (the agent's plan node) programmatically match a lesson to an archetype instead of parsing Description text
	Description string  `json:"description"`
	TimesSeen   int     `json:"times_seen"`
	Confidence  float64 `json:"confidence"` // success rate, 0-1
}

// RegimeContext is a plain rule-based label + transparent confidence, not
// an invented ML score — see regime_provider.go for the exact formula.
type RegimeContext struct {
	Regime     string  `json:"regime"` // bull | bear | sideways
	Confidence float64 `json:"confidence"`
	Basis      string  `json:"basis"` // the rule that produced this, shown not hidden
}

// RecommendationContext is rule-based from Regime + Lessons — see
// recommendation_provider.go. Never a fabricated "AI recommends X".
type RecommendationContext struct {
	RecommendedStyles []string `json:"recommended_styles"`
	Avoid             []string `json:"avoid"`
	Reasons           []string `json:"reasons"`
}
