package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/config"
)

// newConfigCmd inspects the effective configuration.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the effective configuration",
		Long: `Inspect the configuration sorovault actually runs with.

The effective configuration is the --config file layered under the
environment. Secrets are redacted on output: an inspection command must
never become the place a password or a token is retyped from.`,
	}
	cmd.AddCommand(newConfigShowCmd())
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration with secrets redacted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadWith(configPath)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			if asJSON {
				object := map[string]string{}
				for _, row := range effectiveRows(cfg) {
					object[row.key] = row.value
				}
				return json.NewEncoder(w).Encode(object)
			}

			for _, row := range effectiveRows(cfg) {
				if _, err := fmt.Fprintf(w, "%-28s %s\n", row.key, row.value); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the configuration as one JSON object")
	return cmd
}

// effectiveRow is one setting: the env-var name every source uses for it,
// and its post-merge value with secrets redacted.
type effectiveRow struct {
	key   string
	value string
}

// effectiveRows is the whole effective configuration in the order the
// README documents it, so a printed run and the docs read the same way.
// Secrets are redacted at the row, not logged anywhere first: the
// connection string keeps its host — an operator debugging a placement
// issue needs it — and loses its password; tokens and the webhook secret
// are not even shaped back out of what they became.
func effectiveRows(cfg *config.Config) []effectiveRow {
	connections := fmt.Sprintf("%d", cfg.MaxConcurrentRegistrations)

	var tokens string
	switch len(cfg.APITokens) {
	case 0:
		tokens = ""
	case 1:
		//nolint:gosec // a redaction notice naming none of the values
		tokens = "<redacted> (1 token configured)"
	default:
		//nolint:gosec // a redaction notice naming none of the values
		tokens = fmt.Sprintf("<redacted> (%d tokens configured)", len(cfg.APITokens))
	}

	secret := "<redacted>"
	if cfg.RefreshWebhookSecret == "" {
		secret = ""
	}

	return []effectiveRow{
		{"RPC_URL", cfg.RPCURL},
		{"NETWORK_PASSPHRASE", cfg.NetworkPassphrase},
		{"DATABASE_URL", redactDatabaseURL(cfg.DatabaseURL)},
		{"MAX_CONCURRENT_REGISTRATIONS", connections},
		{"DATABASE_MAX_CONNS", optionalNumber(cfg.DatabaseMaxConns, "pgx default")},
		{"DATABASE_MIN_CONNS", optionalNumber(cfg.DatabaseMinConns, "pgx default")},
		{"HTTP_ADDR", cfg.HTTPAddr},
		{"PUBLIC_URL", cfg.PublicURL},
		{"LOG_LEVEL", strings.ToLower(cfg.LogLevel.String())},
		{"LOG_FORMAT", cfg.LogFormat},
		{"RPC_TIMEOUT", cfg.RPCTimeout.String()},
		{"REFRESH_INTERVAL", optionalNumber(int(cfg.RefreshInterval), "0 (off)")},
		{"REFRESH_WEBHOOK_URL", cfg.RefreshWebhookURL},
		{"REFRESH_WEBHOOK_SECRET", secret},
		{"CORS_ORIGINS", strings.Join(cfg.CORSOrigins, ",")},
		{"RATE_LIMIT_PER_MINUTE", fmt.Sprintf("%d", cfg.RateLimitPerMinute)},
		{"RATE_LIMIT_BURST", fmt.Sprintf("%d", cfg.RateLimitBurst)},
		{"TRUST_X_FORWARDED_FOR", boolText(cfg.TrustXForwardedFor)},
		{"READ_ONLY", boolText(cfg.ReadOnly)},
		{"API_TOKENS", tokens},
	}
}

// optionalNumber prints a number, or the note that fits its zero value.
func optionalNumber[T int | int32 | int64](n T, whenZero string) string {
	if n == 0 {
		return whenZero
	}
	return fmt.Sprintf("%d", n)
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// redactDatabaseURL keeps everything an operator debugging a placement
// issue needs — scheme, host, port, user — and replaces the password with
// a marker. A string that does not parse is treated as all secret, not
// printed on the strength of a parser's tolerance failing.
func redactDatabaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<redacted>"
	}
	if parsed.User == nil {
		return raw
	}
	password, hasPassword := parsed.User.Password()
	if !hasPassword || password == "" {
		return raw
	}
	// The marker is wordy on purpose: url.UserPassword escapes "***" to
	// %2A… and the "redacted" text keeps the printed line legible.
	parsed.User = url.UserPassword(parsed.User.Username(), "redacted")
	return parsed.String()
}
