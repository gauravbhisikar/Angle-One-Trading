// Command engine is the AI Trading Engine's entrypoint: loads config,
// opens storage, wires the shared market-data/indicator pipeline, and
// serves the HTTP API. The AI never runs inside this process — it only
// ever produces DSL documents that get POSTed to /strategies.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/shopspring/decimal"

	"tradingengine/internal/api"
	"tradingengine/internal/config"
	"tradingengine/internal/dailyreview"
	"tradingengine/internal/evalcutoff"
	"tradingengine/internal/execution"
	"tradingengine/internal/featurestore"
	"tradingengine/internal/marketdata"
	"tradingengine/internal/marketsession"
	"tradingengine/internal/models"
	"tradingengine/internal/retention"
	"tradingengine/internal/scheduler"
	"tradingengine/internal/storage"
)

// buildCommit is set at build time via deploy.sh's -ldflags (git short
// commit hash) — exposed through GET /version and shown in the dashboard
// footer specifically so "is this actually the new build" is answerable
// by looking at the page, not by grepping the served HTML for a string
// that happens to be new/removed (real incident: a redeploy "succeeded"
// per its own health check while a stale process kept serving old code
// on the port — this makes that class of mistake visible immediately).
var buildCommit = "dev"

func main() {
	envPath := "../.env"
	if _, err := os.Stat(".env"); err == nil {
		envPath = ".env"
	}
	cfg, err := config.Load(envPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := storage.Open(cfg.SQLitePath)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer db.Close()

	strategies := storage.NewStrategyRepo(db)
	orders := storage.NewOrderRepo(db)
	trades := storage.NewTradeRepo(db)
	logs := storage.NewLogRepo(db)
	reviews := storage.NewReviewRepo(db)
	predicted := storage.NewPredictedMetricsRepo(db)

	// Default to the synthetic mock feed — safe for local dev/testing
	// without broker credentials or live market hours. Angel One's real
	// feed (internal/marketdata/angelone) is opt-in via USE_ANGEL_LIVE and
	// intentionally never auto-selected.
	feed := marketdata.NewMockFeed(time.Second)
	go feed.Run()
	if cfg.UseAngelLive {
		log.Println("USE_ANGEL_LIVE=true is set, but live Angel One wiring must be completed explicitly (credentials + instrument master) before use — continuing on the mock feed for safety")
	}

	startingCapital := decimal.NewFromFloat(cfg.StartingCapital)
	eng := scheduler.NewEngine(cfg.MaxConcurrentStrategies, feed, startingCapital, nil)

	// Track the latest close per symbol so the paper broker has a price
	// to fill against — fed by the same shared 1-minute stream everything
	// else uses, not a separate lookup pipeline.
	var priceMu sync.RWMutex
	latestPrice := map[string]decimal.Decimal{}
	eng.Pipeline.OnCandleClose(func(symbol string, tf models.Timeframe, candle models.Candle) {
		if tf != models.TF1m {
			return
		}
		priceMu.Lock()
		latestPrice[symbol] = candle.Close
		priceMu.Unlock()
	})
	priceLookup := func(symbol string) (decimal.Decimal, bool) {
		priceMu.RLock()
		defer priceMu.RUnlock()
		p, ok := latestPrice[symbol]
		return p, ok
	}

	broker := execution.NewPaperBroker(execution.FillBasic, priceLookup)

	featureStore, err := featurestore.Open(context.Background(), cfg.FeatureStorePath)
	if err != nil {
		log.Fatalf("featurestore: %v", err)
	}
	defer featureStore.Close()

	server := api.NewServer(api.Config{
		Engine:                 eng,
		Strategies:             strategies,
		Orders:                 orders,
		Trades:                 trades,
		Logs:                   logs,
		Reviews:                reviews,
		Predicted:              predicted,
		Broker:                 broker,
		FeatureStore:           featureStore,
		DefaultStartingCapital: startingCapital,
		PriceLookup:            priceLookup,
		BuildCommit:            buildCommit,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)

	// Auto-pauses every running strategy at market close (blocks new
	// entries, keeps managing whatever's already open) and auto-resumes
	// only the ones it paused once the market reopens — see
	// internal/marketsession/monitor.go. Every open/close/pause/resume is
	// logged (GET /system/logs, GET /strategies/{id}/logs) for later
	// analysis, not just a runtime state flip nobody can see afterward.
	sessionMonitor := marketsession.NewMonitor(eng, logs, time.Duration(cfg.MarketSessionPollSeconds)*time.Second)
	go sessionMonitor.Run(ctx)

	// Auto-pauses a strategy once it has run long enough to judge before
	// real money is committed: intraday after 30 days, swing after 7
	// completed trades. See internal/evalcutoff/monitor.go.
	cutoffMonitor := evalcutoff.NewMonitor(eng, strategies, trades, logs, time.Duration(cfg.MarketSessionPollSeconds)*time.Second)
	go cutoffMonitor.Run(ctx)

	// Reclaims disk space from strategies idle (no /run) for 90+ days:
	// deletes raw trades/orders/logs, keeps the strategy definition and
	// all analysis (predicted_metrics/ai_reviews/memory) so it can be
	// redeployed and re-run unchanged if ever needed again. See
	// internal/retention/monitor.go. Polls once a day — this doesn't need
	// the market-session cadence the other two monitors use.
	retentionMonitor := retention.NewMonitor(eng, strategies, trades, orders, logs, startingCapital, 24*time.Hour)
	go retentionMonitor.Run(ctx)

	// Captures one real day-by-day performance snapshot per strategy per
	// calendar day (upserted, so re-checking hourly just keeps today's row
	// current as more trades close) — this is what actually populates the
	// previously on-demand-only GET /strategies/{id}/daily-review/-reviews.
	// Saved rows survive retention's raw-trade purge (a separate table),
	// so day-by-day history outlives the raw trades it was computed from.
	dailyReviewMonitor := dailyreview.NewMonitor(strategies, trades, reviews, startingCapital, time.Hour)
	go dailyReviewMonitor.Run(ctx)

	httpServer := &http.Server{Addr: cfg.APIAddr, Handler: server.Handler()}
	go func() {
		log.Printf("engine listening on %s (max %d concurrent strategies)", cfg.APIAddr, cfg.MaxConcurrentStrategies)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("shutting down")
	cancel()
	feed.Close()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
