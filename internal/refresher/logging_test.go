package refresher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/store"
)

// These tests are in package refresher rather than refresher_test so they can
// drive the unexported sweepAndLog directly: going through Run would need a
// short interval and wall-clock timing to reach the same code.

const (
	logTestNetwork = "testnet"
	logTestID      = "CSTALECONTRACT"
)

// logTestNow is the refresher's clock in these tests, so staleness is decided
// by the test rather than by when it happens to run.
var logTestNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// logRegistry is the slice of a registry a sweep needs, answering from canned
// values so no test reaches a network.
type logRegistry struct {
	store      store.Store
	networkErr error
	changed    bool
}

func (r *logRegistry) Network(ctx context.Context) (string, error) {
	// A cancelled sweep fails where a real one does: on the first call it
	// makes.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r.networkErr != nil {
		return "", r.networkErr
	}
	return logTestNetwork, nil
}

func (r *logRegistry) Refresh(ctx context.Context, contractID string) (*registry.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The sweep reads the result, so it must never be nil on success.
	return &registry.Result{
		Contract: store.Contract{
			ContractID:      contractID,
			Network:         logTestNetwork,
			CurrentWasmHash: "hash-2",
		},
		Changed: r.changed,
	}, nil
}

func (r *logRegistry) Store() store.Store { return r.store }

// staleStore holds one contract last refreshed two intervals ago, which makes
// it stale for a refresher whose interval is an hour.
func staleStore(t *testing.T) *store.Memory {
	t.Helper()

	db := store.NewMemory()
	db.SetClock(func() time.Time { return logTestNow.Add(-2 * time.Hour) })
	require.NoError(t, db.Save(context.Background(), store.Contract{
		ContractID:      logTestID,
		Network:         logTestNetwork,
		CurrentWasmHash: "hash-1",
	}, &model.Interface{}))
	return db
}

// newLogRefresher builds a refresher whose log goes to the returned buffer,
// captured as JSON so level, message and attributes can be asserted as data.
func newLogRefresher(t *testing.T, reg *logRegistry) (*Refresher, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	r, err := New(reg, Options{
		Interval: time.Hour,
		Logger:   slog.New(slog.NewJSONHandler(&buf, nil)),
		Now:      func() time.Time { return logTestNow },
	})
	require.NoError(t, err)
	return r, &buf
}

func decodeLogs(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		require.NoError(t, json.Unmarshal(line, &record), "log line is not JSON: %s", line)
		records = append(records, record)
	}
	return records
}

// TestSweepAndLogLogsSuccessfulSweep pins the one line operators read to see
// whether automatic refresh is working.
func TestSweepAndLogLogsSuccessfulSweep(t *testing.T) {
	t.Parallel()

	r, buf := newLogRefresher(t, &logRegistry{store: staleStore(t), changed: true})
	r.sweepAndLog(context.Background())

	records := decodeLogs(t, buf)
	require.Len(t, records, 1, "a successful sweep logs exactly one summary line")

	record := records[0]
	assert.Equal(t, "INFO", record["level"])
	assert.Equal(t, "refresh sweep complete", record["msg"])
	assert.Equal(t, float64(1), record["checked"], "the summary must say what was checked")
	assert.Equal(t, float64(1), record["changed"], "the summary must say what changed")
	assert.Equal(t, float64(0), record["failed"], "the summary must say what failed")

	took, ok := record["took"].(float64)
	require.True(t, ok, "the summary must say how long the sweep took")
	assert.GreaterOrEqual(t, took, float64(0))
}

// TestSweepAndLogWarnsOnFailure proves a sweep that cannot start is reported
// as a warning, not as a completed summary. The underlying error is kept out
// of logs because it may contain sensitive RPC details.
func TestSweepAndLogWarnsOnFailure(t *testing.T) {
	t.Parallel()

	r, buf := newLogRefresher(t, &logRegistry{networkErr: errors.New("rpc down")})
	r.sweepAndLog(context.Background())

	records := decodeLogs(t, buf)
	require.Len(t, records, 1, "a failed sweep logs a warning instead of a summary")

	record := records[0]
	assert.Equal(t, "WARN", record["level"])
	assert.Equal(t, "refresh sweep failed", record["msg"])
	assert.NotContains(t, record, "error", "the warning must not expose the underlying error")
}

// TestSweepAndLogSilentOnCancellation proves a shutdown is not reported as a
// failure: the refresher stops quietly, and Run already returns nil.
func TestSweepAndLogSilentOnCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r, buf := newLogRefresher(t, &logRegistry{})
	r.sweepAndLog(ctx)

	assert.Zero(t, buf.Len(), "a cancelled sweep must log nothing: %s", buf.String())
}

// TestNewDefaultsNilLoggerAndNow covers the options callers are allowed to
// leave unset. The fields are unexported, which is the other reason this test
// lives in the package.
func TestNewDefaultsNilLoggerAndNow(t *testing.T) {
	t.Parallel()

	r, err := New(&logRegistry{}, Options{Interval: time.Minute})
	require.NoError(t, err)

	assert.Same(t, slog.Default(), r.log, "a nil Logger must fall back to slog.Default")
	assert.WithinDuration(t, time.Now(), r.now(), time.Second, "a nil Now must fall back to time.Now")
}
