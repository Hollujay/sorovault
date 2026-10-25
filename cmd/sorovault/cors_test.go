package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/config"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

func corsServer(t *testing.T, origins []string) *httptest.Server {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := registry.New(stellartest.New(), spec.XDRDecoder{}, store.NewMemory(), registry.Options{Logger: log})
	handler, err := newRouter(reg, log, nil, &config.Config{CORSOrigins: origins})
	require.NoError(t, err)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// httpResult is the part of a response these tests assert on. The helpers
// close the body before returning, so no caller holds an open response.
type httpResult struct {
	StatusCode int
	Header     http.Header
}

func corsRequest(t *testing.T, srv *httptest.Server, method, path, origin, requestMethod string) httpResult {
	t.Helper()

	req, err := http.NewRequest(method, srv.URL+path, nil)
	require.NoError(t, err)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if requestMethod != "" {
		req.Header.Set("Access-Control-Request-Method", requestMethod)
	}

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	return httpResult{StatusCode: resp.StatusCode, Header: resp.Header}
}

func TestCORSIgnoredWhenUnset(t *testing.T) {
	t.Parallel()

	srv := corsServer(t, nil)

	resp := corsRequest(t, srv, http.MethodGet, "/api/contracts", "https://app.example.test", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestCORSAllowlist(t *testing.T) {
	t.Parallel()

	srv := corsServer(t, []string{"https://app.example.test"})

	t.Run("allowed", func(t *testing.T) {
		resp := corsRequest(t, srv, http.MethodGet, "/api/contracts", "https://app.example.test", "")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "https://app.example.test", resp.Header.Get("Access-Control-Allow-Origin"))
		assert.Contains(t, resp.Header.Values("Vary"), "Origin")
	})

	t.Run("disallowed", func(t *testing.T) {
		resp := corsRequest(t, srv, http.MethodGet, "/api/contracts", "https://evil.example.test", "")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	})

	t.Run("preflight", func(t *testing.T) {
		resp := corsRequest(t, srv, http.MethodOptions, "/api/contracts",
			"https://app.example.test", http.MethodPost)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "https://app.example.test", resp.Header.Get("Access-Control-Allow-Origin"))
		assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), http.MethodPost)
	})

	t.Run("disallowed preflight", func(t *testing.T) {
		resp := corsRequest(t, srv, http.MethodOptions, "/api/contracts",
			"https://evil.example.test", http.MethodPost)
		assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	})
}

// TestCORSWildcardIsReadOnly proves "*" is not a blanket permission: GET is
// open to any origin, but a preflight or actual POST gets no CORS grant.
func TestCORSWildcardIsReadOnly(t *testing.T) {
	t.Parallel()

	srv := corsServer(t, []string{"*"})

	resp := corsRequest(t, srv, http.MethodGet, "/api/contracts", "https://anywhere.example.test", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))

	for _, method := range []string{http.MethodPost, http.MethodPut} {
		t.Run("preflight "+method, func(t *testing.T) {
			resp := corsRequest(t, srv, http.MethodOptions, "/api/contracts",
				"https://anywhere.example.test", method)
			assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
			assert.Empty(t, resp.Header.Get("Access-Control-Allow-Methods"))
		})
	}

	resp = corsRequest(t, srv, http.MethodPost, "/api/contracts", "https://anywhere.example.test", "")
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
}
