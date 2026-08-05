package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	AngelAPIKey     string
	AngelClientID   string
	AngelPIN        string
	AngelTOTPSecret string

	SQLitePath       string
	FeatureStorePath string
	APIAddr          string

	MaxConcurrentStrategies  int
	StartingCapital          float64
	UseAngelLive             bool
	MarketSessionPollSeconds int

	// LoginUsername/LoginPassword/LoginKey gate the dashboard for any
	// non-loopback caller (api.Server.authMiddleware) — see LOGIN_USERNAME/
	// LOGIN_PASSWORD/LOGIN_KEY below. Any one left empty disables the gate.
	LoginUsername string
	LoginPassword string
	LoginKey      string
}

// Load reads a .env file (if present) into the process environment without
// overriding variables already set, then reads config from the environment.
func Load(envPath string) (*Config, error) {
	if err := loadDotEnv(envPath); err != nil {
		return nil, err
	}

	cfg := &Config{
		AngelAPIKey:      os.Getenv("ANGEL_API_KEY"),
		AngelClientID:    os.Getenv("ANGEL_CLIENT_ID"),
		AngelPIN:         os.Getenv("ANGEL_PIN"),
		AngelTOTPSecret:  os.Getenv("ANGEL_TOTP_SECRET"),
		SQLitePath:       getEnvDefault("SQLITE_PATH", "trading.db"),
		FeatureStorePath: getEnvDefault("FEATURE_STORE_PATH", "features.db"),
		APIAddr:          getEnvDefault("API_ADDR", ":9080"),
		LoginUsername:    os.Getenv("LOGIN_USERNAME"),
		LoginPassword:    os.Getenv("LOGIN_PASSWORD"),
		LoginKey:         os.Getenv("LOGIN_KEY"),
	}

	max, err := strconv.Atoi(getEnvDefault("MAX_CONCURRENT_STRATEGIES", "10"))
	if err != nil || max <= 0 {
		max = 10
	}
	cfg.MaxConcurrentStrategies = max

	capital, err := strconv.ParseFloat(getEnvDefault("STARTING_CAPITAL", "100000"), 64)
	if err != nil || capital <= 0 {
		capital = 100000
	}
	cfg.StartingCapital = capital

	cfg.UseAngelLive = getEnvDefault("USE_ANGEL_LIVE", "false") == "true"

	pollSecs, err := strconv.Atoi(getEnvDefault("MARKET_SESSION_POLL_SECONDS", "30"))
	if err != nil || pollSecs <= 0 {
		pollSecs = 30
	}
	cfg.MarketSessionPollSeconds = pollSecs

	return cfg, nil
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
	return scanner.Err()
}
