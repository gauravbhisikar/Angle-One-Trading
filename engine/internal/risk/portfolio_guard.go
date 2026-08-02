package risk

import (
	"sync"

	"github.com/shopspring/decimal"
)

// SectorLookup maps a symbol to its sector. Returns "" for unmapped
// symbols, in which case sector exposure is skipped for that symbol
// (Instrument Master sector data is future work, ENGINE_SPEC Sec 8).
type SectorLookup func(symbol string) string

// PortfolioGuard enforces DSL_SPEC Sec 14 cross-strategy exposure caps —
// one instance shared by every strategy in the engine, since these limits
// are account-wide, not per-strategy (DSL_SPEC: "portfolio limits are the
// outer bound").
type PortfolioGuard struct {
	mu sync.Mutex

	totalCapital     decimal.Decimal
	deployedBySymbol map[string]decimal.Decimal
	deployedBySector map[string]decimal.Decimal
	sectorOf         SectorLookup
}

func NewPortfolioGuard(totalCapital decimal.Decimal, sectorOf SectorLookup) *PortfolioGuard {
	return &PortfolioGuard{
		totalCapital:     totalCapital,
		deployedBySymbol: map[string]decimal.Decimal{},
		deployedBySector: map[string]decimal.Decimal{},
		sectorOf:         sectorOf,
	}
}

// CanDeploy checks whether adding `amount` capital to `symbol` would
// breach either cap, without committing it.
func (g *PortfolioGuard) CanDeploy(symbol string, amount decimal.Decimal, maxSymbolExposurePct, maxSectorExposurePct float64) (bool, string) {
	if g.totalCapital.IsZero() {
		return true, ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if maxSymbolExposurePct > 0 {
		projected := g.deployedBySymbol[symbol].Add(amount)
		pct := projected.Div(g.totalCapital).Mul(decimal.NewFromInt(100))
		if pct.GreaterThan(decimal.NewFromFloat(maxSymbolExposurePct)) {
			return false, "max_symbol_exposure_exceeded"
		}
	}

	if maxSectorExposurePct > 0 && g.sectorOf != nil {
		if sector := g.sectorOf(symbol); sector != "" {
			projected := g.deployedBySector[sector].Add(amount)
			pct := projected.Div(g.totalCapital).Mul(decimal.NewFromInt(100))
			if pct.GreaterThan(decimal.NewFromFloat(maxSectorExposurePct)) {
				return false, "max_sector_exposure_exceeded"
			}
		}
	}

	return true, ""
}

func (g *PortfolioGuard) RecordDeploy(symbol string, amount decimal.Decimal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deployedBySymbol[symbol] = g.deployedBySymbol[symbol].Add(amount)
	if g.sectorOf != nil {
		if sector := g.sectorOf(symbol); sector != "" {
			g.deployedBySector[sector] = g.deployedBySector[sector].Add(amount)
		}
	}
}

func (g *PortfolioGuard) RecordRelease(symbol string, amount decimal.Decimal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deployedBySymbol[symbol] = g.deployedBySymbol[symbol].Sub(amount)
	if g.sectorOf != nil {
		if sector := g.sectorOf(symbol); sector != "" {
			g.deployedBySector[sector] = g.deployedBySector[sector].Sub(amount)
		}
	}
}
