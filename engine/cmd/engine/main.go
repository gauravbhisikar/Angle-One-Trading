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
	"tradingengine/internal/marketdata/angelone"
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

	// ctx is created here (rather than just before eng.Run, as before) so
	// buildFeed can hand it to the Angel One feed's long-lived
	// reconnect/heartbeat goroutines — those must only ever stop on real
	// engine shutdown, never on the short boot-time timeout buildFeed uses
	// internally to decide whether to fall back to the mock feed.
	ctx, cancel := context.WithCancel(context.Background())
	feed, feedMode := buildFeed(ctx, cfg)

	startingCapital := decimal.NewFromFloat(cfg.StartingCapital)
	eng := scheduler.NewEngine(cfg.MaxConcurrentStrategies, feed, startingCapital, nil)

	// Track the latest close per symbol so the paper broker has a price
	// to fill against — fed by the same shared 1-minute stream everything
	// else uses, not a separate lookup pipeline. Deliberately NOT tick-
	// level: fills against the last CLOSED 1-minute candle (never a
	// mid-candle tick) is the conservative, backtestable execution model
	// this engine commits to — a real, if modest, correctness choice, not
	// a limitation to route around for the display price below.
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

	// Separate, tick-level price for DISPLAY only (dashboard "live price"/
	// mark-to-market) — updates on every raw tick instead of waiting for a
	// full 1-minute candle to close, so the UI actually feels live instead
	// of stepping once a minute. Never used for fills; see priceLookup
	// above for why fills deliberately stay on the slower, conservative
	// price.
	var tickMu sync.RWMutex
	liveTickPrice := map[string]decimal.Decimal{}
	eng.Pipeline.SetRawTickObserver(func(t models.Tick) {
		tickMu.Lock()
		liveTickPrice[t.Symbol] = t.Price
		tickMu.Unlock()
	})
	displayPriceLookup := func(symbol string) (decimal.Decimal, bool) {
		tickMu.RLock()
		p, ok := liveTickPrice[symbol]
		tickMu.RUnlock()
		if ok {
			return p, true
		}
		return priceLookup(symbol) // fall back to the 1m-candle price until the first raw tick arrives
	}

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
		PriceLookup:            displayPriceLookup,
		BuildCommit:            buildCommit,
		FeedMode:               feedMode,
		LoginUsername:          cfg.LoginUsername,
		LoginPassword:          cfg.LoginPassword,
		LoginKey:               cfg.LoginKey,
	})

	go eng.Run(ctx)

	// Restores whatever was actually running/paused before this process
	// last stopped — scheduler.Engine's runtime map is pure in-memory, so
	// without this every redeploy would silently drop every strategy back
	// to not_started until someone noticed and clicked Run again. Runs
	// before the HTTP port is reachable (below), so nothing can race it.
	server.AutoResumeAll()

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

// buildFeed picks the engine's price source. USE_ANGEL_LIVE=true attempts a
// real login + instrument lookup + WebSocket connect against Angel One,
// bounded by a short boot timeout distinct from lifeCtx — a slow or down
// broker API must not delay the whole HTTP API from coming up. Any failure
// in that sequence (bad credentials, network unreachable, timeout) falls
// back to the synthetic mock feed with a loud log line, never a crash.
// lifeCtx is passed to the live feed's Connect for its long-lived
// reconnect/heartbeat goroutines, which must keep running for the engine's
// entire lifetime, not just until the boot timeout expires.
func buildFeed(lifeCtx context.Context, cfg *config.Config) (marketdata.Feed, string) {
	if !cfg.UseAngelLive {
		feed := marketdata.NewMockFeed(time.Second)
		go feed.Run()
		return feed, "mock"
	}

	bootCtx, bootCancel := context.WithTimeout(lifeCtx, 25*time.Second)
	defer bootCancel()

	client := angelone.NewClient(cfg.AngelAPIKey, cfg.AngelClientID, cfg.AngelPIN, cfg.AngelTOTPSecret)
	if err := client.Login(bootCtx); err != nil {
		log.Printf("USE_ANGEL_LIVE=true but Angel One login failed (%v) — falling back to mock feed", err)
		feed := marketdata.NewMockFeed(time.Second)
		go feed.Run()
		return feed, "mock"
	}

	httpClient := &http.Client{Timeout: 20 * time.Second}
	inst := angelone.ResolveNIFTYBEES(bootCtx, httpClient)

	wsFeed := angelone.NewWSFeed(client, map[string]angelone.Instrument{"NIFTYBEES": inst})
	if err := wsFeed.Connect(bootCtx, lifeCtx); err != nil {
		log.Printf("USE_ANGEL_LIVE=true but Angel One WebSocket connect failed (%v) — falling back to mock feed", err)
		feed := marketdata.NewMockFeed(time.Second)
		go feed.Run()
		return feed, "mock"
	}

	log.Printf("engine: using LIVE Angel One feed for NIFTYBEES (token=%s)", inst.Token)
	return wsFeed, "live"
}
