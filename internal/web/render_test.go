package web

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

// A real testnet contract address, so the page under test carries a
// plausible contract ID.
const renderContractID = "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"

// newRenderServer builds the UI server the way NewServer does in production
// and captures its log, so a test can assert both what the visitor is told
// and what the operator is.
func newRenderServer(t *testing.T) (*Server, *bytes.Buffer) {
	t.Helper()

	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	reg := registry.New(stellartest.New(), spec.XDRDecoder{}, store.NewMemory(),
		registry.Options{Logger: log})

	s, err := NewServer(reg, log)
	require.NoError(t, err)

	return s, &logs
}

// contractDataWithoutInterface is the data of a contract whose spec has no
// interface. The contract page dereferences it, so the template fails partway
// through — after the layout and the heading have already been written.
func contractDataWithoutInterface() contractData {
	return contractData{
		Contract: store.Contract{ContractID: renderContractID, Network: "testnet"},
		Spec:     store.Spec{WasmHash: "0badc0de"},
	}
}

// TestRenderTemplateErrorIsAClean500 covers the failure path of a template
// execution: the visitor gets a 500 and an error response, never the
// half-written page that was buffered before the failure, and never the
// internal error text.
func TestRenderTemplateErrorIsAClean500(t *testing.T) {
	t.Parallel()

	s, logs := newRenderServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/contracts/"+renderContractID, nil)

	s.render(w, r, "contract.html", contractDataWithoutInterface())

	assert.Equal(t, http.StatusInternalServerError, w.Code,
		"a template that fails partway through must not answer 200")

	body := w.Body.String()
	assert.NotContains(t, body, "<!doctype html>",
		"the buffered page must be discarded, not written out half-rendered")
	assert.NotContains(t, body, "<html",
		"the buffered page must be discarded, not written out half-rendered")
	assert.NotContains(t, body, renderContractID,
		"content written before the failure must not reach the visitor")
	assert.NotContains(t, body, "nil pointer",
		"the internal error text must not be shown to the visitor")
	assert.NotContains(t, body, "contract.html",
		"the template name must not be shown to the visitor")
	assert.Equal(t, "internal error", strings.TrimSpace(body))
	assert.NotContains(t, w.Header().Get("Content-Type"), "text/html",
		"the error response is not a page, so it must not claim to be one")

	// The operator still gets the detail, which is the whole point of
	// logging it rather than showing it.
	assert.Contains(t, logs.String(), "rendering template",
		"the failure must be logged with the template that failed")
	assert.Contains(t, logs.String(), "contract.html")
}

// TestRenderStatusFailureAnswersInternalServerError covers the same failure
// on a page that carries a status of its own: a failed render must not keep
// the status the page asked for, or the browser would cache a broken page
// under it.
func TestRenderStatusFailureAnswersInternalServerError(t *testing.T) {
	t.Parallel()

	s, _ := newRenderServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	s.renderStatus(w, r, http.StatusNotFound, "contract.html", contractDataWithoutInterface())

	assert.Equal(t, http.StatusInternalServerError, w.Code,
		"a failed render must not answer with the status the page asked for")
	assert.Equal(t, "internal error", strings.TrimSpace(w.Body.String()))
}

// TestRenderHTMXPartialFailureIsNotAFragment covers the partial htmx swaps
// into the results table. The failure answers with the same plain error as
// any other render, not with a fragment: htmx does not swap a non-2xx
// response, so the rows already on screen stay where they are.
func TestRenderHTMXPartialFailureIsNotAFragment(t *testing.T) {
	t.Parallel()

	s, _ := newRenderServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/partials/contracts?q=vote", nil)
	r.Header.Set("HX-Request", "true")

	// The partial's template reads fields the data does not have, so the
	// execution fails after the fragment's opening tag was written.
	s.render(w, r, "contract_rows.html", struct{ Query string }{Query: "vote"})

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	body := w.Body.String()
	assert.NotContains(t, body, `id="contract-rows"`,
		"a failed partial must not be swapped into the page")
	assert.NotContains(t, body, "vote",
		"content written before the failure must not reach the visitor")
	assert.NotContains(t, w.Header().Get("Content-Type"), "text/html",
		"the error response is not a fragment, so it must not claim to be one")
	assert.Equal(t, "internal error", strings.TrimSpace(body))
}

// TestRenderSuccessIsUnaffected is the control for the failure tests above:
// the same server renders a partial it can render, so a failure test passing
// means the failure was handled, not that everything is broken.
func TestRenderSuccessIsUnaffected(t *testing.T) {
	t.Parallel()

	s, _ := newRenderServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/partials/contracts", nil)

	s.render(w, r, "contract_rows.html", indexData{})

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, w.Body.String(), `id="contract-rows"`)
}

// TestFailRendersTheErrorPage shows the error page the UI does have, for the
// failures that happen before a template is involved. It says what went
// wrong in the visitor's terms and nothing about the internal failure.
func TestFailRendersTheErrorPage(t *testing.T) {
	t.Parallel()

	s, _ := newRenderServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/contracts/"+renderContractID, nil)

	s.fail(w, r, errors.New("connection refused"))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, w.Body.String(), "Something went wrong.")
	assert.NotContains(t, w.Body.String(), "connection refused",
		"the internal error must not reach the visitor")
}
