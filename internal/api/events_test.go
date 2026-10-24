package api_test

import (
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/stellar/stellartest"
)

// TestListEvents checks the events endpoint. It mirrors the functions
// endpoint, so the tests mirror its tests: a real contract first, then the
// version selection and the error cases.
func TestListEvents(t *testing.T) {
	t.Parallel()

	// newZKVoteHarness registers the real testnet fixture, which declares
	// exactly one event, Voted.
	newZKVoteHarness := func(t *testing.T) *harness {
		t.Helper()

		module, err := os.ReadFile("../spec/testdata/zkvote.wasm")
		require.NoError(t, err)

		h := newHarness(t)
		h.fake.Deploy(idA, module)
		resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
		require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))
		return h
	}

	t.Run("lists the real fixture's event", func(t *testing.T) {
		t.Parallel()

		h := newZKVoteHarness(t)
		resp, raw := h.do(t, http.MethodGet, "/api/contracts/"+idA+"/events", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
		assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

		got := h.decode(t, raw)
		assert.Equal(t, idA, got["contract_id"])
		assert.Equal(t, "testnet", got["network"])
		assert.NotEmpty(t, got["wasm_hash"])

		events := got["events"].([]any)
		require.Len(t, events, 1)
		voted := events[0].(map[string]any)
		assert.Equal(t, "Voted", voted["name"])
		assert.Equal(t, []any{"voted"}, voted["prefix_topics"])
		assert.Equal(t, "map", voted["data_format"])

		params := voted["params"].([]any)
		require.Len(t, params, 2)
		voter := params[0].(map[string]any)
		assert.Equal(t, "voter", voter["name"])
		assert.Equal(t, "topic-list", voter["location"])
		assert.Equal(t, "address", voter["type"].(map[string]any)["kind"])
		index := params[1].(map[string]any)
		assert.Equal(t, "proposal_index", index["name"])
		assert.Equal(t, "data", index["location"])
		assert.Equal(t, "u32", index["type"].(map[string]any)["kind"])
	})

	t.Run("a contract with no events answers with an empty list", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.fake.Deploy(idA, moduleWith(t, "balance"))
		resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
		require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

		resp, raw = h.do(t, http.MethodGet, "/api/contracts/"+idA+"/events", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

		got := h.decode(t, raw)
		require.Contains(t, got, "events")
		assert.Empty(t, got["events"], "no events is [], never null")
		assert.IsType(t, []any{}, got["events"], "the empty list marshals as an array")
	})

	t.Run("wasm_hash selects a superseded version", func(t *testing.T) {
		t.Parallel()

		v1, err := os.ReadFile("../spec/testdata/zkvote.wasm")
		require.NoError(t, err)
		oldHash := stellartest.HashOf(v1)

		h := newZKVoteHarness(t)
		h.fake.Upgrade(idA, moduleWith(t, "balance"))
		resp, raw := h.do(t, http.MethodPost, "/api/contracts/"+idA+"/refresh", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

		// The current interface has no events.
		resp, raw = h.do(t, http.MethodGet, "/api/contracts/"+idA+"/events", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
		require.Empty(t, h.decode(t, raw)["events"])

		// The first version's event is still reachable by hash, exactly as
		// the functions endpoint selects one.
		resp, raw = h.do(t, http.MethodGet,
			"/api/contracts/"+idA+"/events?wasm_hash="+oldHash, "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
		events := h.decode(t, raw)["events"].([]any)
		require.Len(t, events, 1)
		assert.Equal(t, "Voted", events[0].(map[string]any)["name"])
	})

	t.Run("missing contract", func(t *testing.T) {
		t.Parallel()

		h := newZKVoteHarness(t)
		resp, _ := h.do(t, http.MethodGet, "/api/contracts/"+idB+"/events", "")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}
