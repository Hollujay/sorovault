package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

type pingStore struct {
	store.Store
	mu   sync.Mutex
	err  error
	ping int
}

func (s *pingStore) Ping(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ping++
	return s.err
}

func (s *pingStore) pings() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ping
}

func readinessServer(t *testing.T, db store.Store, fake *stellartest.Fake) *httptest.Server {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := registry.New(fake, spec.XDRDecoder{}, db, registry.Options{Logger: log})
	handler, err := newRouter(reg, log, nil, nil)
	require.NoError(t, err)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func getReadiness(t *testing.T, srv *httptest.Server) (httpResult, readinessResponse, []byte) {
	t.Helper()

	resp, err := srv.Client().Get(srv.URL + "/readyz")
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	var report readinessResponse
	require.NoError(t, json.Unmarshal(raw, &report), "response was not JSON: %s", raw)
	return httpResult{StatusCode: resp.StatusCode, Header: resp.Header}, report, raw
}

func TestReadyzHealthy(t *testing.T) {
	t.Parallel()

	srv := readinessServer(t, store.NewMemory(), stellartest.New())

	resp, report, raw := getReadiness(t, srv)
	assert.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	assert.Equal(t, "ok", report.Status)
	assert.Equal(t, "ok", report.Checks["database"])
	assert.Equal(t, "ok", report.Checks["rpc"])
	assert.NotEmpty(t, report.Version)
}

func TestReadyzDatabaseDown(t *testing.T) {
	t.Parallel()

	db := &pingStore{Store: store.NewMemory(), err: errors.New("connection refused")}
	srv := readinessServer(t, db, stellartest.New())

	resp, report, raw := getReadiness(t, srv)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, string(raw))
	assert.Equal(t, "unavailable", report.Status)
	assert.Contains(t, report.Checks["database"], "connection refused")
	assert.Equal(t, "ok", report.Checks["rpc"])
}

func TestReadyzRPCDown(t *testing.T) {
	t.Parallel()

	fake := stellartest.New()
	fake.NetworkErr = errors.New("rpc is down")
	srv := readinessServer(t, store.NewMemory(), fake)

	resp, report, raw := getReadiness(t, srv)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, string(raw))
	assert.Equal(t, "unavailable", report.Status)
	assert.Equal(t, "ok", report.Checks["database"])
	assert.Contains(t, report.Checks["rpc"], "rpc is down")
}

// TestHealthzStaysLiveWhenDependenciesAreDown keeps the two probes
// deliberately different: liveness must not restart a process over a
// dependency that will recover.
func TestHealthzStaysLiveWhenDependenciesAreDown(t *testing.T) {
	t.Parallel()

	db := &pingStore{Store: store.NewMemory(), err: errors.New("connection refused")}
	fake := stellartest.New()
	fake.NetworkErr = errors.New("rpc is down")
	srv := readinessServer(t, db, fake)

	resp, err := srv.Client().Get(srv.URL + "/healthz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestReadyzCachesForAFewSeconds(t *testing.T) {
	t.Parallel()

	db := &pingStore{Store: store.NewMemory()}
	probe := newReadinessProbe(db, func(context.Context) error { return nil })

	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	probe.now = func() time.Time { return now }

	_, ready := probe.report(context.Background())
	require.True(t, ready)
	_, ready = probe.report(context.Background())
	require.True(t, ready)
	assert.Equal(t, 1, db.pings(), "a cached answer must not re-check the database")

	now = now.Add(readinessCacheTTL)
	_, ready = probe.report(context.Background())
	require.True(t, ready)
	assert.Equal(t, 2, db.pings(), "expiring the cache re-checks the database")
}
