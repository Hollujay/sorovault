// Package config loads SoroVault's settings from the environment, with an
// optional YAML file as the base layer under it.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Defaults applied when the corresponding variable is unset.
const (
	DefaultRPCURL             = "https://soroban-testnet.stellar.org"
	DefaultNetworkPassphrase  = "Test SDF Network ; September 2015"
	DefaultHTTPAddr           = ":8080"
	DefaultLogLevel           = "info"
	DefaultLogFormat          = "text"
	DefaultRPCTimeout         = 30 * time.Second
	DefaultRateLimitPerMinute = 30
	DefaultRateLimitBurst     = 10
)

// Log format names accepted by LOG_FORMAT.
const (
	LogFormatText = "text"
	LogFormatJSON = "json"
)

// Bounds on the connection pool sizes accepted from the environment.
//
// The sizes themselves default to zero, which means "not configured": pgx
// picks its own default, the greater of 4 or the number of CPUs for the
// maximum and none for the minimum. That default is deliberately not spelled
// out here, because pgx derives it from the machine the process runs on and
// a copy frozen into this file would be right for one host and wrong for
// another.
const (
	// MinDatabaseMaxConns is the smallest maximum accepted: a pool of
	// zero connections could not serve a request.
	MinDatabaseMaxConns = 1
	// MaxDatabaseConns is the largest size accepted, for the maximum or
	// the minimum. A registry serving a busy API does not need a pool of
	// tens of thousands of connections, so a value this large is nearly
	// always a typo.
	MaxDatabaseConns = 10000
)

// Config is the resolved runtime configuration.
type Config struct {
	// RPCURL is the Stellar RPC endpoint contract code is read from.
	RPCURL string
	// NetworkPassphrase is the network operators expect RPCURL to serve.
	// It is a guard, not a source of truth: the registry reads the actual
	// passphrase from the endpoint and refuses to run on a mismatch, so a
	// stale RPC_URL cannot file mainnet contracts under "testnet".
	NetworkPassphrase string
	// DatabaseURL is the Postgres connection string for the registry.
	DatabaseURL string
	// MaxConcurrentRegistrations is how many registrations and refreshes
	// may run at once, across every client. Zero means unset, and the
	// registry's own default applies.
	MaxConcurrentRegistrations int
	// DatabaseMaxConns is the maximum number of connections the registry's
	// pool holds open. Zero means unset, and pgx's own default applies.
	DatabaseMaxConns int32
	// DatabaseMinConns is the number of connections the pool keeps open
	// between requests. Zero means unset, and pgx's own default applies.
	DatabaseMinConns int32
	// HTTPAddr is the address the API and browse UI listen on.
	HTTPAddr string
	// PublicURL is the externally reachable origin for link previews and sitemaps.
	PublicURL string
	// LogLevel is one of debug, info, warn, error.
	LogLevel slog.Level
	// LogFormat selects the handler: text for people, json for log systems
	// that query fields.
	LogFormat string
	// RPCTimeout bounds a single contract fetch.
	RPCTimeout time.Duration
	// RefreshInterval, when positive, makes `serve` re-check every
	// registered contract that has not been refreshed within it. Zero turns
	// automatic refresh off.
	RefreshInterval time.Duration
	// RefreshWebhookURL, when set, is the endpoint a refresh sweep posts
	// to whenever a contract's interface changes.
	RefreshWebhookURL string
	// RefreshWebhookSecret, when set, signs webhook payloads with
	// HMAC-SHA256 so a receiver can tell them from anyone else's.
	RefreshWebhookSecret string
	// CORSOrigins are the exact browser origins allowed to call the API.
	// "*" allows every origin for read-only requests only. Empty disables
	// CORS headers, as the server has always behaved.
	CORSOrigins []string
	// RateLimitPerMinute bounds registrations and refreshes per client.
	RateLimitPerMinute int
	// RateLimitBurst is how many of those requests may arrive at once.
	RateLimitBurst int
	// TrustXForwardedFor makes the rate limiter use the first address in
	// X-Forwarded-For as the client. It must only be set behind a proxy the
	// operator controls, because a client can otherwise forge its address.
	TrustXForwardedFor bool
	// ReadOnly makes serve refuse every write endpoint with 405, leaving
	// reads and the automatic refresher untouched, so an operator can
	// publish a registry for browsing without accepting registrations.
	ReadOnly bool
	// APITokens are the bearer credentials the write endpoints accept.
	// Put in rotation by configuring several. Empty keeps every write
	// public, as the API has always behaved; when any are set, a write
	// must present one as `Authorization: Bearer <token>`.
	//
	// Secrets live here in memory only: nothing logs, serialises or
	// echoes the configured values.
	APITokens []string
}

// MinAPITokenLength is the shortest API token the write auth accepts.
//
// The limit is a floor, not advice: a token short enough to guess is not
// meaningfully different from no token at all. Generate with something
// like `openssl rand -hex 32`.
const MinAPITokenLength = 16

// ParseAPITokens decodes an API_TOKENS value: comma-separated bearer
// tokens, whitespace around an entry ignored. Whitespace inside an entry
// is kept — it is a secret's bytes, and trimming secrets is how mismatches
// happen — so an entry with any embedded space is refused as a mistake.
// An empty or absent value yields no tokens, which keeps every write
// public.
func ParseAPITokens(raw string) ([]string, error) {
	tokens := []string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "":
			continue
		case strings.ContainsAny(entry, " \t\n"):
			return nil, fmt.Errorf("config: API_TOKENS contains whitespace; each token must be one string")
		}
		tokens = append(tokens, entry)
	}

	names := map[string]bool{}
	for _, token := range tokens {
		if len(token) < MinAPITokenLength {
			return nil, fmt.Errorf("config: API_TOKENS has a %d character token; use at least %d (e.g. `openssl rand -hex 32`)",
				len(token), MinAPITokenLength)
		}
		if names[token] {
			return nil, fmt.Errorf("config: API_TOKENS repeats a token; each token must be unique")
		}
		names[token] = true
	}
	return tokens, nil
}

// MinRefreshInterval is the shortest REFRESH_INTERVAL accepted. Below it a
// sweep over a large registry could still be running when the next is due,
// and a public RPC endpoint would see it as abuse.
const MinRefreshInterval = time.Minute

// source resolves a setting by name: the environment first, then the
// values a config file supplied. The environment keeps precedence
// everywhere, because an operator who exported a variable — usually in a
// container or a systemd unit — did so deliberately, while a file is
// edited and reviewed. Keys in the file are the env-var names,
// case-insensitively.
type source struct {
	file map[string]string
}

// Getenv is a value for name, or "" when neither layer sets it. An empty
// value in the file cannot shadow the environment away: absence and an
// empty variable have always meant the same thing, and they keep meaning
// it.
func (s *source) Getenv(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return s.file[strings.ToLower(name)]
}

// knownConfigKeys is every setting name Load reads from any source,
// lowercased. A config file must name exactly these: a key outside the
// set is rejected naming it, because a spelling slip that "works" by
// being ignored is how a file stops being reviewed at all.
var knownConfigKeys = map[string]bool{
	"database_url":                 true,
	"database_url_file":            true,
	"rpc_url":                      true,
	"network_passphrase":           true,
	"max_concurrent_registrations": true,
	"database_max_conns":           true,
	"database_min_conns":           true,
	"http_addr":                    true,
	"public_url":                   true,
	"log_level":                    true,
	"log_format":                   true,
	"rpc_timeout":                  true,
	"refresh_interval":             true,
	"refresh_webhook_url":          true,
	"refresh_webhook_secret":       true,
	"cors_origins":                 true,
	"rate_limit_per_minute":        true,
	"rate_limit_burst":             true,
	"trust_x_forwarded_for":        true,
	"read_only":                    true,
	"api_tokens":                   true,
}

// loadConfigFile parses a YAML config file into the flat map of
// env-name → value the overlaying source consults.
//
// The file must not become a second configuration language: keys are the
// env-var names, every value is a single scalar the existing env parsing
// will see as a string, and anything else — a nested map, a list, an
// unknown key — is an error naming the key, not a value silently ignored.
func loadConfigFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is an operator-provided setting
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}

	values := make(map[string]string, len(doc))
	for key, val := range doc {
		lower := strings.ToLower(strings.TrimSpace(key))
		if !knownConfigKeys[lower] {
			return nil, fmt.Errorf("config: %s: unknown key %q", path, key)
		}
		value, err := scalarValue(path, key, lower, val)
		if err != nil {
			return nil, err
		}
		if value != "" {
			values[lower] = value
		}
	}
	return values, nil
}

// scalarValue renders one YAML value as the string the env layer parses.
// YAML types the document for the user — booleans, integers, quoted
// strings — so it is translated here once, and the parsing code stays
// single-sourced:
func scalarValue(path, key, lower string, val any) (string, error) {
	switch typed := val.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	case bool, int, int64, uint64, float32, float64:
		return strings.TrimSpace(fmt.Sprintf("%v", typed)), nil
	case map[string]any:
		return "", fmt.Errorf("config: %s: %q holds a nested map; each setting is a single value", path, key)
	case []any:
		return "", fmt.Errorf("config: %s: %q holds a list; give a single value, or comma-separate it like %s",
			path, key, strings.ToUpper(lower))
	default:
		return "", fmt.Errorf("config: %s: %q is a %T, which is not a written setting", path, key, val)
	}
}

// Bounds on MAX_CONCURRENT_REGISTRATIONS, the number of registrations and
// refreshes allowed to run at once.
const (
	// MinConcurrentRegistrationLimit is the smallest limit accepted: one
	// registration at a time is a limit, none at all is not.
	MinConcurrentRegistrationLimit = 1
	// MaxConcurrentRegistrationLimit is the largest limit accepted. Every
	// registration holds a WASM module in memory while it decodes, so a
	// limit in the thousands would defeat the point of having one.
	MaxConcurrentRegistrationLimit = 1000
)

// Load reads configuration from the process environment.
//
// The database connection string is the only setting with no usable default
// (see config.DatabaseURL), so it is the only one whose absence is an error.
func Load() (*Config, error) {
	return LoadWith("")
}

// LoadWith loads configuration the way Load does, layered: when configPath
// names a YAML file, its values are the base layer and the environment
// overrides them key by key. An empty path is Load exactly.
//
// The file's keys are the env-var names, case-insensitively, and unknown
// keys are refused naming them (see loadConfigFile).
func LoadWith(configPath string) (*Config, error) {
	src := &source{}
	if configPath != "" {
		file, err := loadConfigFile(configPath)
		if err != nil {
			return nil, err
		}
		src.file = file
	}

	databaseURL, err := databaseURL(src)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		RPCURL:            envOr(src, "RPC_URL", DefaultRPCURL),
		NetworkPassphrase: envOr(src, "NETWORK_PASSPHRASE", DefaultNetworkPassphrase),
		DatabaseURL:       databaseURL,
		HTTPAddr:          envOr(src, "HTTP_ADDR", DefaultHTTPAddr),
		PublicURL:         strings.TrimRight(strings.TrimSpace(src.Getenv("PUBLIC_URL")), "/"),
		RPCTimeout:        DefaultRPCTimeout,
	}

	level, err := parseLevel(envOr(src, "LOG_LEVEL", DefaultLogLevel))
	if err != nil {
		return nil, err
	}
	cfg.LogLevel = level

	format, err := parseLogFormat(envOr(src, "LOG_FORMAT", DefaultLogFormat))
	if err != nil {
		return nil, err
	}
	cfg.LogFormat = format

	if raw := src.Getenv("RPC_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("config: RPC_TIMEOUT %q: %w", raw, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("config: RPC_TIMEOUT must be positive, got %s", d)
		}
		cfg.RPCTimeout = d
	}

	if raw := src.Getenv("REFRESH_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("config: REFRESH_INTERVAL %q: %w", raw, err)
		}
		if d != 0 && d < MinRefreshInterval {
			return nil, fmt.Errorf("config: REFRESH_INTERVAL must be 0 (off) or at least %s, got %s",
				MinRefreshInterval, d)
		}
		cfg.RefreshInterval = d
	}

	limit, err := envInt(src, "MAX_CONCURRENT_REGISTRATIONS",
		MinConcurrentRegistrationLimit, MaxConcurrentRegistrationLimit)
	if err != nil {
		return nil, err
	}
	cfg.MaxConcurrentRegistrations = limit

	webhookURL := strings.TrimSpace(src.Getenv("REFRESH_WEBHOOK_URL"))
	if webhookURL != "" {
		if err := checkWebhookURL(webhookURL); err != nil {
			return nil, err
		}
	}
	cfg.RefreshWebhookURL = webhookURL
	cfg.RefreshWebhookSecret = src.Getenv("REFRESH_WEBHOOK_SECRET")

	cfg.CORSOrigins, err = parseCORSOrigins(src.Getenv("CORS_ORIGINS"))
	if err != nil {
		return nil, err
	}

	cfg.RateLimitPerMinute, err = envIntDefault(src, "RATE_LIMIT_PER_MINUTE",
		DefaultRateLimitPerMinute, 1, 100000)
	if err != nil {
		return nil, err
	}
	cfg.RateLimitBurst, err = envIntDefault(src, "RATE_LIMIT_BURST",
		DefaultRateLimitBurst, 1, 100000)
	if err != nil {
		return nil, err
	}
	cfg.TrustXForwardedFor, err = envBool(src, "TRUST_X_FORWARDED_FOR")
	if err != nil {
		return nil, err
	}
	cfg.ReadOnly, err = envBool(src, "READ_ONLY")
	if err != nil {
		return nil, err
	}
	cfg.APITokens, err = ParseAPITokens(src.Getenv("API_TOKENS"))
	if err != nil {
		return nil, err
	}

	maxConns, err := envPoolSize(src, "DATABASE_MAX_CONNS", MinDatabaseMaxConns)
	if err != nil {
		return nil, err
	}
	minConns, err := envPoolSize(src, "DATABASE_MIN_CONNS", 0)
	if err != nil {
		return nil, err
	}
	// A pool cannot hold more connections than its maximum, so a minimum
	// above it is a configuration mistake rather than something to
	// reconcile at connect time.
	if maxConns > 0 && minConns > maxConns {
		return nil, fmt.Errorf("config: DATABASE_MIN_CONNS (%d) cannot be greater than DATABASE_MAX_CONNS (%d)",
			minConns, maxConns)
	}
	cfg.DatabaseMaxConns = maxConns
	cfg.DatabaseMinConns = minConns

	if cfg.PublicURL != "" {
		parsed, err := url.Parse(cfg.PublicURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			return nil, errors.New("config: PUBLIC_URL must be a public absolute HTTP(S) origin without a path, query, credentials or fragment")
		}
	}

	if cfg.DatabaseURL == "" {
		return nil, errors.New("config: DATABASE_URL or DATABASE_URL_FILE is required")
	}
	if cfg.RPCURL == "" {
		return nil, errors.New("config: RPC_URL must not be empty")
	}

	return cfg, nil
}

// checkWebhookURL rejects a webhook address that could never be delivered
// to. Catching it here means the mistake is reported when the process
// starts, rather than on the first contract that gets upgraded.
func checkWebhookURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("config: REFRESH_WEBHOOK_URL %q: %w", raw, err)
	}
	if !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("config: REFRESH_WEBHOOK_URL %q must be an absolute HTTP or HTTPS URL", raw)
	}
	return nil
}

// DatabaseURL resolves the Postgres connection string over the same
// layering LoadWith uses: the --config file may name either setting, and
// the environment decides when both layers speak for the same name.
// Commands that only need the connection string call it, so a file-layer
// DATABASE_URL works for them too.
func DatabaseURL(configPath string) (string, error) {
	src := &source{}
	if configPath != "" {
		file, err := loadConfigFile(configPath)
		if err != nil {
			return "", err
		}
		src.file = file
	}
	return databaseURL(src)
}

// databaseURL resolves the connection string over a config source: it is
// DATABASE_URL, or the contents of the file named by DATABASE_URL_FILE
// when that is set instead. Docker secrets, Swarm secrets and Kubernetes
// Secret volumes hand credentials over as files; a value that never enters
// the environment cannot leak through `docker inspect`, an error report or
// the environment of every child process the way an exported variable can.
//
// Setting both is a configuration mistake rather than a preference, so it
// is an error instead of a silent pick.
func databaseURL(src *source) (string, error) {
	inline := src.Getenv("DATABASE_URL")
	path := src.Getenv("DATABASE_URL_FILE")

	if inline != "" && path != "" {
		return "", errors.New("config: set either DATABASE_URL or DATABASE_URL_FILE, not both")
	}
	if path == "" {
		return inline, nil
	}

	// The read error names the path; the contents are a credential, so they
	// must not reach the error, a log line or a stack trace.
	b, err := os.ReadFile(path) //nolint:gosec // path is an operator-provided secret file setting
	if err != nil {
		return "", fmt.Errorf("config: DATABASE_URL_FILE: %w", err)
	}
	// Secret files usually end in a newline (echo adds one, and Kubernetes
	// keeps what it was given). A connection string with one on the end is
	// not the string the driver was given.
	return strings.TrimRight(string(b), "\r\n"), nil
}

// NewLogger builds the structured logger the rest of the process writes to.
//
// The format is validated by Load, so an unknown value here means a Config
// was built by hand; text is the safe default.
func (c *Config) NewLogger(w *os.File) *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == LogFormatJSON {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// parseCORSOrigins reads the comma-separated CORS_ORIGINS value.
//
// Origins are exact scheme://host[:port] values. "*" is accepted on its own,
// never mixed with a list, because the two mean different things: a list is
// an allowlist, while "*" deliberately opens only the read-only routes.
func parseCORSOrigins(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	seen := make(map[string]struct{})
	var origins []string
	star := false
	for _, item := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(item)
		if origin == "" {
			continue
		}
		if origin == "*" {
			star = true
			continue
		}

		parsed, err := url.Parse(origin)
		if err != nil {
			return nil, fmt.Errorf("config: CORS_ORIGINS %q: %w", origin, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fmt.Errorf("config: CORS_ORIGINS %q must use http or https", origin)
		}
		if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
			(parsed.Path != "" && parsed.Path != "/") {
			return nil, fmt.Errorf("config: CORS_ORIGINS %q must be an origin such as https://app.example.com", origin)
		}

		normalized := strings.TrimSuffix(origin, "/")
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		origins = append(origins, normalized)
	}

	if star {
		if len(origins) > 0 {
			return nil, errors.New("config: CORS_ORIGINS must be * or a list of origins, not both")
		}
		return []string{"*"}, nil
	}
	if len(origins) == 0 {
		return nil, errors.New("config: CORS_ORIGINS must not be empty")
	}
	return origins, nil
}

func envIntDefault(src *source, name string, fallback, min, max int) (int, error) {
	raw := strings.TrimSpace(src.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q: %w", name, raw, err)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("config: %s must be between %d and %d, got %d",
			name, min, max, n)
	}
	return n, nil
}

func envBool(src *source, name string) (bool, error) {
	raw := strings.TrimSpace(src.Getenv(name))
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("config: %s %q: %w", name, raw, err)
	}
	return v, nil
}

func envOr(src *source, key, fallback string) string {
	if v := src.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envInt reads an integer from the configuration source.
//
// An unset or empty variable yields zero, which means "not configured". A
// value that is not a whole number, or that falls outside the accepted
// range, is an error: a limit of zero registrations would stop the registry
// working, and one of thousands would not be a limit.
func envInt(src *source, name string, min, max int) (int, error) {
	raw := strings.TrimSpace(src.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q: %w", name, raw, err)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("config: %s must be between %d and %d, got %d",
			name, min, max, n)
	}
	return n, nil
}

// envPoolSize reads a connection pool size from the environment.
//
// An unset or empty variable yields zero, which means "leave pgx's default
// alone". A value that is not a number, or that falls outside the accepted
// range, is an error: a pool that is too small cannot serve a request, and
// one that is too large is nearly always a typo.
func envPoolSize(src *source, name string, min int32) (int32, error) {
	raw := strings.TrimSpace(src.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q: %w", name, raw, err)
	}
	if n < int64(min) || n > int64(MaxDatabaseConns) {
		return 0, fmt.Errorf("config: %s must be between %d and %d, got %d",
			name, min, MaxDatabaseConns, n)
	}
	return int32(n), nil
}

func parseLogFormat(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case LogFormatText:
		return LogFormatText, nil
	case LogFormatJSON:
		return LogFormatJSON, nil
	default:
		return "", fmt.Errorf("config: unknown LOG_FORMAT %q (want text or json)", s)
	}
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: unknown LOG_LEVEL %q (want debug, info, warn or error)", s)
	}
}
