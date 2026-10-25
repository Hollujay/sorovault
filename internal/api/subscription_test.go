package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionLifecycle(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	body := `{"contract_ids":["` + idA + `"],"url":"https://hooks.example.test/sorovault"}`

	resp, raw := h.do(t, http.MethodPost, "/api/subscriptions", body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	created := h.decode(t, raw)
	secret, _ := created["id"].(string)
	require.True(t, strings.HasPrefix(secret, "sub_"), "got %q", secret)
	assert.Len(t, secret, len("sub_")+64, "the secret is 256 random bits, not a counter")

	// The listing shows the subscription but never its secret.
	resp, raw = h.do(t, http.MethodGet, "/api/subscriptions", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	assert.NotContains(t, string(raw), secret)
	subs := h.decode(t, raw)["subscriptions"].([]any)
	require.Len(t, subs, 1)
	assert.Equal(t, true, subs[0].(map[string]any)["active"])

	resp, raw = h.do(t, http.MethodDelete, "/api/subscriptions/"+secret, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	resp, raw = h.do(t, http.MethodDelete, "/api/subscriptions/"+secret, "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(raw))
}

func TestSubscribeRejectsBadRequests(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	for name, body := range map[string]string{
		"no contracts":     `{"contract_ids":[],"url":"https://hooks.example.test"}`,
		"bad contract":     `{"contract_ids":["not-a-contract"],"url":"https://hooks.example.test"}`,
		"relative url":     `{"contract_ids":["` + idA + `"],"url":"/hook"}`,
		"non-http url":     `{"contract_ids":["` + idA + `"],"url":"file:///etc/passwd"}`,
		"unknown property": `{"contract_ids":["` + idA + `"],"url":"https://hooks.example.test","extra":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp, raw := h.do(t, http.MethodPost, "/api/subscriptions", body)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw))
		})
	}
}
