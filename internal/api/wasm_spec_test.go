package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

// wasmSpecStore wraps the in-memory store and fails GetWasmSpec on demand,
// standing in for a database outage. With err nil it behaves normally.
type wasmSpecStore struct {
	store.Store
	err error
}

func (s wasmSpecStore) GetWasmSpec(ctx context.Context, network, wasmHash string) (*store.WasmSpec, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.Store.GetWasmSpec(ctx, network, wasmHash)
}

func TestGetWasmSpecRejectsMalformedHash(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	tests := []struct {
		name string
		hash string
	}{
		{"not hexadecimal", strings.Repeat("z", 64)},
		{"too short", strings.Repeat("a", 62)},
		{"odd length", strings.Repeat("a", 63)},
		{"too long", strings.Repeat("a", 66)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, raw := h.do(t, http.MethodGet, "/api/wasm/"+tt.hash, "")
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw))
			assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
			assert.Equal(t, "invalid wasm hash", h.decode(t, raw)["error"],
				"a malformed hash must be refused before it reaches the store")
		})
	}
}

func TestGetWasmSpecUnknownHash(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	hash := strings.Repeat("ab", 32)

	resp, raw := h.do(t, http.MethodGet, "/api/wasm/"+hash, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(raw))
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	got := h.decode(t, raw)
	assert.NotEmpty(t, got["error"])
	assert.Contains(t, got["error"], hash)
}

// TestGetWasmSpecStoreFailure checks that an unexpected store failure is a
// generic 500: the caller learns nothing about the database behind it.
func TestGetWasmSpecStoreFailure(t *testing.T) {
	t.Parallel()

	const secret = "dial tcp 10.0.0.5:5432: password authentication failed for user postgres" //nolint:gosec // a fake database error the test asserts is not leaked
	h := newHarnessWithStore(t, stellartest.New(), wasmSpecStore{
		Store: store.NewMemory(),
		err:   errors.New(secret),
	})

	resp, raw := h.do(t, http.MethodGet, "/api/wasm/"+strings.Repeat("ab", 32), "")
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode, string(raw))
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
	assert.Equal(t, "internal error", h.decode(t, raw)["error"])
	assert.NotContains(t, string(raw), secret,
		"the underlying store error must not leak to the caller")
	assert.NotContains(t, string(raw), "password")
}
