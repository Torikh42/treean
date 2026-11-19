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
	testEnvDatabaseURL         = "DATABASE_URL"
	testEnvJWTSecret           = "JWT_SECRET"
	testEnvWebhookSecret       = "WEBHOOK_SECRET"
	testEnvPort                = "PORT"
	testEnvAllowDemoHeaderAuth = "ALLOW_DEMO_HEADER_AUTH"
	testEnvExpirySweepInterval = "EXPIRY_SWEEP_INTERVAL"
	testEnvHTTPReadTimeout     = "HTTP_READ_TIMEOUT"
	testEnvHTTPWriteTimeout    = "HTTP_WRITE_TIMEOUT"
	testEnvDBMaxConns          = "DB_MAX_CONNS"
	testEnvDBMinConns          = "DB_MIN_CONNS"
	testEnvCORSAllowedOrigins  = "CORS_ALLOWED_ORIGINS"

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
	if cfg.DatabaseURL != testDatabaseURL {
		t.Fatalf("DatabaseURL = %q, want %q", cfg.DatabaseURL, testDatabaseURL)
	}
	if cfg.JWTSecret != testJWTSecret {
		t.Fatalf("JWTSecret = %q, want %q", cfg.JWTSecret, testJWTSecret)
	}
	if cfg.WebhookSecret != testWebhookSecret {
		t.Fatalf("WebhookSecret = %q, want %q", cfg.WebhookSecret, testWebhookSecret)
	}
	if cfg.Port != "8080" {
		t.Fatalf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.AllowDemoHeaderAuth {
		t.Fatal("expected AllowDemoHeaderAuth to be false")
	}
	if cfg.ExpirySweepInterval != 5*time.Second {
		t.Fatalf("ExpirySweepInterval = %v, want 5s", cfg.ExpirySweepInterval)
	}
	if cfg.HTTPReadTimeout != 15*time.Second {
		t.Fatalf("HTTPReadTimeout = %v, want 15s", cfg.HTTPReadTimeout)
	}
	if cfg.HTTPWriteTimeout != 30*time.Second {
		t.Fatalf("HTTPWriteTimeout = %v, want 30s", cfg.HTTPWriteTimeout)
	}
	if cfg.DBMaxConns != 50 {
		t.Fatalf("DBMaxConns = %d, want 50", cfg.DBMaxConns)
	}
	if cfg.DBMinConns != 5 {
		t.Fatalf("DBMinConns = %d, want 5", cfg.DBMinConns)
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
	for _, secret := range []string{cfg.DatabaseURL, cfg.JWTSecret, cfg.WebhookSecret} {
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

	if payload.Config.DatabaseURL != "[REDACTED]" {
		t.Fatalf("logged database_url = %q, want [REDACTED]", payload.Config.DatabaseURL)
	}
	if payload.Config.JWTSecret != "[REDACTED]" {
		t.Fatalf("logged jwt_secret = %q, want [REDACTED]", payload.Config.JWTSecret)
	}
	if payload.Config.WebhookSecret != "[REDACTED]" {
		t.Fatalf("logged webhook_secret = %q, want [REDACTED]", payload.Config.WebhookSecret)
	}
	if payload.Config.Port != "8080" || !payload.Config.AllowDemoHeaderAuth {
		t.Fatalf("unexpected non-secret fields in log: %+v", payload.Config)
	}
	if payload.Config.ExpirySweepInterval != 5*time.Second ||
		payload.Config.HTTPReadTimeout != 15*time.Second ||
		payload.Config.HTTPWriteTimeout != 30*time.Second ||
		payload.Config.DBMaxConns != 50 ||
		payload.Config.DBMinConns != 5 ||
		payload.Config.CORSAllowedOriginsCount != 2 {
		t.Fatalf("unexpected timing/pool/cors fields in log: %+v", payload.Config)
	}
}
