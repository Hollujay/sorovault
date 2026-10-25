package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/api"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

func newRateLimitedHarness(t *testing.T, opts api.RateLimitOptions) *harness {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fake := stellartest.New()
	db := store.NewMemory()
	reg := registry.New(fake, spec.XDRDecoder{}, db, registry.Options{Logger: log})

	server := api.NewServer(reg, log)
	server.SetRateLimit(opts)

	r := chi.NewRouter()
	r.Mount("/api", server.Routes())

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &harness{server: srv, fake: fake, store: db, api: server}
}

func postWithForwardedFor(t *testing.T, h *harness, path, body, forwardedFor string) (statusResult, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, h.server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}

	resp, err := h.server.Client().Do(req)
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return statusResult{StatusCode: resp.StatusCode, Header: resp.Header}, raw
}

// statusResult is the part of a response these tests assert on. The helper
// closes the body before returning, so no caller holds an open response.
type statusResult struct {
	StatusCode int
	Header     http.Header
}

// TestRateLimitTriggersAndResets proves the registration route refuses the
// request over the limit, says when to retry, and recovers once a token is
// available again.
func TestRateLimitTriggersAndResets(t *testing.T) {
	t.Parallel()

	// 60/minute is one token a second: long enough that the second request
	// cannot be refilled even on a loaded runner, short enough to wait out.
	h := newRateLimitedHarness(t, api.RateLimitOptions{PerMinute: 60, Burst: 1})
	h.fake.Deploy(idA, moduleWith(t, "balance"))
	body := `{"contract_id":"` + idA + `"}`

	resp, raw := h.do(t, http.MethodPost, "/api/contracts", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

	resp, raw = h.do(t, http.MethodPost, "/api/contracts", body)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode, string(raw))
	assert.NotEmpty(t, resp.Header.Get("Retry-After"))
	assert.Contains(t, h.decode(t, raw)["error"], "rate limit")

	// GET routes are read-only and stay unrestricted even while the write
	// limit is exhausted.
	resp, raw = h.do(t, http.MethodGet, "/api/contracts", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	assert.Eventually(t, func() bool {
		resp, _ := h.do(t, http.MethodPost, "/api/contracts", body)
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond, "the limit should reset once a token is available")
}

func TestRateLimitCoversRefresh(t *testing.T) {
	t.Parallel()

	h := newRateLimitedHarness(t, api.RateLimitOptions{PerMinute: 1, Burst: 1})
	path := "/api/contracts/" + idA + "/refresh"

	resp, raw := h.do(t, http.MethodPost, path, "")
	require.Equal(t, http.StatusNotFound, resp.StatusCode, string(raw))

	resp, raw = h.do(t, http.MethodPost, path, "")
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode, string(raw))
	assert.NotEmpty(t, resp.Header.Get("Retry-After"))
}

// TestRateLimitOnlyTrustsForwardedForWhenConfigured guards the client key:
// by default a caller must not be able to escape the limit by inventing a
// new address in X-Forwarded-For.
func TestRateLimitOnlyTrustsForwardedForWhenConfigured(t *testing.T) {
	t.Parallel()

	t.Run("ignored by default", func(t *testing.T) {
		t.Parallel()

		h := newRateLimitedHarness(t, api.RateLimitOptions{PerMinute: 1, Burst: 1})
		body := `{"contract_id":"` + idA + `"}`

		resp, raw := postWithForwardedFor(t, h, "/api/contracts", body, "203.0.113.1")
		require.NotEqual(t, http.StatusTooManyRequests, resp.StatusCode, string(raw))

		resp, raw = postWithForwardedFor(t, h, "/api/contracts", body, "203.0.113.2")
		assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, string(raw))
	})

	t.Run("trusted when configured", func(t *testing.T) {
		t.Parallel()

		h := newRateLimitedHarness(t, api.RateLimitOptions{
			PerMinute: 1, Burst: 1, TrustXForwardedFor: true,
		})
		body := `{"contract_id":"` + idA + `"}`

		resp, raw := postWithForwardedFor(t, h, "/api/contracts", body, "203.0.113.1")
		require.NotEqual(t, http.StatusTooManyRequests, resp.StatusCode, string(raw))

		resp, raw = postWithForwardedFor(t, h, "/api/contracts", body, "203.0.113.2")
		assert.NotEqual(t, http.StatusTooManyRequests, resp.StatusCode, string(raw))
	})
}
