// Package config loads SoroVault's settings from the environment.
package config

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/soroworks/sorovault/internal/attest"
)

// Defaults applied when the corresponding variable is unset.
const (
	DefaultRPCURL            = "https://soroban-testnet.stellar.org"
	DefaultNetworkPassphrase = "Test SDF Network ; September 2015"
	DefaultHTTPAddr          = ":8080"
	DefaultLogLevel          = "info"
	DefaultRPCTimeout        = 30 * time.Second

	// DefaultRefreshEventsInterval is how often the event follower asks the
	// RPC for new upgrade events when REFRESH_EVENTS is on. The network
	// closes a ledger about every five seconds.
	DefaultRefreshEventsInterval = 10 * time.Second
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
	// HTTPAddr is the address the API and browse UI listen on.
	HTTPAddr string
	// LogLevel is one of debug, info, warn, error.
	LogLevel slog.Level
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
	// RefreshEvents makes `serve` follow the network's contract-upgrade
	// events and refresh a contract as soon as it is upgraded, instead of
	// waiting for the next polling sweep. Polling (REFRESH_INTERVAL) keeps
	// running as a safety net, so it must be set too.
	RefreshEvents bool
	// RefreshEventsInterval is how often new upgrade events are read.
	RefreshEventsInterval time.Duration

	// Signer, when set, signs every interface response. It is built from
	// SIGNING_KEY (or SIGNING_KEY_FILE): a base64 32-byte Ed25519 seed.
	// `sorovault keygen` makes one.
	Signer *attest.Signer
	// RetiredSigningKeys are earlier public keys (SIGNING_RETIRED_KEYS,
	// comma-separated base64) that stay published at GET /api/keys so
	// signatures made before a rotation can still be verified.
	RetiredSigningKeys []ed25519.PublicKey
}

// MinRefreshEventsInterval is the shortest REFRESH_EVENTS_INTERVAL accepted,
// so the follower cannot hammer the RPC faster than the network closes
// ledgers.
const MinRefreshEventsInterval = 2 * time.Second

// MinRefreshInterval is the shortest REFRESH_INTERVAL accepted. Below it a
// sweep over a large registry could still be running when the next is due,
// and a public RPC endpoint would see it as abuse.
const MinRefreshInterval = time.Minute

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
// (see DatabaseURLFromEnv), so it is the only one whose absence is an error.
func Load() (*Config, error) {
	databaseURL, err := DatabaseURLFromEnv()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		RPCURL:            envOr("RPC_URL", DefaultRPCURL),
		NetworkPassphrase: envOr("NETWORK_PASSPHRASE", DefaultNetworkPassphrase),
		DatabaseURL:       databaseURL,
		HTTPAddr:          envOr("HTTP_ADDR", DefaultHTTPAddr),
		RPCTimeout:        DefaultRPCTimeout,
	}

	level, err := parseLevel(envOr("LOG_LEVEL", DefaultLogLevel))
	if err != nil {
		return nil, err
	}
	cfg.LogLevel = level

	if raw := os.Getenv("RPC_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("config: RPC_TIMEOUT %q: %w", raw, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("config: RPC_TIMEOUT must be positive, got %s", d)
		}
		cfg.RPCTimeout = d
	}

	if raw := os.Getenv("REFRESH_INTERVAL"); raw != "" {
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

	limit, err := envInt("MAX_CONCURRENT_REGISTRATIONS",
		MinConcurrentRegistrationLimit, MaxConcurrentRegistrationLimit)
	if err != nil {
		return nil, err
	}
	cfg.MaxConcurrentRegistrations = limit

	webhookURL := strings.TrimSpace(os.Getenv("REFRESH_WEBHOOK_URL"))
	if webhookURL != "" {
		if err := checkWebhookURL(webhookURL); err != nil {
			return nil, err
		}
	}
	cfg.RefreshWebhookURL = webhookURL
	cfg.RefreshWebhookSecret = os.Getenv("REFRESH_WEBHOOK_SECRET")

	if err := loadEvents(cfg); err != nil {
		return nil, err
	}
	if err := loadSigning(cfg); err != nil {
		return nil, err
	}

	if cfg.DatabaseURL == "" {
		return nil, errors.New("config: DATABASE_URL or DATABASE_URL_FILE is required")
	}
	if cfg.RPCURL == "" {
		return nil, errors.New("config: RPC_URL must not be empty")
	}

	return cfg, nil
}

// loadEvents reads the event-driven refresh settings.
func loadEvents(cfg *Config) error {
	cfg.RefreshEventsInterval = DefaultRefreshEventsInterval

	if raw := strings.TrimSpace(os.Getenv("REFRESH_EVENTS")); raw != "" {
		on, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("config: REFRESH_EVENTS %q: want true or false", raw)
		}
		cfg.RefreshEvents = on
	}

	if raw := os.Getenv("REFRESH_EVENTS_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("config: REFRESH_EVENTS_INTERVAL %q: %w", raw, err)
		}
		if d < MinRefreshEventsInterval {
			return fmt.Errorf("config: REFRESH_EVENTS_INTERVAL must be at least %s, got %s",
				MinRefreshEventsInterval, d)
		}
		cfg.RefreshEventsInterval = d
	}

	if cfg.RefreshEvents && cfg.RefreshInterval <= 0 {
		return errors.New("config: REFRESH_EVENTS needs REFRESH_INTERVAL as well; " +
			"polling stays on as the safety net behind the events")
	}
	return nil
}

// loadSigning reads the interface-signing key and the retired public keys.
//
// Like the database URL, the signing key can come from a file
// (SIGNING_KEY_FILE) so it never has to enter the environment.
func loadSigning(cfg *Config) error {
	inline := strings.TrimSpace(os.Getenv("SIGNING_KEY"))
	path := os.Getenv("SIGNING_KEY_FILE")
	if inline != "" && path != "" {
		return errors.New("config: set either SIGNING_KEY or SIGNING_KEY_FILE, not both")
	}
	if path != "" {
		b, err := os.ReadFile(path) //nolint:gosec // path is an operator-provided secret file setting
		if err != nil {
			return fmt.Errorf("config: SIGNING_KEY_FILE: %w", err)
		}
		inline = strings.TrimSpace(string(b))
	}

	if inline != "" {
		// The error never includes the key.
		seed, err := attest.ParseSeed(inline)
		if err != nil {
			return errors.New("config: SIGNING_KEY is not valid base64")
		}
		signer, err := attest.NewSigner(seed)
		if err != nil {
			return fmt.Errorf("config: SIGNING_KEY: %w", err)
		}
		cfg.Signer = signer
	}

	for _, raw := range strings.Split(os.Getenv("SIGNING_RETIRED_KEYS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		pub, err := attest.ParsePublicKey(raw)
		if err != nil {
			return fmt.Errorf("config: SIGNING_RETIRED_KEYS: %w", err)
		}
		cfg.RetiredSigningKeys = append(cfg.RetiredSigningKeys, pub)
	}
	if len(cfg.RetiredSigningKeys) > 0 && cfg.Signer == nil {
		return errors.New("config: SIGNING_RETIRED_KEYS is set but there is no SIGNING_KEY")
	}
	return nil
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

// DatabaseURLFromEnv resolves the Postgres connection string.
//
// It is DATABASE_URL, or the contents of the file named by DATABASE_URL_FILE
// when that is set instead. Docker secrets, Swarm secrets and Kubernetes
// Secret volumes hand credentials over as files; a value that never enters the
// environment cannot leak through `docker inspect`, an error report or the
// environment of every child process the way an exported variable can.
//
// Setting both is a configuration mistake rather than a preference, so it is
// an error instead of a silent pick.
func DatabaseURLFromEnv() (string, error) {
	inline := os.Getenv("DATABASE_URL")
	path := os.Getenv("DATABASE_URL_FILE")

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
func (c *Config) NewLogger(w *os.File) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: c.LogLevel}))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envInt reads an integer from the environment.
//
// An unset or empty variable yields zero, which means "not configured". A
// value that is not a whole number, or that falls outside the accepted
// range, is an error: a limit of zero registrations would stop the registry
// working, and one of thousands would not be a limit.
func envInt(name string, min, max int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
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
