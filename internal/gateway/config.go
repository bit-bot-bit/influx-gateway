package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Target struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Token  string `json:"token"`
	Weight int    `json:"weight"`
}

type Config struct {
	HTTPAddr        string
	GatewayToken    string
	RedisAddr       string
	UpstreamTimeout time.Duration
	FreshnessWindow time.Duration
	CacheTTL        time.Duration
	CacheTTLJitter  time.Duration
	CacheLockTTL    time.Duration
	CacheLockWait   time.Duration
	SlowQueryThreshold time.Duration
	QueryPreviewChars  int
	ForwardRetries  int
	ProbeInterval   time.Duration
	BreakerFailures int
	BreakerCooldown time.Duration
	Targets         []Target
}

func LoadConfig() (Config, error) {
	cfg := Config{
		HTTPAddr:        getEnv("HTTP_ADDR", ":8080"),
		GatewayToken:    os.Getenv("GATEWAY_TOKEN"),
		RedisAddr:       getEnv("REDIS_ADDR", "redis:6379"),
		UpstreamTimeout: time.Duration(getEnvInt("UPSTREAM_TIMEOUT_SECONDS", 60)) * time.Second,
		FreshnessWindow: time.Duration(getEnvInt("FRESHNESS_WINDOW_SECONDS", 15)) * time.Second,
		CacheTTL:        time.Duration(getEnvInt("CACHE_TTL_SECONDS", 60)) * time.Second,
		CacheTTLJitter:  time.Duration(getEnvInt("CACHE_TTL_JITTER_SECONDS", 5)) * time.Second,
		CacheLockTTL:    time.Duration(getEnvInt("CACHE_LOCK_TTL_SECONDS", 5)) * time.Second,
		CacheLockWait:   time.Duration(getEnvInt("CACHE_LOCK_WAIT_MILLISECONDS", 800)) * time.Millisecond,
		SlowQueryThreshold: time.Duration(getEnvInt("SLOW_QUERY_THRESHOLD_MILLISECONDS", 2000)) * time.Millisecond,
		QueryPreviewChars:  getEnvInt("QUERY_PREVIEW_CHARS", 240),
		ForwardRetries:  getEnvInt("FORWARD_RETRIES", 1),
		ProbeInterval:   time.Duration(getEnvInt("PROBE_INTERVAL_SECONDS", 10)) * time.Second,
		BreakerFailures: getEnvInt("BREAKER_FAILURES", 3),
		BreakerCooldown: time.Duration(getEnvInt("BREAKER_COOLDOWN_SECONDS", 20)) * time.Second,
	}

	targetsJSON := os.Getenv("TARGETS_JSON")
	if targetsJSON == "" {
		return cfg, fmt.Errorf("TARGETS_JSON is required")
	}
	if err := json.Unmarshal([]byte(targetsJSON), &cfg.Targets); err != nil {
		return cfg, fmt.Errorf("parse TARGETS_JSON: %w", err)
	}
	if len(cfg.Targets) == 0 {
		return cfg, fmt.Errorf("at least one target is required")
	}
	for i := range cfg.Targets {
		if cfg.Targets[i].Weight <= 0 {
			cfg.Targets[i].Weight = 1
		}
	}
	if cfg.GatewayToken == "" {
		return cfg, fmt.Errorf("GATEWAY_TOKEN is required")
	}
	if cfg.CacheTTLJitter < 0 {
		cfg.CacheTTLJitter = 0
	}
	if cfg.QueryPreviewChars < 0 {
		cfg.QueryPreviewChars = 0
	}

	return cfg, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}
