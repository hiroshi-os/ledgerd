package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL      string
	Addr             string
	IdempotencyTTL   time.Duration
	ProcessorSeed    int64
	BootstrapAPIKey  string
	BootstrapKeyName string
}

func FromEnv() (Config, error) {
	cfg := Config{
		DatabaseURL:      getenv("DATABASE_URL", "postgres://ledgerd:ledgerd@localhost:5432/ledgerd?sslmode=disable"),
		Addr:             getenv("LEDGERD_ADDR", ":8080"),
		IdempotencyTTL:   24 * time.Hour,
		ProcessorSeed:    1,
		BootstrapAPIKey:  getenv("LEDGERD_BOOTSTRAP_API_KEY", "sk_test_ledgerd"),
		BootstrapKeyName: "bootstrap",
	}
	if v := os.Getenv("LEDGERD_IDEMPOTENCY_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("LEDGERD_IDEMPOTENCY_TTL: %w", err)
		}
		cfg.IdempotencyTTL = d
	}
	if cfg.IdempotencyTTL <= 0 {
		return Config{}, fmt.Errorf("idempotency TTL must be positive")
	}
	if v := os.Getenv("LEDGERD_PROCESSOR_SEED"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("LEDGERD_PROCESSOR_SEED: %w", err)
		}
		cfg.ProcessorSeed = n
	}
	if cfg.BootstrapAPIKey == "" {
		return Config{}, fmt.Errorf("LEDGERD_BOOTSTRAP_API_KEY is empty")
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
