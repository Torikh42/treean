// Package config loads and validates runtime configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	redactedValue             = "[REDACTED]"
	defaultRedisTimeout       = 500 * time.Millisecond
	defaultEventCacheTTL      = 3 * time.Second
	defaultRateLimitRegMax    = 5
	defaultRateLimitLoginMax  = 10
	defaultRateLimitLoginIP   = 60
	defaultRateLimitOrderUser = 20
	defaultRateLimitOrderIP   = 300
	defaultRateLimitDemoMax   = 5
)

// Config holds validated runtime configuration for the API server and workers.
type Config struct {
	Port                      string
	DatabaseURL               string
	JWTSecret                 string
	WebhookSecret             string
	RedisURL                  string
	RedisTimeout              time.Duration
	EventCacheTTL             time.Duration
	RateLimitEnabled          bool
	RateLimitRegisterMax      int
	RateLimitLoginMax         int
	RateLimitLoginIPMax       int
	RateLimitOrderUserMax     int
	RateLimitOrderIPMax       int
	RateLimitDemoFlashSaleMax int
	AllowDemoHeaderAuth       bool
	ExpirySweepInterval       time.Duration
	HTTPReadTimeout           time.Duration
	HTTPWriteTimeout          time.Duration
	DBMaxConns                int32
	DBMinConns                int32
	CORSAllowedOrigins        []string
}

// LogValue implements slog.LogValuer and redacts sensitive fields from logs.
func (c Config) LogValue() slog.Value {
	redisState := "disabled"
	if c.RedisURL != "" {
		redisState = redactedValue
	}
	return slog.GroupValue(
		slog.String("port", c.Port),
		slog.String("database_url", redactedValue),
		slog.String("jwt_secret", redactedValue),
		slog.String("webhook_secret", redactedValue),
		slog.String("redis_url", redisState),
		slog.Duration("redis_timeout", c.RedisTimeout),
		slog.Duration("event_cache_ttl", c.EventCacheTTL),
		slog.Bool("rate_limit_enabled", c.RateLimitEnabled),
		slog.Bool("allow_demo_header_auth", c.AllowDemoHeaderAuth),
		slog.Duration("expiry_sweep_interval", c.ExpirySweepInterval),
		slog.Duration("http_read_timeout", c.HTTPReadTimeout),
		slog.Duration("http_write_timeout", c.HTTPWriteTimeout),
		slog.Int64("db_max_conns", int64(c.DBMaxConns)),
		slog.Int64("db_min_conns", int64(c.DBMinConns)),
		slog.Int("cors_allowed_origins_count", len(c.CORSAllowedOrigins)),
	)
}

// Load reads configuration from environment variables and validates required fields.
func Load() (Config, error) {
	dbURL, err := requireNonBlankEnv("DATABASE_URL")
	if err != nil {
		return Config{}, err
	}

	jwtSecret, err := requireNonBlankEnv("JWT_SECRET")
	if err != nil {
		return Config{}, err
	}

	webhookSecret, err := requireNonBlankEnv("WEBHOOK_SECRET")
	if err != nil {
		return Config{}, err
	}

	port, err := parsePort("PORT", "8080")
	if err != nil {
		return Config{}, err
	}

	allowDemoHeaderAuth, err := parseBool("ALLOW_DEMO_HEADER_AUTH", false)
	if err != nil {
		return Config{}, err
	}

	sweepInterval, err := parseDuration("EXPIRY_SWEEP_INTERVAL", 5*time.Second)
	if err != nil {
		return Config{}, err
	}

	readTimeout, err := parseDuration("HTTP_READ_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}

	writeTimeout, err := parseDuration("HTTP_WRITE_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}

	maxConns, err := parseInt32("DB_MAX_CONNS", 50)
	if err != nil {
		return Config{}, err
	}

	minConns, err := parseInt32("DB_MIN_CONNS", 5)
	if err != nil {
		return Config{}, err
	}
	if minConns > maxConns {
		return Config{}, fmt.Errorf("DB_MIN_CONNS (%d) cannot exceed DB_MAX_CONNS (%d)", minConns, maxConns)
	}

	corsAllowedOrigins := parseCSVList("CORS_ALLOWED_ORIGINS", defaultCORSAllowedOrigins())

	cfg := Config{
		Port:                port,
		DatabaseURL:         dbURL,
		JWTSecret:           jwtSecret,
		WebhookSecret:       webhookSecret,
		AllowDemoHeaderAuth: allowDemoHeaderAuth,
		ExpirySweepInterval: sweepInterval,
		HTTPReadTimeout:     readTimeout,
		HTTPWriteTimeout:    writeTimeout,
		DBMaxConns:          maxConns,
		DBMinConns:          minConns,
		CORSAllowedOrigins:  corsAllowedOrigins,
	}
	if err := loadRedisAndRateLimitConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loadRedisAndRateLimitConfig(cfg *Config) error {
	cfg.RedisURL = strings.TrimSpace(os.Getenv("REDIS_URL"))

	var err error
	if cfg.RedisTimeout, err = parseDuration("REDIS_TIMEOUT", defaultRedisTimeout); err != nil {
		return err
	}
	if cfg.EventCacheTTL, err = parseDuration("EVENT_CACHE_TTL", defaultEventCacheTTL); err != nil {
		return err
	}
	if cfg.RateLimitEnabled, err = parseBool("RATE_LIMIT_ENABLED", true); err != nil {
		return err
	}
	if cfg.RateLimitRegisterMax, err = parsePositiveInt("RATE_LIMIT_REGISTER_MAX", defaultRateLimitRegMax); err != nil {
		return err
	}
	if cfg.RateLimitLoginMax, err = parsePositiveInt("RATE_LIMIT_LOGIN_MAX", defaultRateLimitLoginMax); err != nil {
		return err
	}
	if cfg.RateLimitLoginIPMax, err = parsePositiveInt("RATE_LIMIT_LOGIN_IP_MAX", defaultRateLimitLoginIP); err != nil {
		return err
	}
	if cfg.RateLimitOrderUserMax, err = parsePositiveInt("RATE_LIMIT_ORDER_USER_MAX", defaultRateLimitOrderUser); err != nil {
		return err
	}
	if cfg.RateLimitOrderIPMax, err = parsePositiveInt("RATE_LIMIT_ORDER_IP_MAX", defaultRateLimitOrderIP); err != nil {
		return err
	}
	if cfg.RateLimitDemoFlashSaleMax, err = parsePositiveInt("RATE_LIMIT_DEMO_FLASH_SALE_MAX", defaultRateLimitDemoMax); err != nil {
		return err
	}
	return nil
}

func requireNonBlankEnv(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func parsePort(key, fallback string) (string, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return "", fmt.Errorf("invalid port for %s: %w", key, err)
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid port range for %s: %d (must be 1..65535)", key, port)
	}
	return strconv.Itoa(port), nil
}

func parseBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	val, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid boolean for %s: %w", key, err)
	}
	return val, nil
}

func parseDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid positive duration for %s: %q", key, raw)
	}
	return d, nil
}

func parseInt32(key string, fallback int32) (int32, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid integer for %s: %w", key, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("invalid positive integer for %s: %d", key, n)
	}
	return int32(n), nil
}

func parsePositiveInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid integer for %s: %w", key, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("invalid positive integer for %s: %d", key, n)
	}
	return n, nil
}

func defaultCORSAllowedOrigins() []string {
	return []string{
		"http://localhost:5173",
		"http://127.0.0.1:5173",
		"http://localhost:3000",
		"http://127.0.0.1:3000",
	}
}

func parseCSVList(key string, fallback []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
