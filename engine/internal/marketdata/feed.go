// Package marketdata builds candles from a broker tick feed and fans them
// out to every subscriber. One feed connection for the whole engine,
// symbol subscriptions deduped across all running strategies
// (ENGINE_SPEC Sec 0.2).
package marketdata

import "tradingengine/internal/models"

// Feed is any tick source: Angel One's live WebSocket, a historical
// replay, or the synthetic mock feed used for local testing.
type Feed interface {
	Subscribe(symbols []string) error
	Ticks() <-chan models.Tick
	Close() error
}
