// Command server runs contextbuilder as an HTTP service — the boundary
// the Python LangGraph agent calls across (POST /context/build,
// POST /research/query), since it can't import Go packages directly.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"contextbuilder"
	"memory"
)

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	engineURL := getEnv("ENGINE_URL", "http://localhost:9080")
	memoryPath := getEnv("MEMORY_DB_PATH", "memory.db")
	addr := ":" + getEnv("CONTEXTBUILDER_PORT", "9090")

	ctx := context.Background()
	mgr, err := memory.Open(ctx, memoryPath)
	if err != nil {
		log.Fatalf("memory.Open: %v", err)
	}
	defer mgr.Close()

	builder := contextbuilder.NewBuilder(
		contextbuilder.NewMarketProvider(engineURL),
		contextbuilder.NewGlobalProvider(),
		contextbuilder.NewPortfolioProvider(engineURL),
		contextbuilder.NewMemoryProvider(mgr),
		contextbuilder.NewRegimeProvider(),
		contextbuilder.NewRecommendationProvider(),
	)

	server := contextbuilder.NewServer(builder, mgr)

	log.Printf("contextbuilder server listening on %s (engine=%s, memory=%s)", addr, engineURL, memoryPath)
	if err := http.ListenAndServe(addr, server.Handler()); err != nil {
		log.Fatalf("http: %v", err)
	}
}
