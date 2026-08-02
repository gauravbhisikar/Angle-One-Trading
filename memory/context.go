package memory

import (
	"context"
	"fmt"
	"time"
)

// ContextSnapshot is exactly what the AI saw when it built one strategy
// version — the piece that lets a future agent answer "why did this fail?"
// months later instead of re-deriving market conditions from scratch.
// Fields are strings (not typed numbers) deliberately: this is a record
// of what connectors reported at the time, not a place to recompute or
// validate their values.
type ContextSnapshot struct {
	StrategyID     string
	Version        int
	MarketRegime   string // bull | bear | sideways, whatever the caller's regime read was
	VIX            string
	FIINet         string
	DIINet         string
	BreadthADRatio string
	NewsSentiment  string // bullish | neutral | bearish
	NewsScore      string
	PCR            string
	RSI            string
	Trend          string
	VolumeRegime   string
	Notes          string
	CreatedAt      time.Time
}

func (m *Manager) SaveContext(ctx context.Context, c ContextSnapshot) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO strategy_context (strategy_id, version, market_regime, vix, fii_net, dii_net, breadth_ad_ratio,
		 news_sentiment, news_score, pcr, rsi, trend, volume_regime, notes, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(strategy_id, version) DO UPDATE SET
		   market_regime=excluded.market_regime, vix=excluded.vix, fii_net=excluded.fii_net, dii_net=excluded.dii_net,
		   breadth_ad_ratio=excluded.breadth_ad_ratio, news_sentiment=excluded.news_sentiment, news_score=excluded.news_score,
		   pcr=excluded.pcr, rsi=excluded.rsi, trend=excluded.trend, volume_regime=excluded.volume_regime, notes=excluded.notes`,
		c.StrategyID, c.Version, c.MarketRegime, c.VIX, c.FIINet, c.DIINet, c.BreadthADRatio,
		c.NewsSentiment, c.NewsScore, c.PCR, c.RSI, c.Trend, c.VolumeRegime, c.Notes, c.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("memory: save context: %w", err)
	}
	return nil
}

func (m *Manager) GetContextForStrategy(ctx context.Context, strategyID string, version int) (ContextSnapshot, error) {
	var c ContextSnapshot
	var createdAt string
	err := m.db.QueryRowContext(ctx,
		`SELECT strategy_id, version, market_regime, vix, fii_net, dii_net, breadth_ad_ratio,
		 news_sentiment, news_score, pcr, rsi, trend, volume_regime, notes, created_at
		 FROM strategy_context WHERE strategy_id = ? AND version = ?`,
		strategyID, version,
	).Scan(&c.StrategyID, &c.Version, &c.MarketRegime, &c.VIX, &c.FIINet, &c.DIINet, &c.BreadthADRatio,
		&c.NewsSentiment, &c.NewsScore, &c.PCR, &c.RSI, &c.Trend, &c.VolumeRegime, &c.Notes, &createdAt)
	if err != nil {
		return ContextSnapshot{}, err
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return c, nil
}
