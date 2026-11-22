package config

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	testEnvDatabaseURL              = "DATABASE_URL"
	testEnvJWTSecret                = "JWT_SECRET"
	testEnvWebhookSecret            = "WEBHOOK_SECRET"
	testEnvPort                     = "PORT"
	testEnvAllowDemoHeaderAuth      = "ALLOW_DEMO_HEADER_AUTH"
	testEnvExpirySweepInterval      = "EXPIRY_SWEEP_INTERVAL"
	testEnvHTTPReadTimeout          = "HTTP_READ_TIMEOUT"
	testEnvHTTPWriteTimeout         = "HTTP_WRITE_TIMEOUT"
	testEnvDBMaxConns               = "DB_MAX_CONNS"
	testEnvDBMinConns               = "DB_MIN_CONNS"
	testEnvCORSAllowedOrigins       = "CORS_ALLOWED_ORIGINS"
	testEnvRedisURL                 = "REDIS_URL"
	testEnvRedisTimeout             = "REDIS_TIMEOUT"
	testEnvEventCacheTTL            = "EVENT_CACHE_TTL"
	testEnvRateLimitEnabled         = "RATE_LIMIT_ENABLED"
	testEnvRateLimitRegisterMax     = "RATE_LIMIT_REGISTER_MAX"
	testEnvRateLimitLoginMax        = "RATE_LIMIT_LOGIN_MAX"
	testEnvRateLimitLoginIPMax      = "RATE_LIMIT_LOGIN_IP_MAX"
	testEnvRateLimitOrderUserMax    = "RATE_LIMIT_ORDER_USER_MAX"
	testEnvRateLimitOrderIPMax      = "RATE_LIMIT_ORDER_IP_MAX"
	testEnvRateLimitDemoMax         = "RATE_LIMIT_DEMO_FLASH_SALE_MAX"
	testEnvRateLimitRegisterHardMax = "RATE_LIMIT_REGISTER_HARD_MAX"
	testEnvRateLimitLoginIPHardMax  = "RATE_LIMIT_LOGIN_IP_HARD_MAX"
	testEnvPoWDifficulty            = "POW_DIFFICULTY"
	testEnvPoWSecret                = "POW_SECRET"

	testDatabaseURL   = "postgres://u:p@localhost:5432/db?sslmode=disable"
	testJWTSecret     = "jwt-secret"
	testWebhookSecret = "webhook-secret"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv(testEnvDatabaseURL, testDatabaseURL)
	t.Setenv(testEnvJWTSecret, testJWTSecret)
	t.Setenv(testEnvWebhookSecret, testWebhookSecret)
}

func clearOptionalEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		testEnvPort,
		testEnvAllowDemoHeaderAuth,
		testEnvExpirySweepInterval,
		testEnvHTTPReadTimeout,
		testEnvHTTPWriteTimeout,
		testEnvDBMaxConns,
		testEnvDBMinConns,
		testEnvCORSAllowedOrigins,
		testEnvRedisURL,
		testEnvRedisTimeout,
		testEnvEventCacheTTL,
		testEnvRateLimitEnabled,
		testEnvRateLimitRegisterMax,
		testEnvRateLimitLoginMax,
		testEnvRateLimitLoginIPMax,
		testEnvRateLimitOrderUserMax,
		testEnvRateLimitOrderIPMax,
		testEnvRateLimitDemoMax,
		testEnvRateLimitRegisterHardMax,
		testEnvRateLimitLoginIPHardMax,
		testEnvPoWDifficulty,
		testEnvPoWSecret,
	} {
		t.Setenv(key, "")
	}
}

func TestLoad_MissingOrBlankRequiredEnv(t *testing.T) {
	requiredKeys := []string{
		testEnvDatabaseURL,
		testEnvJWTSecret,
		testEnvWebhookSecret,
	}
	blankValues := []struct {
		name string
		val  string
	}{
		{name: "empty", val: ""},
		{name: "whitespace", val: "  \t\n  "},
	}

	for _, key := range requiredKeys {
		for _, bv := range blankValues {
			t.Run(key+"_"+bv.name, func(t *testing.T) {
				setRequiredEnv(t)
				clearOptionalEnv(t)
				t.Setenv(key, bv.val)

				_, err := Load()
				if err == nil {
					t.Fatalf("expected error when %s is %s", key, bv.name)
				}
				if !strings.Contains(err.Error(), key) {
					t.Fatalf("error %q does not mention %s", err.Error(), key)
				}
			})
		}
	}
}

func TestLoad_Defaults(t *testing.T) {
	setRequiredEnv(t)
	clearOptionalEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertDefaultCoreConfig(t, cfg)
	assertDefaultRedisAndRateLimit(t, cfg)
}

func assertDefaultCoreConfig(t *testing.T, cfg Config) {
	t.Helper()
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"DatabaseURL", cfg.DatabaseURL, testDatabaseURL},
		{"JWTSecret", cfg.JWTSecret, testJWTSecret},
		{"WebhookSecret", cfg.WebhookSecret, testWebhookSecret},
		{"Port", cfg.Port, "8080"},
		{"AllowDemoHeaderAuth", cfg.AllowDemoHeaderAuth, false},
		{"ExpirySweepInterval", cfg.ExpirySweepInterval, 5 * time.Second},
		{"HTTPReadTimeout", cfg.HTTPReadTimeout, 15 * time.Second},
		{"HTTPWriteTimeout", cfg.HTTPWriteTimeout, 30 * time.Second},
		{"DBMaxConns", cfg.DBMaxConns, int32(50)},
		{"DBMinConns", cfg.DBMinConns, int32(5)},
	}
	for _, tc := range checks {
		if tc.got != tc.want {
			t.Fatalf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	wantDefaultOrigins := []string{
		"http://localhost:5173",
		"http://127.0.0.1:5173",
		"http://localhost:3000",
		"http://127.0.0.1:3000",
	}
	if !slices.Equal(cfg.CORSAllowedOrigins, wantDefaultOrigins) {
		t.Fatalf("CORSAllowedOrigins = %v, want %v", cfg.CORSAllowedOrigins, wantDefaultOrigins)
	}
}

func assertDefaultRedisAndRateLimit(t *testing.T, cfg Config) {
	t.Helper()
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"RedisURL", cfg.RedisURL, ""},
		{"RedisTimeout", cfg.RedisTimeout, 500 * time.Millisecond},
		{"EventCacheTTL", cfg.EventCacheTTL, 3 * time.Second},
		{"RateLimitEnabled", cfg.RateLimitEnabled, true},
		{"RateLimitRegisterMax", cfg.RateLimitRegisterMax, 5},
		{"RateLimitLoginMax", cfg.RateLimitLoginMax, 10},
		{"RateLimitLoginIPMax", cfg.RateLimitLoginIPMax, 60},
		{"RateLimitOrderUserMax", cfg.RateLimitOrderUserMax, 20},
		{"RateLimitOrderIPMax", cfg.RateLimitOrderIPMax, 0},
		{"RateLimitRegisterHardMax", cfg.RateLimitRegisterHardMax, 100},
		{"RateLimitLoginIPHardMax", cfg.RateLimitLoginIPHardMax, 600},
		{"RateLimitDemoFlashSaleMax", cfg.RateLimitDemoFlashSaleMax, 5},
		{"PoWDifficulty", cfg.PoWDifficulty, 16},
		{"PoWSecret", cfg.PoWSecret, testJWTSecret},
	}
	for _, tc := range checks {
		if tc.got != tc.want {
			t.Fatalf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func assertFullOverrideConfig(t *testing.T, cfg Config) {
	t.Helper()
	checks := []struct {
		name string
		got  any
		want any
	}{
		{name: "DatabaseURL", got: cfg.DatabaseURL, want: testDatabaseURL},
		{name: "JWTSecret", got: cfg.JWTSecret, want: testJWTSecret},
		{name: "WebhookSecret", got: cfg.WebhookSecret, want: testWebhookSecret},
		{name: "Port", got: cfg.Port, want: "9090"},
		{name: "AllowDemoHeaderAuth", got: cfg.AllowDemoHeaderAuth, want: true},
		{name: "ExpirySweepInterval", got: cfg.ExpirySweepInterval, want: 2 * time.Second},
		{name: "HTTPReadTimeout", got: cfg.HTTPReadTimeout, want: 10 * time.Second},
		{name: "HTTPWriteTimeout", got: cfg.HTTPWriteTimeout, want: 20 * time.Second},
		{name: "DBMaxConns", got: cfg.DBMaxConns, want: int32(25)},
		{name: "DBMinConns", got: cfg.DBMinConns, want: int32(3)},
	}
	for _, tc := range checks {
		if tc.got != tc.want {
			t.Fatalf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	wantOrigins := []string{"https://app.example.com", "http://localhost:4173"}
	if !slices.Equal(cfg.CORSAllowedOrigins, wantOrigins) {
		t.Fatalf("CORSAllowedOrigins = %v, want %v", cfg.CORSAllowedOrigins, wantOrigins)
	}
}

func TestLoad_Overrides(t *testing.T) {
	t.Run("full overrides and trimmed values", func(t *testing.T) {
		setRequiredEnv(t)
		clearOptionalEnv(t)
		t.Setenv(testEnvDatabaseURL, "  "+testDatabaseURL+"  ")
		t.Setenv(testEnvJWTSecret, "  "+testJWTSecret+"  ")
		t.Setenv(testEnvWebhookSecret, "  "+testWebhookSecret+"  ")
		t.Setenv(testEnvPort, " 9090 ")
		t.Setenv(testEnvAllowDemoHeaderAuth, " true ")
		t.Setenv(testEnvExpirySweepInterval, " 2s ")
		t.Setenv(testEnvHTTPReadTimeout, " 10s ")
		t.Setenv(testEnvHTTPWriteTimeout, " 20s ")
		t.Setenv(testEnvDBMaxConns, " 25 ")
		t.Setenv(testEnvDBMinConns, " 3 ")
		t.Setenv(testEnvCORSAllowedOrigins, " https://app.example.com , , http://localhost:4173 ")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertFullOverrideConfig(t, cfg)
	})

	t.Run("boolean variants", func(t *testing.T) {
		boolCases := []struct {
			raw  string
			want bool
		}{
			{raw: "false", want: false},
			{raw: "1", want: true},
			{raw: "TRUE", want: true},
		}

		for _, tc := range boolCases {
			t.Run(tc.raw, func(t *testing.T) {
				setRequiredEnv(t)
				clearOptionalEnv(t)
				t.Setenv(testEnvAllowDemoHeaderAuth, tc.raw)

				cfg, err := Load()
				if err != nil {
					t.Fatalf("unexpected error for %q: %v", tc.raw, err)
				}
				if cfg.AllowDemoHeaderAuth != tc.want {
					t.Fatalf("AllowDemoHeaderAuth = %v, want %v", cfg.AllowDemoHeaderAuth, tc.want)
				}
			})
		}
	})

	t.Run("db min conns equals max conns", func(t *testing.T) {
		setRequiredEnv(t)
		clearOptionalEnv(t)
		t.Setenv(testEnvDBMaxConns, "10")
		t.Setenv(testEnvDBMinConns, "10")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.DBMinConns != 10 || cfg.DBMaxConns != 10 {
			t.Fatalf("got min=%d max=%d, want 10 and 10", cfg.DBMinConns, cfg.DBMaxConns)
		}
	})

	t.Run("cors allowed origins variants", func(t *testing.T) {
		cases := []struct {
			name string
			raw  string
			want []string
		}{
			{
				name: "wildcard",
				raw:  " * ",
				want: []string{"*"},
			},
			{
				name: "commas and whitespace only falls back to defaults",
				raw:  " ,  , ",
				want: []string{
					"http://localhost:5173",
					"http://127.0.0.1:5173",
					"http://localhost:3000",
					"http://127.0.0.1:3000",
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				setRequiredEnv(t)
				clearOptionalEnv(t)
				t.Setenv(testEnvCORSAllowedOrigins, tc.raw)

				cfg, err := Load()
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !slices.Equal(cfg.CORSAllowedOrigins, tc.want) {
					t.Fatalf("CORSAllowedOrigins = %v, want %v", cfg.CORSAllowedOrigins, tc.want)
				}
			})
		}
	})
}

func TestLoad_InvalidOptionalEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantKey string
	}{
		{name: "port zero", env: map[string]string{testEnvPort: "0"}, wantKey: testEnvPort},
		{name: "port above 65535", env: map[string]string{testEnvPort: "65536"}, wantKey: testEnvPort},
		{name: "port non-numeric", env: map[string]string{testEnvPort: "abc"}, wantKey: testEnvPort},
		{name: "invalid demo header bool", env: map[string]string{testEnvAllowDemoHeaderAuth: "invalid"}, wantKey: testEnvAllowDemoHeaderAuth},
		{name: "invalid sweep duration", env: map[string]string{testEnvExpirySweepInterval: "bad"}, wantKey: testEnvExpirySweepInterval},
		{name: "non-positive sweep duration", env: map[string]string{testEnvExpirySweepInterval: "0s"}, wantKey: testEnvExpirySweepInterval},
		{name: "negative read timeout", env: map[string]string{testEnvHTTPReadTimeout: "-1s"}, wantKey: testEnvHTTPReadTimeout},
		{name: "invalid write timeout", env: map[string]string{testEnvHTTPWriteTimeout: "bad"}, wantKey: testEnvHTTPWriteTimeout},
		{name: "non-positive db max conns", env: map[string]string{testEnvDBMaxConns: "0"}, wantKey: testEnvDBMaxConns},
		{name: "overflow db max conns", env: map[string]string{testEnvDBMaxConns: "2147483648"}, wantKey: testEnvDBMaxConns},
		{name: "invalid db min conns", env: map[string]string{testEnvDBMinConns: "abc"}, wantKey: testEnvDBMinConns},
		{name: "overflow db min conns", env: map[string]string{testEnvDBMinConns: "2147483648"}, wantKey: testEnvDBMinConns},
		{name: "min conns exceeds max conns", env: map[string]string{testEnvDBMinConns: "10", testEnvDBMaxConns: "5"}, wantKey: testEnvDBMinConns},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			clearOptionalEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			_, err := Load()
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Fatalf("error %q does not mention %s", err.Error(), tc.wantKey)
			}
		})
	}
}

func TestConfig_LogValueRedactsSecrets(t *testing.T) {
	cfg := Config{
		Port:                "8080",
		DatabaseURL:         "postgres://secret_user:super_secret_pass@db:5432/prod",
		JWTSecret:           "super-secret-jwt-key",
		WebhookSecret:       "super-secret-webhook-key",
		PoWSecret:           "super-secret-pow-key",
		RedisURL:            "redis://:super-secret-redis-pass@redis:6379/0",
		AllowDemoHeaderAuth: true,
		ExpirySweepInterval: 5 * time.Second,
		HTTPReadTimeout:     15 * time.Second,
		HTTPWriteTimeout:    30 * time.Second,
		DBMaxConns:          50,
		DBMinConns:          5,
		CORSAllowedOrigins:  []string{"http://localhost:5173", "http://localhost:3000"},
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("config loaded", slog.Any("config", cfg))

	rawOut := buf.String()
	for _, secret := range []string{cfg.DatabaseURL, cfg.JWTSecret, cfg.WebhookSecret, cfg.PoWSecret, cfg.RedisURL} {
		if strings.Contains(rawOut, secret) {
			t.Fatalf("log output leaked secret %q: %s", secret, rawOut)
		}
	}

	var payload struct {
		Config struct {
			Port                    string        `json:"port"`
			DatabaseURL             string        `json:"database_url"`
			JWTSecret               string        `json:"jwt_secret"`
			WebhookSecret           string        `json:"webhook_secret"`
			PoWSecret               string        `json:"pow_secret"`
			AllowDemoHeaderAuth     bool          `json:"allow_demo_header_auth"`
			ExpirySweepInterval     time.Duration `json:"expiry_sweep_interval"`
			HTTPReadTimeout         time.Duration `json:"http_read_timeout"`
			HTTPWriteTimeout        time.Duration `json:"http_write_timeout"`
			DBMaxConns              int32         `json:"db_max_conns"`
			DBMinConns              int32         `json:"db_min_conns"`
			CORSAllowedOriginsCount int           `json:"cors_allowed_origins_count"`
		} `json:"config"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("failed to unmarshal log output: %v (output=%s)", err, rawOut)
	}

	for _, check := range []struct {
		name string
		got  string
	}{
		{"database_url", payload.Config.DatabaseURL},
		{"jwt_secret", payload.Config.JWTSecret},
		{"webhook_secret", payload.Config.WebhookSecret},
		{"pow_secret", payload.Config.PoWSecret},
	} {
		if check.got != "[REDACTED]" {
			t.Fatalf("logged %s = %q, want [REDACTED]", check.name, check.got)
		}
	}
	if payload.Config.Port != "8080" || !payload.Config.AllowDemoHeaderAuth ||
		payload.Config.ExpirySweepInterval != 5*time.Second ||
		payload.Config.HTTPReadTimeout != 15*time.Second ||
		payload.Config.HTTPWriteTimeout != 30*time.Second ||
		payload.Config.DBMaxConns != 50 ||
		payload.Config.DBMinConns != 5 ||
		payload.Config.CORSAllowedOriginsCount != 2 {
		t.Fatalf("unexpected non-secret fields in log: %+v", payload.Config)
	}
}

func assertIntField(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", name, got, want)
	}
}

func TestLoad_CGNATRateLimitDefaultsAndOverrides(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		setRequiredEnv(t)
		clearOptionalEnv(t)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertIntField(t, "RateLimitOrderIPMax", cfg.RateLimitOrderIPMax, 0)
		assertIntField(t, "RateLimitRegisterHardMax", cfg.RateLimitRegisterHardMax, 100)
		assertIntField(t, "RateLimitLoginIPHardMax", cfg.RateLimitLoginIPHardMax, 600)
		assertIntField(t, "PoWDifficulty", cfg.PoWDifficulty, 16)
		if cfg.PoWSecret != testJWTSecret {
			t.Fatalf("PoWSecret = %q, want fallback to JWTSecret %q", cfg.PoWSecret, testJWTSecret)
		}
	})

	t.Run("explicit order IP zero and PoW overrides", func(t *testing.T) {
		setRequiredEnv(t)
		clearOptionalEnv(t)
		t.Setenv(testEnvRateLimitOrderIPMax, "0")
		t.Setenv(testEnvRateLimitRegisterHardMax, "150")
		t.Setenv(testEnvRateLimitLoginIPHardMax, "900")
		t.Setenv(testEnvPoWDifficulty, "20")
		t.Setenv(testEnvPoWSecret, " custom-pow-secret ")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertIntField(t, "RateLimitOrderIPMax", cfg.RateLimitOrderIPMax, 0)
		assertIntField(t, "RateLimitRegisterHardMax", cfg.RateLimitRegisterHardMax, 150)
		assertIntField(t, "RateLimitLoginIPHardMax", cfg.RateLimitLoginIPHardMax, 900)
		assertIntField(t, "PoWDifficulty", cfg.PoWDifficulty, 20)
		if cfg.PoWSecret != "custom-pow-secret" {
			t.Fatalf("PoWSecret = %q, want custom-pow-secret", cfg.PoWSecret)
		}

		var buf bytes.Buffer
		slog.New(slog.NewJSONHandler(&buf, nil)).Info("config loaded", slog.Any("config", cfg))
		if strings.Contains(buf.String(), "custom-pow-secret") || !strings.Contains(buf.String(), `"pow_secret":"[REDACTED]"`) {
			t.Fatalf("LogValue PoWSecret redaction mismatch: %s", buf.String())
		}
	})

	t.Run("blank POW_SECRET falls back to JWTSecret", func(t *testing.T) {
		setRequiredEnv(t)
		clearOptionalEnv(t)
		t.Setenv(testEnvPoWSecret, "   \t  ")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.PoWSecret != testJWTSecret {
			t.Fatalf("PoWSecret = %q, want fallback %q", cfg.PoWSecret, testJWTSecret)
		}
	})

	t.Run("PoW difficulty bounds", func(t *testing.T) {
		for _, valid := range []string{"8", "24"} {
			t.Run("valid_"+valid, func(t *testing.T) {
				setRequiredEnv(t)
				clearOptionalEnv(t)
				t.Setenv(testEnvPoWDifficulty, valid)

				if _, err := Load(); err != nil {
					t.Fatalf("expected difficulty %s to pass, got %v", valid, err)
				}
			})
		}

		for _, invalid := range []string{"7", "30"} {
			t.Run("invalid_"+invalid, func(t *testing.T) {
				setRequiredEnv(t)
				clearOptionalEnv(t)
				t.Setenv(testEnvPoWDifficulty, invalid)

				_, err := Load()
				if err == nil {
					t.Fatalf("expected error for POW_DIFFICULTY=%s", invalid)
				}
				if !strings.Contains(err.Error(), "must be between 8 and 24") {
					t.Fatalf("error %q does not contain 'must be between 8 and 24'", err.Error())
				}
			})
		}
	})

	t.Run("invalid rate limit bounds", func(t *testing.T) {
		cases := []struct {
			name    string
			env     map[string]string
			wantErr string
		}{
			{
				name:    "negative order IP max",
				env:     map[string]string{testEnvRateLimitOrderIPMax: "-1"},
				wantErr: testEnvRateLimitOrderIPMax,
			},
			{
				name:    "register hard max below soft max",
				env:     map[string]string{testEnvRateLimitRegisterMax: "10", testEnvRateLimitRegisterHardMax: "5"},
				wantErr: testEnvRateLimitRegisterHardMax,
			},
			{
				name:    "login IP hard max below soft max",
				env:     map[string]string{testEnvRateLimitLoginIPMax: "100", testEnvRateLimitLoginIPHardMax: "50"},
				wantErr: testEnvRateLimitLoginIPHardMax,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				setRequiredEnv(t)
				clearOptionalEnv(t)
				for k, v := range tc.env {
					t.Setenv(k, v)
				}

				_, err := Load()
				if err == nil {
					t.Fatalf("expected error for %s", tc.name)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %s", err.Error(), tc.wantErr)
				}
			})
		}
	})
}
