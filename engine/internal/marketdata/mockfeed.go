package marketdata

import (
	"math/rand"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/models"
)

// MockFeed is a synthetic tick generator for local testing/dev without
// broker credentials or live market hours — a bounded random walk per
// symbol, ticking at a configurable interval.
type MockFeed struct {
	interval time.Duration
	ticks    chan models.Tick
	stop     chan struct{}
	once     sync.Once

	mu      sync.Mutex
	prices  map[string]float64
	symbols []string
}

func NewMockFeed(interval time.Duration) *MockFeed {
	return &MockFeed{
		interval: interval,
		ticks:    make(chan models.Tick, 1024),
		stop:     make(chan struct{}),
		prices:   map[string]float64{},
	}
}

func (m *MockFeed) Subscribe(symbols []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range symbols {
		if _, ok := m.prices[s]; !ok {
			m.prices[s] = 1000 + rand.Float64()*2000
			m.symbols = append(m.symbols, s)
		}
	}
	return nil
}

func (m *MockFeed) Ticks() <-chan models.Tick { return m.ticks }

func (m *MockFeed) Close() error {
	m.once.Do(func() { close(m.stop) })
	return nil
}

// Run generates ticks until Close is called. Call in its own goroutine.
func (m *MockFeed) Run() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			close(m.ticks)
			return
		case now := <-ticker.C:
			m.mu.Lock()
			symbols := append([]string(nil), m.symbols...)
			m.mu.Unlock()
			for _, s := range symbols {
				m.mu.Lock()
				price := m.prices[s]
				price += (rand.Float64() - 0.5) * price * 0.002
				if price < 1 {
					price = 1
				}
				m.prices[s] = price
				m.mu.Unlock()

				select {
				case m.ticks <- models.Tick{
					Symbol:    s,
					Price:     decimal.NewFromFloat(price).Round(2),
					Volume:    int64(50 + rand.Intn(500)),
					Timestamp: now,
				}:
				default:
				}
			}
		}
	}
}
