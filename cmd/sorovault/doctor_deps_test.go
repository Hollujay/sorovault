package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/config"
	"github.com/soroworks/sorovault/internal/stellar"
)

// clearDoctorEnv removes every configuration variable that could change how
// the doctor's dependencies are built, so a value in the developer's shell
// cannot make these tests pass or fail for the wrong reason. t.Setenv
// restores the previous values when the test ends.
func clearDoctorEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"DATABASE_URL",
		"DATABASE_URL_FILE",
		"RPC_URL",
		"NETWORK_PASSPHRASE",
		"HTTP_ADDR",
		"LOG_LEVEL",
		"RPC_TIMEOUT",
		"REFRESH_INTERVAL",
		"MAX_CONCURRENT_REGISTRATIONS",
		"REFRESH_WEBHOOK_URL",
		"REFRESH_WEBHOOK_SECRET",
	} {
		t.Setenv(key, "")
	}
}

// TestDoctorWithoutDatabaseConfigurationIsReported proves a missing database
// URL reaches the operator as guidance naming the setting, rather than as a
// panic or an obscure driver error.
func TestDoctorWithoutDatabaseConfigurationIsReported(t *testing.T) {
	clearDoctorEnv(t)

	cmd := newDoctorCmd(doctorDepsFromEnv)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, out.String()+err.Error(), "DATABASE_URL",
		"the report must say which setting is missing")
}

// TestNewDoctorDepsInvalidRPCURLFailsChecks proves a malformed RPC URL is
// reported by the checks that use it instead of being accepted at
// construction time and surfacing later as a panic.
func TestNewDoctorDepsInvalidRPCURLFailsChecks(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("DATABASE_URL", "postgres://sorovault:sorovault@localhost:5432/sorovault")
	t.Setenv("RPC_URL", "://invalid")
	t.Setenv("NETWORK_PASSPHRASE", stellar.NetworkTestnet)

	deps, err := doctorDepsFromEnv()
	require.NoError(t, err, "an unparseable URL is a check failure, not a wiring failure")
	t.Cleanup(func() { _ = deps.Close() })

	// Only the endpoint checks run here: the database is not part of what
	// this test is pinning, and it must not be contacted.
	var checks []doctorCheck
	for _, check := range doctorChecks(deps) {
		if check.Name == "rpc" || check.Name == "passphrase" {
			checks = append(checks, check)
		}
	}
	require.Len(t, checks, 2)

	report := runDoctor(context.Background(), checks)
	require.False(t, report.OK)

	for _, name := range []string{"rpc", "passphrase"} {
		result := findDoctorResult(t, report, name)
		assert.False(t, result.OK, "the %s check must fail for an invalid RPC URL", name)
		assert.NotEmpty(t, result.Error, "the %s check must explain the failure", name)
	}
}

// TestNewDoctorDepsBuildsEveryDependency proves a valid configuration yields
// all the clients the doctor checks need, with the configured passphrase and
// a close hook.
func TestNewDoctorDepsBuildsEveryDependency(t *testing.T) {
	clearDoctorEnv(t)

	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)

		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding RPC request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Method != "getNetwork" {
			t.Errorf("unexpected RPC method %q", req.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"passphrase":      stellar.NetworkTestnet,
				"protocolVersion": 23,
			},
		})
	}))
	t.Cleanup(srv.Close)

	t.Setenv("DATABASE_URL", "postgres://sorovault:sorovault@localhost:5432/sorovault")
	t.Setenv("RPC_URL", srv.URL)
	t.Setenv("NETWORK_PASSPHRASE", stellar.NetworkTestnet)

	cfg, err := config.Load()
	require.NoError(t, err)
	deps, err := newDoctorDeps(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = deps.Close() })

	assert.NotNil(t, deps.Ping, "the database check needs a ping client")
	assert.NotNil(t, deps.Migrations, "the migrations check needs a status client")
	assert.NotNil(t, deps.Passphrase, "the RPC checks need a passphrase client")
	assert.NotNil(t, deps.Close, "the doctor must be able to release its clients")
	assert.Equal(t, stellar.NetworkTestnet, deps.ExpectPassphrase)

	// The passphrase is read once and remembered: the reachability and
	// passphrase checks share a single round trip.
	for range 2 {
		got, err := deps.Passphrase(context.Background())
		require.NoError(t, err)
		assert.Equal(t, stellar.NetworkTestnet, got)
	}
	assert.Equal(t, int64(1), calls.Load(), "the passphrase must be read once")
}

// findDoctorResult returns the named result from a report.
func findDoctorResult(t *testing.T, report doctorReport, name string) doctorResult {
	t.Helper()

	for _, result := range report.Checks {
		if result.Name == name {
			return result
		}
	}
	require.FailNowf(t, "missing doctor check", "no %q check in the report", name)
	return doctorResult{}
}
