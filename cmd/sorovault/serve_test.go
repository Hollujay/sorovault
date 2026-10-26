package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/config"
	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

func testRouter(t *testing.T) http.Handler {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := registry.New(stellartest.New(), spec.XDRDecoder{}, store.NewMemory(), registry.Options{Logger: log})

	handler, err := newRouter(reg, log, nil, nil)
	require.NoError(t, err)
	return handler
}

// TestRouterMountsEverything checks that the API, the browse UI and the
// health endpoint all coexist under one handler, which is the wiring most
// likely to break when a route is added.
func TestRouterMountsEverything(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(testRouter(t))
	t.Cleanup(srv.Close)

	tests := []struct {
		path        string
		wantStatus  int
		wantContent string
	}{
		{"/healthz", http.StatusOK, "application/json"},
		{"/api/contracts", http.StatusOK, "application/json"},
		{"/", http.StatusOK, "text/html"},
		{"/static/app.css", http.StatusOK, "text/css"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			resp, err := srv.Client().Get(srv.URL + tt.path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Contains(t, resp.Header.Get("Content-Type"), tt.wantContent)
		})
	}
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(testRouter(t))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/healthz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	var got map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))

	assert.Equal(t, "ok", got["status"])
	assert.NotEmpty(t, got["version"])
}

// TestRootCmdWiring guards the command tree against a command being added
// without being registered.
// TestHealthzIsNotCompressed keeps the liveness endpoint cheap and plain,
// even for a client that would accept a compressed body everywhere else.
func TestHealthzIsNotCompressed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(testRouter(t))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/healthz", nil)
	require.NoError(t, err)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Empty(t, resp.Header.Get("Content-Encoding"))
}

func TestRootCmdWiring(t *testing.T) {
	t.Parallel()

	root := newRootCmd()

	got := map[string]bool{}
	for _, c := range root.Commands() {
		got[c.Name()] = true
	}

	for _, want := range []string{"add", "remove", "list", "get", "refresh", "export", "import", "serve", "migrate", "doctor"} {
		assert.True(t, got[want], "command %q should be registered", want)
	}
}

// TestReadOnlyWiring proves serve's read-only flag reaches the mounted
// router: with it on, the API's writes refuse with 405 while reads and the
// health endpoints keep answering.
func TestReadOnlyWiring(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := registry.New(stellartest.New(), spec.XDRDecoder{}, store.NewMemory(), registry.Options{Logger: log})

	handler, err := newRouter(reg, log, nil, &config.Config{ReadOnly: true})
	require.NoError(t, err)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/api/contracts")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode, "reads stay public in read-only mode")

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/contracts", bytes.NewReader([]byte(`{}`)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err = srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, string(raw))
	assert.Contains(t, string(raw), "read-only")
}

// TestWriteAuthWiring proves serve's API_TOKENS wiring reaches the mounted
// router: with tokens configured, the API's writes need a bearer
// credential while reads stay public.
func TestWriteAuthWiring(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := registry.New(stellartest.New(), spec.XDRDecoder{}, store.NewMemory(), registry.Options{Logger: log})

	handler, err := newRouter(reg, log, nil, &config.Config{APITokens: []string{"0123456789abcdef-wired"}})
	require.NoError(t, err)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/api/contracts")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode, "reads stay public under token auth")

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/contracts", bytes.NewReader([]byte(`{}`)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err = srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw))

	req.Header.Set("Authorization", "Bearer 0123456789abcdef-wired")
	resp, err = srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	raw, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw),
		"the credential opens the route; {} is still an empty contract ID")
}

func TestPrintFunction(t *testing.T) {
	t.Parallel()

	fn := model.Function{
		Name: "transfer",
		Doc:  "Move value.\nRequires auth.",
		Inputs: []model.Param{
			{Name: "from", Type: model.Scalar(model.KindAddress)},
			{Name: "amount", Type: model.Scalar(model.KindI128)},
		},
		Outputs: []model.Type{model.Scalar(model.KindVoid)},
	}

	var buf bytes.Buffer
	printFunction(&buf, fn)
	out := buf.String()

	assert.Contains(t, out, "fn transfer(from: Address, amount: i128) -> ()")
	assert.Contains(t, out, "// Move value.")
	assert.Contains(t, out, "// Requires auth.")
	assert.Contains(t, out, "returns: ()")
}

func TestPrintFunctionNoOutputs(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printFunction(&buf, model.Function{Name: "reset"})

	assert.Contains(t, buf.String(), "returns: ()",
		"a function with no declared output returns unit")
}

func TestPrintRegisterResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result *registry.Result
		want   string
	}{
		{
			name: "new contract",
			result: &registry.Result{
				Contract: store.Contract{ContractID: "C1", Network: "testnet", CurrentWasmHash: "abc"},
				Created:  true, Changed: true,
			},
			want: "registered",
		},
		{
			name: "upgraded contract",
			result: &registry.Result{
				Contract: store.Contract{ContractID: "C1", Network: "testnet", CurrentWasmHash: "def"},
				Changed:  true,
			},
			want: "upgraded",
		},
		{
			name: "unchanged contract",
			result: &registry.Result{
				Contract: store.Contract{ContractID: "C1", Network: "testnet", CurrentWasmHash: "abc"},
			},
			want: "already current",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			printRegisterResult(&buf, tt.result)
			assert.Contains(t, buf.String(), tt.want)
		})
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "abc", truncate("abc", 5))
	assert.Equal(t, "abc", truncate("abc", 3))
	assert.Equal(t, "ab", truncate("abc", 2))
	assert.Equal(t, "", truncate("abc", 0))
}
