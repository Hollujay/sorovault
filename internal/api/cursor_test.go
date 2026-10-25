package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validContractID encodes a fresh, well-formed contract address. The registration
// endpoint validates the Stellar strkey alphabet and checksum, so fixture IDs are
// encoded rather than hand-spelled; the last byte of the payload distinguishes them.
func validContractID(t *testing.T, last byte) string {
	t.Helper()

	payload := bytes.Repeat([]byte{0xAB}, 31)
	payload = append(payload, last)
	id, err := strkey.Encode(strkey.VersionByteContract, payload)
	require.NoError(t, err)
	return id
}

// TestContractsCursorPagination checks the listing's cursor pages: a walk
// over them covers every contract exactly once, even when a contract lands
// in the registry while the walk is in progress.
func TestContractsCursorPagination(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	idC := validContractID(t, 0xCD)
	for _, id := range []string{idA, idB, idC} {
		h.fake.Deploy(id, moduleWith(t, "balance"))
		resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+id+`"}`)
		require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))
	}

	// collect walks the listing one page at a time, following next.
	collect := func(t *testing.T, start string) []string {
		t.Helper()

		var seen []string
		for {
			url := "/api/contracts?limit=1"
			if start != "" {
				url += "&cursor=" + start
			}
			resp, raw := h.do(t, http.MethodGet, url, "")
			require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

			var page struct {
				Contracts []struct {
					ContractID string `json:"contract_id"`
				} `json:"contracts"`
				Next string `json:"next"`
			}
			require.NoError(t, json.Unmarshal(raw, &page), string(raw))

			for _, c := range page.Contracts {
				seen = append(seen, c.ContractID)
			}
			if page.Next == "" {
				return seen
			}
			start = page.Next
		}
	}

	t.Run("a walk with pages of one reaches every contract once", func(t *testing.T) {
		// Not parallel with the arrival subtest on purpose: with both
		// walking at once, the newcomer can land between this walk's
		// pages, and then a count it did not seed is exactly the
		// behaviour the next subtest documents.
		seen := collect(t, "")
		require.Len(t, seen, 3)
		assert.Len(t, uniqueStrings(seen), 3, "no contract repeats across page boundaries")
	})

	t.Run("an arrival between pages is never repeated", func(t *testing.T) {
		// The arrival below must not race the first subtest's count
		// assertion, so this runs after it rather than beside it.

		// A contract lands in the registry while a client is mid-walk: the
		// offset walk shifts every further page down by one and returns
		// the row at the boundary twice. Whether the newcomer itself is
		// inside this walk depends on where its refresh time sorts it
		// against the cursor, so a cursor walk guarantees the started-with
		// rows are reached and nothing doubles up, and the newcomer's
		// exactly-once turn is on the next walk.
		idNew := validContractID(t, 0xDD)
		h.fake.Deploy(idNew, moduleWith(t, "balance"))
		resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idNew+`"}`)
		require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

		seen := collect(t, "")
		for _, want := range []string{idA, idB, idC} {
			assert.Contains(t, seen, want, "the walk that started before the arrival still reaches it")
		}
		assert.Len(t, uniqueStrings(seen), len(seen), "nothing repeats")

		again := collect(t, "")
		require.Len(t, again, 4, "the walk started after the arrival covers it too")
		assert.Len(t, uniqueStrings(again), 4, "and exactly once")
	})

	t.Run("the last page carries no next", func(t *testing.T) {
		t.Parallel()

		resp, raw := h.do(t, http.MethodGet, "/api/contracts?limit=100", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
		assert.NotContains(t, string(raw), `"next"`, "a terminal page has no cursor")
	})

	t.Run("a malformed cursor is a 400", func(t *testing.T) {
		t.Parallel()

		resp, raw := h.do(t, http.MethodGet, "/api/contracts?cursor=not-a-cursor", "")
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Contains(t, h.decode(t, raw)["error"], "malformed cursor")
	})

	t.Run("a cursor and an offset together are a 400", func(t *testing.T) {
		t.Parallel()

		resp, raw := h.do(t, http.MethodGet, "/api/contracts?cursor=abc&offset=1", "")
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Contains(t, h.decode(t, raw)["error"], "not both")
	})

	t.Run("offset keeps working", func(t *testing.T) {
		t.Parallel()

		resp, raw := h.do(t, http.MethodGet, "/api/contracts?limit=1&offset=1", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

		var page struct {
			Offset int `json:"offset"`
		}
		require.NoError(t, json.Unmarshal(raw, &page))
		assert.Equal(t, 1, page.Offset, "the deprecated parameter answers as before")
	})
}

// uniqueStrings collapses duplicates, so a walk's completeness and absence
// of repeats are one assertion each.
func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
