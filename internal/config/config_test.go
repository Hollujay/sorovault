package config_test

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/config"
)

const testDatabaseURL = "postgres://user:pass@localhost:5432/sorovault" //nolint:gosec // fixture, not a real credential

// setEnv applies a set of variables for one test, clearing any others the
// package reads so the ambient environment cannot influence the result.
func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()

	for _, key := range []string{
		"RPC_URL", "NETWORK_PASSPHRASE", "DATABASE_URL", "DATABASE_URL_FILE",
		"HTTP_ADDR", "LOG_LEVEL", "LOG_FORMAT", "RPC_TIMEOUT", "REFRESH_INTERVAL",
		"MAX_CONCURRENT_REGISTRATIONS", "REFRESH_WEBHOOK_URL",
		"REFRESH_WEBHOOK_SECRET", "REFRESH_EVENTS", "REFRESH_EVENTS_INTERVAL",
		"SIGNING_KEY", "SIGNING_KEY_FILE", "SIGNING_RETIRED_KEYS",
		"CORS_ORIGINS", "RATE_LIMIT_PER_MINUTE", "RATE_LIMIT_BURST", "TRUST_X_FORWARDED_FOR",
		"DATABASE_MAX_CONNS", "DATABASE_MIN_CONNS",
	} {
		if v, ok := vars[key]; ok {
			t.Setenv(key, v)
			continue
		}
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	setEnv(t, map[string]string{"DATABASE_URL": testDatabaseURL})

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, config.DefaultRPCURL, cfg.RPCURL)
	assert.Equal(t, config.DefaultNetworkPassphrase, cfg.NetworkPassphrase)
	assert.Equal(t, config.DefaultHTTPAddr, cfg.HTTPAddr)
	assert.Equal(t, testDatabaseURL, cfg.DatabaseURL)
	assert.Equal(t, slog.LevelInfo, cfg.LogLevel)
	assert.Equal(t, config.LogFormatText, cfg.LogFormat)
	assert.Equal(t, config.DefaultRPCTimeout, cfg.RPCTimeout)
	assert.Zero(t, cfg.RefreshInterval, "automatic refresh is opt-in")
	assert.Zero(t, cfg.MaxConcurrentRegistrations,
		"the concurrency limit is left to the registry unless it is configured")
	assert.Zero(t, cfg.DatabaseMaxConns, "the pool size is left to pgx unless it is configured")
	assert.Zero(t, cfg.DatabaseMinConns, "the pool size is left to pgx unless it is configured")
	assert.Empty(t, cfg.CORSOrigins, "CORS is opt-in")
	assert.Equal(t, config.DefaultRateLimitPerMinute, cfg.RateLimitPerMinute)
	assert.Equal(t, config.DefaultRateLimitBurst, cfg.RateLimitBurst)
	assert.False(t, cfg.TrustXForwardedFor)
}

func TestLoadOverrides(t *testing.T) {
	setEnv(t, map[string]string{
		"DATABASE_URL":       testDatabaseURL,
		"RPC_URL":            "https://rpc.example.test",
		"NETWORK_PASSPHRASE": "Public Global Stellar Network ; September 2015",
		"HTTP_ADDR":          "127.0.0.1:9999",
		"LOG_LEVEL":          "debug",
		"RPC_TIMEOUT":        "90s",
		"REFRESH_INTERVAL":   "6h",
	})

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 6*time.Hour, cfg.RefreshInterval)

	assert.Equal(t, "https://rpc.example.test", cfg.RPCURL)
	assert.Equal(t, "Public Global Stellar Network ; September 2015", cfg.NetworkPassphrase)
	assert.Equal(t, "127.0.0.1:9999", cfg.HTTPAddr)
	assert.Equal(t, slog.LevelDebug, cfg.LogLevel)
	assert.Equal(t, 90*time.Second, cfg.RPCTimeout)
}

func TestMaxConcurrentRegistrations(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    int
		wantErr string
	}{
		{
			name: "unset leaves the registry's own default alone",
		},
		{
			name:  "a limit",
			value: "8",
			want:  8,
		},
		{
			name:  "the smallest limit",
			value: "1",
			want:  1,
		},
		{
			name:  "the largest limit",
			value: "1000",
			want:  1000,
		},
		{
			name:  "surrounding space is ignored",
			value: " 12 ",
			want:  12,
		},
		{
			name:    "zero is not a limit",
			value:   "0",
			wantErr: "must be between",
		},
		{
			name:    "a negative limit",
			value:   "-1",
			wantErr: "must be between",
		},
		{
			name:    "above the largest limit",
			value:   "1001",
			wantErr: "must be between",
		},
		{
			name:    "not a number",
			value:   "lots",
			wantErr: "MAX_CONCURRENT_REGISTRATIONS",
		},
		{
			name:    "not a whole number",
			value:   "2.5",
			wantErr: "MAX_CONCURRENT_REGISTRATIONS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := map[string]string{"DATABASE_URL": testDatabaseURL}
			if tt.value != "" {
				vars["MAX_CONCURRENT_REGISTRATIONS"] = tt.value
			}
			setEnv(t, vars)

			cfg, err := config.Load()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.MaxConcurrentRegistrations)
		})
	}
}

func TestDatabasePoolSize(t *testing.T) {
	tests := []struct {
		name    string
		max     string
		min     string
		wantMax int32
		wantMin int32
		wantErr string
	}{
		{
			name: "unset leaves pgx's own default alone",
		},
		{
			name:    "both set",
			max:     "20",
			min:     "5",
			wantMax: 20,
			wantMin: 5,
		},
		{
			name:    "maximum only",
			max:     "20",
			wantMax: 20,
		},
		{
			name:    "minimum only",
			min:     "5",
			wantMin: 5,
		},
		{
			name:    "minimum equal to the maximum",
			max:     "4",
			min:     "4",
			wantMax: 4,
			wantMin: 4,
		},
		{
			name:    "smallest maximum",
			max:     "1",
			wantMax: 1,
		},
		{
			name:    "largest maximum",
			max:     "10000",
			wantMax: 10000,
		},
		{
			name:    "zero minimum",
			min:     "0",
			wantMin: 0,
		},
		{
			name:    "surrounding space is ignored",
			max:     " 12 ",
			wantMax: 12,
		},
		{
			name:    "minimum above the maximum",
			max:     "2",
			min:     "3",
			wantErr: "cannot be greater than",
		},
		{
			name:    "zero maximum",
			max:     "0",
			wantErr: "must be between",
		},
		{
			name:    "negative maximum",
			max:     "-1",
			wantErr: "must be between",
		},
		{
			name:    "maximum above the limit",
			max:     "10001",
			wantErr: "must be between",
		},
		{
			name:    "minimum above the limit",
			min:     "10001",
			wantErr: "must be between",
		},
		{
			name:    "maximum is not a number",
			max:     "many",
			wantErr: "DATABASE_MAX_CONNS",
		},
		{
			name:    "minimum is not a number",
			min:     "few",
			wantErr: "DATABASE_MIN_CONNS",
		},
		{
			name:    "maximum is not a whole number",
			max:     "1.5",
			wantErr: "DATABASE_MAX_CONNS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := map[string]string{"DATABASE_URL": testDatabaseURL}
			if tt.max != "" {
				vars["DATABASE_MAX_CONNS"] = tt.max
			}
			if tt.min != "" {
				vars["DATABASE_MIN_CONNS"] = tt.min
			}
			setEnv(t, vars)

			cfg, err := config.Load()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantMax, cfg.DatabaseMaxConns)
			assert.Equal(t, tt.wantMin, cfg.DatabaseMinConns)
		})
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	setEnv(t, nil)

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

func TestRefreshWebhook(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		setEnv(t, map[string]string{"DATABASE_URL": testDatabaseURL})

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Empty(t, cfg.RefreshWebhookURL)
		assert.Empty(t, cfg.RefreshWebhookSecret)
	})

	t.Run("set", func(t *testing.T) {
		setEnv(t, map[string]string{
			"DATABASE_URL":           testDatabaseURL,
			"REFRESH_WEBHOOK_URL":    "https://hooks.example.test/sorovault",
			"REFRESH_WEBHOOK_SECRET": "s3cret",
		})

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, "https://hooks.example.test/sorovault", cfg.RefreshWebhookURL)
		assert.Equal(t, "s3cret", cfg.RefreshWebhookSecret)
	})

	t.Run("a URL that cannot be posted to is refused", func(t *testing.T) {
		for _, url := range []string{"not a url", "/relative", "ftp://example.test/hook"} {
			setEnv(t, map[string]string{
				"DATABASE_URL":        testDatabaseURL,
				"REFRESH_WEBHOOK_URL": url,
			})

			_, err := config.Load()
			require.Error(t, err, "url %q", url)
			assert.Contains(t, err.Error(), "REFRESH_WEBHOOK_URL")
		}
	})
}

func TestLogLevels(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"DEBUG", slog.LevelDebug},
		{"  Info  ", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			setEnv(t, map[string]string{"DATABASE_URL": testDatabaseURL, "LOG_LEVEL": tt.in})

			cfg, err := config.Load()
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.LogLevel)
		})
	}
}

func TestLogFormat(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"text", config.LogFormatText},
		{"json", config.LogFormatJSON},
		{"JSON", config.LogFormatJSON},
		{"  Text  ", config.LogFormatText},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			setEnv(t, map[string]string{"DATABASE_URL": testDatabaseURL, "LOG_FORMAT": tt.in})

			cfg, err := config.Load()
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.LogFormat)
		})
	}
}

// TestJSONLoggerEmitsParseableLines is the contract a log system depends on:
// every line is one JSON object with the message and its fields.
func TestJSONLoggerEmitsParseableLines(t *testing.T) {
	setEnv(t, map[string]string{"DATABASE_URL": testDatabaseURL, "LOG_FORMAT": "json"})

	cfg, err := config.Load()
	require.NoError(t, err)

	f, err := os.CreateTemp(t.TempDir(), "sorovault-log")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(f.Name()) })

	cfg.NewLogger(f).Info("sweep complete", "contracts", 3, "failed", 1)
	require.NoError(t, f.Close())

	body, err := os.ReadFile(f.Name())
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	require.Len(t, lines, 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got), "line was not JSON: %s", lines[0])
	assert.Equal(t, "sweep complete", got["msg"])
	assert.Equal(t, float64(3), got["contracts"])
	assert.Equal(t, float64(1), got["failed"])
}

func TestCORSOrigins(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    []string
		wantErr string
	}{
		{name: "unset means no CORS"},
		{name: "one origin", value: "https://app.example.test", want: []string{"https://app.example.test"}},
		{
			name:  "a list, with space and a trailing slash",
			value: " https://app.example.test/ , http://localhost:3000 ",
			want:  []string{"https://app.example.test", "http://localhost:3000"},
		},
		{name: "wildcard", value: "*", want: []string{"*"}},
		{
			name:    "wildcard cannot be mixed with a list",
			value:   "*, https://app.example.test",
			wantErr: "not both",
		},
		{
			name:    "wildcard after a list",
			value:   "https://app.example.test, *",
			wantErr: "not both",
		},
		{
			name:    "not an origin",
			value:   "not a url",
			wantErr: "CORS_ORIGINS",
		},
		{
			name:    "only http and https",
			value:   "ftp://app.example.test",
			wantErr: "http or https",
		},
		{
			name:    "a path is not an origin",
			value:   "https://app.example.test/api",
			wantErr: "must be an origin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := map[string]string{"DATABASE_URL": testDatabaseURL}
			if tt.value != "" {
				vars["CORS_ORIGINS"] = tt.value
			}
			setEnv(t, vars)

			cfg, err := config.Load()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.CORSOrigins)
		})
	}
}

func TestRateLimit(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		want    int
		wantErr string
	}{
		{name: "defaults", want: config.DefaultRateLimitPerMinute},
		{name: "a configured limit", vars: map[string]string{"RATE_LIMIT_PER_MINUTE": "120"}, want: 120},
		{name: "zero is not a limit", vars: map[string]string{"RATE_LIMIT_PER_MINUTE": "0"}, wantErr: "between"},
		{name: "not a number", vars: map[string]string{"RATE_LIMIT_PER_MINUTE": "lots"}, wantErr: "RATE_LIMIT_PER_MINUTE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := map[string]string{"DATABASE_URL": testDatabaseURL}
			for k, v := range tt.vars {
				vars[k] = v
			}
			setEnv(t, vars)

			cfg, err := config.Load()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.RateLimitPerMinute)
		})
	}

	t.Run("burst", func(t *testing.T) {
		setEnv(t, map[string]string{
			"DATABASE_URL":          testDatabaseURL,
			"RATE_LIMIT_BURST":      "5",
			"TRUST_X_FORWARDED_FOR": "true",
		})

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, 5, cfg.RateLimitBurst)
		assert.True(t, cfg.TrustXForwardedFor)
	})

	t.Run("invalid burst and boolean", func(t *testing.T) {
		for _, vars := range []map[string]string{
			{"RATE_LIMIT_BURST": "0"},
			{"RATE_LIMIT_BURST": "many"},
			{"TRUST_X_FORWARDED_FOR": "sometimes"},
		} {
			env := map[string]string{"DATABASE_URL": testDatabaseURL}
			for k, v := range vars {
				env[k] = v
			}
			setEnv(t, env)

			_, err := config.Load()
			require.Error(t, err)
		}
	})
}

func TestInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		wantErr string
	}{
		{
			name:    "unknown log level",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "LOG_LEVEL": "chatty"},
			wantErr: "unknown LOG_LEVEL",
		},
		{
			name:    "unknown log format",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "LOG_FORMAT": "xml"},
			wantErr: "unknown LOG_FORMAT",
		},
		{
			name:    "unparseable timeout",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "RPC_TIMEOUT": "soon"},
			wantErr: "RPC_TIMEOUT",
		},
		{
			name:    "non-positive timeout",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "RPC_TIMEOUT": "0s"},
			wantErr: "must be positive",
		},
		{
			name:    "negative timeout",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "RPC_TIMEOUT": "-5s"},
			wantErr: "must be positive",
		},
		{
			name:    "unparseable refresh interval",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "REFRESH_INTERVAL": "daily"},
			wantErr: "REFRESH_INTERVAL",
		},
		{
			name:    "refresh interval below minimum",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "REFRESH_INTERVAL": "5s"},
			wantErr: "at least",
		},
		{
			name:    "negative refresh interval",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "REFRESH_INTERVAL": "-1h"},
			wantErr: "at least",
		},
		{
			name:    "both database url sources set",
			vars:    map[string]string{"DATABASE_URL": testDatabaseURL, "DATABASE_URL_FILE": "/tmp/secret"},
			wantErr: "set either DATABASE_URL or DATABASE_URL_FILE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.vars)

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestLoadDatabaseURLFromFile(t *testing.T) {
	t.Run("reads contents", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "dburl")
		require.NoError(t, os.WriteFile(f, []byte(testDatabaseURL), 0o600))
		setEnv(t, map[string]string{"DATABASE_URL_FILE": f})

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, testDatabaseURL, cfg.DatabaseURL)
	})

	t.Run("trims trailing newline", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "dburl")
		require.NoError(t, os.WriteFile(f, []byte(testDatabaseURL+"\n"), 0o600))
		setEnv(t, map[string]string{"DATABASE_URL_FILE": f})

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, testDatabaseURL, cfg.DatabaseURL)
	})

	t.Run("trims carriage return", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "dburl")
		require.NoError(t, os.WriteFile(f, []byte(testDatabaseURL+"\r\n"), 0o600))
		setEnv(t, map[string]string{"DATABASE_URL_FILE": f})

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, testDatabaseURL, cfg.DatabaseURL)
	})

	t.Run("unreadable file names path", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "missing")
		setEnv(t, map[string]string{"DATABASE_URL_FILE": f})

		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DATABASE_URL_FILE")
		assert.NotContains(t, err.Error(), testDatabaseURL)
		assert.Contains(t, err.Error(), f)
	})
}

func TestLoadEvents(t *testing.T) {
	t.Run("off by default, with the default interval", func(t *testing.T) {
		setEnv(t, map[string]string{"DATABASE_URL": testDatabaseURL})
		cfg, err := config.Load()
		require.NoError(t, err)
		assert.False(t, cfg.RefreshEvents)
		assert.Equal(t, config.DefaultRefreshEventsInterval, cfg.RefreshEventsInterval)
	})

	t.Run("on, beside polling", func(t *testing.T) {
		setEnv(t, map[string]string{
			"DATABASE_URL": testDatabaseURL, "REFRESH_INTERVAL": "1h",
			"REFRESH_EVENTS": "true", "REFRESH_EVENTS_INTERVAL": "3s",
		})
		cfg, err := config.Load()
		require.NoError(t, err)
		assert.True(t, cfg.RefreshEvents)
		assert.Equal(t, 3*time.Second, cfg.RefreshEventsInterval)
	})

	for name, vars := range map[string]map[string]string{
		"events without the polling safety net": {"REFRESH_EVENTS": "true"},
		"not a boolean":                         {"REFRESH_EVENTS": "maybe", "REFRESH_INTERVAL": "1h"},
		"interval too short":                    {"REFRESH_EVENTS_INTERVAL": "100ms"},
		"interval not a duration":               {"REFRESH_EVENTS_INTERVAL": "soon"},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			vars["DATABASE_URL"] = testDatabaseURL
			setEnv(t, vars)
			_, err := config.Load()
			assert.Error(t, err)
		})
	}
}

func TestLoadSigning(t *testing.T) {
	seed := base64.StdEncoding.EncodeToString(make([]byte, 32))
	retired := base64.StdEncoding.EncodeToString(make([]byte, 32))

	t.Run("no key, no signer", func(t *testing.T) {
		setEnv(t, map[string]string{"DATABASE_URL": testDatabaseURL})
		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Nil(t, cfg.Signer)
	})

	t.Run("a key from the environment", func(t *testing.T) {
		setEnv(t, map[string]string{"DATABASE_URL": testDatabaseURL, "SIGNING_KEY": seed, "SIGNING_RETIRED_KEYS": retired + ", " + retired})
		cfg, err := config.Load()
		require.NoError(t, err)
		require.NotNil(t, cfg.Signer)
		assert.Len(t, cfg.Signer.KeyID(), 16)
		assert.Len(t, cfg.RetiredSigningKeys, 2)
	})

	t.Run("a key from a file, newline and all", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "key")
		require.NoError(t, os.WriteFile(path, []byte(seed+"\n"), 0o600))
		setEnv(t, map[string]string{"DATABASE_URL": testDatabaseURL, "SIGNING_KEY_FILE": path})
		cfg, err := config.Load()
		require.NoError(t, err)
		assert.NotNil(t, cfg.Signer)
	})

	for name, vars := range map[string]map[string]string{
		"both inline and file": {"SIGNING_KEY": seed, "SIGNING_KEY_FILE": "/nonexistent"},
		"missing file":         {"SIGNING_KEY_FILE": "/nonexistent/key"},
		"not base64":           {"SIGNING_KEY": "!!!"},
		"wrong length":         {"SIGNING_KEY": base64.StdEncoding.EncodeToString([]byte("short"))},
		"bad retired key":      {"SIGNING_KEY": seed, "SIGNING_RETIRED_KEYS": "AAAA"},
		"retired without key":  {"SIGNING_RETIRED_KEYS": retired},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			vars["DATABASE_URL"] = testDatabaseURL
			setEnv(t, vars)
			_, err := config.Load()
			require.Error(t, err)
			assert.NotContains(t, err.Error(), seed, "the key must not leak into an error")
		})
	}
}
