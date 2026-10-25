package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/config"
	"github.com/soroworks/sorovault/internal/refresher"
)

// discardLogger is a logger that keeps test output quiet.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRefreshNotifierDisabledWithoutWebhookURL proves that refresh is silent
// unless an endpoint is configured, even if a secret is left in the
// environment.
func TestRefreshNotifierDisabledWithoutWebhookURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		secret string
	}{
		{name: "no configuration"},
		{name: "secret without a URL", secret: "s3cret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a := &app{
				cfg: &config.Config{RefreshWebhookSecret: tt.secret},
				log: discardLogger(),
			}

			notifier, err := refreshNotifier(a)
			require.NoError(t, err)
			assert.Nil(t, notifier, "no webhook URL means no notifier")
		})
	}
}

// TestRefreshNotifierPostsToConfiguredWebhook proves the notifier built from
// configuration is a real webhook: it posts the change to the configured URL
// and signs the payload with the configured secret.
func TestRefreshNotifierPostsToConfiguredWebhook(t *testing.T) {
	t.Parallel()

	type delivery struct {
		method      string
		contentType string
		signature   string
		body        []byte
	}

	const secret = "shared-secret"
	deliveries := make(chan delivery, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		deliveries <- delivery{
			method:      r.Method,
			contentType: r.Header.Get("Content-Type"),
			signature:   r.Header.Get(refresher.SignatureHeader),
			body:        body,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: &config.Config{
			RefreshWebhookURL:    srv.URL,
			RefreshWebhookSecret: secret,
		},
		log: discardLogger(),
	}

	notifier, err := refreshNotifier(a)
	require.NoError(t, err)
	require.NotNil(t, notifier, "a configured URL must yield a notifier")

	change := refresher.Change{
		ContractID:   "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
		Network:      "testnet",
		OldWasmHash:  "old",
		NewWasmHash:  "new",
		InterfaceURL: "/api/contracts/x",
	}
	require.NoError(t, notifier.Notify(context.Background(), change))

	var got delivery
	select {
	case got = <-deliveries:
	case <-time.After(time.Second):
		t.Fatal("the configured webhook was not called")
	}

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "application/json", got.contentType)
	assert.Equal(t, refresher.Sign(secret, got.body), got.signature,
		"the configured secret must sign the payload")

	var posted refresher.Change
	require.NoError(t, json.Unmarshal(got.body, &posted))
	assert.Equal(t, change.ContractID, posted.ContractID)
	assert.Equal(t, change.Network, posted.Network)
	assert.False(t, posted.ChangedAt.IsZero())
}

// TestRefreshNotifierRejectsInvalidURL proves a bad endpoint is reported
// while the process is starting rather than when the first contract changes.
func TestRefreshNotifierRejectsInvalidURL(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"not a url", "not-a-url", "/relative", "ftp://example.test/hook", "http://"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			a := &app{
				cfg: &config.Config{RefreshWebhookURL: raw},
				log: discardLogger(),
			}

			notifier, err := refreshNotifier(a)
			require.Error(t, err, "URL %q should be rejected", raw)
			assert.Nil(t, notifier)
		})
	}
}

// TestStartRefresherReportsWebhookConfigurationErrors proves the notifier
// error reaches serve's caller instead of starting a refresher that cannot
// deliver notifications.
func TestStartRefresherReportsWebhookConfigurationErrors(t *testing.T) {
	t.Parallel()

	a := &app{
		cfg: &config.Config{
			RefreshInterval:   time.Hour,
			RPCTimeout:        time.Second,
			RefreshWebhookURL: "ftp://example.test/hook",
		},
		log: discardLogger(),
	}

	gotRefresher, stop, err := startRefresher(context.Background(), a)
	require.Error(t, err)
	assert.Nil(t, gotRefresher)
	assert.Nil(t, stop)
}
