package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/config"
)

// writeYAMLConfigFile is a YAML file the tests load through --config.
func writeYAMLConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sorovault.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// TestConfigShowRedactsSecrets pins the promise the command makes: the
// output is safe to paste into an issue thread. Anything a secret-shaped
// value reaches as must have lost the secret itself first.
func TestConfigShowRedactsSecrets(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{ //nolint:gosec // test fixtures; every secret below is printed only through redaction
		RPCURL:               "https://soroban-testnet.stellar.org",
		NetworkPassphrase:    "Test SDF Network ; September 2015",
		DatabaseURL:          "postgres://vault:s3cret-here@db.internal:5432/sorovault",
		HTTPAddr:             ":8080",
		LogFormat:            config.LogFormatJSON,
		RPCTimeout:           30 * time.Second,
		RefreshWebhookSecret: "whsec-abcdefg-hmac-material",
		APITokens:            []string{"0123456789abcdef-one", "0987654321fedcba-two"},
		RateLimitPerMinute:   30,
		RateLimitBurst:       10,
	}

	rows := effectiveRows(cfg)
	byName := map[string]string{}
	for _, row := range rows {
		byName[row.key] = row.value
	}

	// The password is gone; the placement facts stay.
	assert.Equal(t, "postgres://vault:redacted@db.internal:5432/sorovault", byName["DATABASE_URL"])
	assert.NotContains(t, byName["DATABASE_URL"], "s3cret")

	// Tokens are not even shaped back out.
	assert.Equal(t, "<redacted> (2 tokens configured)", byName["API_TOKENS"])
	assert.Equal(t, "<redacted>", byName["REFRESH_WEBHOOK_SECRET"])

	// Everything the output claims about itself stays true.
	assert.Equal(t, "https://soroban-testnet.stellar.org", byName["RPC_URL"])
	assert.Equal(t, ":8080", byName["HTTP_ADDR"])
	assert.Equal(t, "json", byName["LOG_FORMAT"])
	assert.Equal(t, "false", byName["READ_ONLY"])

	// Secret material must not sneak through any other row either.
	out := printableRows(cfg)
	assert.NotContains(t, out, "s3cret")
	assert.NotContains(t, out, "whsec-abcdefg")
	assert.NotContains(t, out, "0123456789abcdef")

	// No user info, no credential to protect: the tolerant url parser
	// accepts plenty of odd shapes, and none of them carry a secret.
	assert.Equal(t, "postgres ola db.internal 5432 sorovault", redactDatabaseURL("postgres ola db.internal 5432 sorovault"))
}

// TestConfigShowJSONLoadedConfig runs the command end to end over a
// configuration file, proving that --config reaches the row printer and
// that --json returns parseable JSON.
func TestConfigShowJSONLoadedConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://vault:p@localhost:1/sorovault")
	path := writeYAMLConfigFile(t, "http_addr: \":9999\"\nread_only: true\n")

	// Configuring the root command moves a package-level pointer, which
	// every other test in the package would inherit. Give the change back
	// on the way out.
	t.Cleanup(func() { configPath = "" })

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "config", "show", "--json"})
	var output strings.Builder
	root.SetOut(&output)
	root.SetErr(&output)
	require.NoError(t, root.Execute())

	var printed map[string]string
	require.NoError(t, json.Unmarshal([]byte(output.String()), &printed))
	assert.Equal(t, ":9999", printed["HTTP_ADDR"])
	assert.Equal(t, "true", printed["READ_ONLY"])
}

func printableRows(cfg *config.Config) string {
	var out strings.Builder
	for _, row := range effectiveRows(cfg) {
		out.WriteString(row.key)
		out.WriteString("\t")
		out.WriteString(row.value)
		out.WriteString("\n")
	}
	return out.String()
}
