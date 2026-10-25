package api_test

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/api"
)

// newMiddlewareServer mounts the production middleware stack on a tiny
// router, with logging discarded so a recovered panic does not spam the test
// output.
func newMiddlewareServer(t *testing.T, routes func(chi.Router)) *httptest.Server {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := chi.NewRouter()
	r.Use(api.Middleware(log)...)
	routes(r)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func middlewareRequest(t *testing.T, srv *httptest.Server, path string, headers map[string]string) (int, http.Header, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, string(body)
}

func TestMiddlewareRecoversPanic(t *testing.T) {
	t.Parallel()

	srv := newMiddlewareServer(t, func(r chi.Router) {
		r.Get("/panic", func(http.ResponseWriter, *http.Request) {
			panic("handler exploded")
		})
		r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		})
	})

	status, header, body := middlewareRequest(t, srv, "/panic", nil)
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.NotContains(t, body, "handler exploded",
		"a panic must not leak its value to the caller")
	panicID := header.Get("X-Request-Id")
	assert.NotEmpty(t, panicID, "even a recovered panic carries a request ID")

	status, header, body = middlewareRequest(t, srv, "/ok", nil)
	assert.Equal(t, http.StatusOK, status, "the server keeps serving after a panic")
	assert.Equal(t, "ok", body)
	assert.NotEmpty(t, header.Get("X-Request-Id"))
	assert.NotEqual(t, panicID, header.Get("X-Request-Id"), "every request gets its own ID")
}

func TestMiddlewareRequestID(t *testing.T) {
	t.Parallel()

	srv := newMiddlewareServer(t, func(r chi.Router) {
		r.Get("/id", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, chimiddleware.GetReqID(r.Context()))
		})
	})

	t.Run("generated", func(t *testing.T) {
		status, header, first := middlewareRequest(t, srv, "/id", nil)
		require.Equal(t, http.StatusOK, status)
		require.NotEmpty(t, first, "the handler sees a request ID in its context")
		assert.Equal(t, first, header.Get("X-Request-Id"),
			"the response advertises the same ID the handler sees")

		status, _, second := middlewareRequest(t, srv, "/id", nil)
		require.Equal(t, http.StatusOK, status)
		assert.NotEqual(t, first, second)
	})

	t.Run("client supplied", func(t *testing.T) {
		headers := map[string]string{"X-Request-Id": "client-supplied-request-id"}
		status, header, body := middlewareRequest(t, srv, "/id", headers)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "client-supplied-request-id", body)
		assert.Equal(t, "client-supplied-request-id", header.Get("X-Request-Id"))
	})
}

// TestMiddlewareIgnoresXForwardedFor pins the documented decision not to use
// chi's RealIP middleware: a client cannot choose the address the server
// believes it came from by setting X-Forwarded-For.
func TestMiddlewareIgnoresXForwardedFor(t *testing.T) {
	t.Parallel()

	srv := newMiddlewareServer(t, func(r chi.Router) {
		r.Get("/remote-addr", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, r.RemoteAddr) //nolint:gosec // a test handler echoing the address back as text/plain
		})
	})

	headers := map[string]string{"X-Forwarded-For": "203.0.113.7, 198.51.100.4"}
	status, _, body := middlewareRequest(t, srv, "/remote-addr", headers)
	require.Equal(t, http.StatusOK, status)

	host, _, err := net.SplitHostPort(body)
	require.NoError(t, err, "RemoteAddr must remain the socket address, got %q", body)
	ip := net.ParseIP(host)
	require.NotNil(t, ip)
	assert.True(t, ip.IsLoopback(), "the connection is local, not the forwarded address")
	assert.NotContains(t, body, "203.0.113.7")
}
