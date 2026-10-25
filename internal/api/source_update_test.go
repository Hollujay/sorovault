package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSourceUpdateHarness registers idA so the source endpoint has a contract
// to update.
func newSourceUpdateHarness(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)
	h.fake.Deploy(idA, moduleWith(t, "balance"))
	resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))
	return h
}

func TestUpdateContractSourceAcceptsHTTPAndHTTPS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sourceURL    string
		sourceCommit string
	}{
		{
			name:         "https with a 40-character commit",
			sourceURL:    "https://github.com/soroworks/sorovault",
			sourceCommit: "0123456789abcdef0123456789abcdef01234567",
		},
		{
			name:         "https with a 64-character commit",
			sourceURL:    "https://github.com/soroworks/sorovault/tree/main",
			sourceCommit: strings.Repeat("a", 64),
		},
		{
			name:      "http is allowed by the handler",
			sourceURL: "http://example.com/sorovault.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newSourceUpdateHarness(t)

			body := fmt.Sprintf(`{"source_url":%q`, tt.sourceURL)
			if tt.sourceCommit != "" {
				body += fmt.Sprintf(`,"source_commit":%q`, tt.sourceCommit)
			}
			body += "}"

			resp, raw := h.do(t, http.MethodPut, "/api/contracts/"+idA+"/source", body)
			require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
			assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

			got := h.decode(t, raw)
			assert.Equal(t, tt.sourceURL, got["source_url"])
			if tt.sourceCommit == "" {
				assert.NotContains(t, got, "source_commit")
			} else {
				assert.Equal(t, tt.sourceCommit, got["source_commit"])
			}

			// The update is persisted, not just echoed back.
			resp, raw = h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
			require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
			got = h.decode(t, raw)
			assert.Equal(t, tt.sourceURL, got["source_url"])
			if tt.sourceCommit == "" {
				assert.NotContains(t, got, "source_commit")
			} else {
				assert.Equal(t, tt.sourceCommit, got["source_commit"])
			}
		})
	}
}

func TestUpdateContractSourceRejectsInvalidLinks(t *testing.T) {
	t.Parallel()

	overlong := `{"source_url":"https://example.com/` + strings.Repeat("a", 1<<17) + `"}`

	tests := []struct {
		name string
		body string
	}{
		{"javascript scheme", `{"source_url":"javascript:alert(1)"}`},
		{"data scheme", `{"source_url":"data:text/plain,hello"}`},
		{"ftp scheme", `{"source_url":"ftp://example.com/repo"}`},
		{"relative link", `{"source_url":"github.com/soroworks/sorovault"}`},
		{"missing host", `{"source_url":"https://"}`},
		{"empty link", `{"source_url":"   "}`},
		{"malformed URL", `{"source_url":"https://github.com/%zz"}`},
		{"short commit", `{"source_url":"https://github.com/soroworks/sorovault","source_commit":"abc123"}`},
		{"non-hex commit", `{"source_url":"https://github.com/soroworks/sorovault","source_commit":"` + strings.Repeat("g", 40) + `"}`},
		{"overlong source link", overlong},
		{"empty body", ""},
		{"not JSON", "{"},
		{"unknown field", `{"source_url":"https://github.com/soroworks/sorovault","extra":1}`},
		{"two objects", `{"source_url":"https://github.com/soroworks/sorovault"}{"source_url":"https://example.com"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newSourceUpdateHarness(t)

			resp, raw := h.do(t, http.MethodPut, "/api/contracts/"+idA+"/source", tt.body)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw))
			assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
			assert.NotEmpty(t, h.decode(t, raw)["error"])

			// A refused write must leave the contract untouched.
			resp, raw = h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
			require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
			got := h.decode(t, raw)
			assert.NotContains(t, got, "source_url")
			assert.NotContains(t, got, "source_commit")
		})
	}
}

func TestUpdateContractSourceRejectedWriteKeepsPreviousLink(t *testing.T) {
	t.Parallel()

	h := newSourceUpdateHarness(t)

	const sourceURL = "https://github.com/soroworks/sorovault"
	const sourceCommit = "0123456789abcdef0123456789abcdef01234567"
	body := fmt.Sprintf(`{"source_url":%q,"source_commit":%q}`, sourceURL, sourceCommit)

	resp, raw := h.do(t, http.MethodPut, "/api/contracts/"+idA+"/source", body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	resp, raw = h.do(t, http.MethodPut, "/api/contracts/"+idA+"/source", `{"source_url":"javascript:alert(1)"}`)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw))

	resp, raw = h.do(t, http.MethodGet, "/api/contracts/"+idA, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	got := h.decode(t, raw)
	assert.Equal(t, sourceURL, got["source_url"], "a rejected update must not clear the stored link")
	assert.Equal(t, sourceCommit, got["source_commit"])
}

func TestUpdateContractSourceUnknownContract(t *testing.T) {
	t.Parallel()

	h := newSourceUpdateHarness(t)
	body := `{"source_url":"https://github.com/soroworks/sorovault"}`

	resp, raw := h.do(t, http.MethodPut, "/api/contracts/"+idB+"/source", body)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(raw))
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	got := h.decode(t, raw)
	assert.NotEmpty(t, got["error"])
	assert.Contains(t, got["error"], idB)
}
