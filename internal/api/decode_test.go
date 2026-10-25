package api_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/store"
	"github.com/soroworks/sorovault/internal/wasm/wasmtest"
)

// decodeBodyLimit is the upload cap documented in openapi.json and the
// README. The tests pin the number so the three cannot drift apart.
const decodeBodyLimit = 512 << 10

// zkvoteHash is the SHA-256 the network knows the fixture by, and therefore
// the hash ?format=ts must name as the interface version.
const zkvoteHash = "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781"

// decodeFixture loads the checked-in testnet contract — the suite's single
// real WASM — from the spec package's testdata.
func decodeFixture(t *testing.T) []byte {
	t.Helper()
	module, err := os.ReadFile("../spec/testdata/zkvote.wasm")
	require.NoError(t, err)
	return module
}

// postRaw posts body to path with an explicit Content-Type, which h.do
// cannot express: it always claims JSON.
func postRaw(t *testing.T, h *harness, path, contentType string, body []byte) (statusResult, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		h.server.URL+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)

	resp, err := h.server.Client().Do(req)
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return statusResult{StatusCode: resp.StatusCode, Header: resp.Header}, raw
}

// TestDecodeRawBodyReturnsABIWithoutStoring is the endpoint's core promise:
// the fixture's interface comes back, and neither the registry, the
// uploaded-code catalog, nor the network is touched.
func TestDecodeRawBodyReturnsABIWithoutStoring(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	resp, raw := postRaw(t, h, "/api/decode", "application/wasm", decodeFixture(t))
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	body := h.decode(t, raw)
	assert.Equal(t, "1", body["abi_version"])

	functions, ok := body["functions"].([]any)
	require.True(t, ok, "functions must be an array, never null: %s", raw)
	assert.Len(t, functions, 7, "the zkvote fixture exposes seven functions")
	names := make([]string, 0, len(functions))
	for _, fn := range functions {
		names = append(names, fn.(map[string]any)["name"].(string))
	}
	assert.Contains(t, names, "has_voted")

	page, err := h.store.ListContracts(context.Background(), store.ListFilter{})
	require.NoError(t, err)
	assert.Zero(t, page.Total, "decode must not register the contract")

	_, err = h.store.GetWasmSpec(context.Background(), "testnet", zkvoteHash)
	assert.ErrorIs(t, err, store.ErrNotFound, "decode must not catalog the module either")

	assert.Zero(t, h.fake.Calls["Network"], "decode must not reach the network")
}

// TestDecodeAcceptsMultipartFile proves the second accepted body shape.
func TestDecodeAcceptsMultipartFile(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "zkvote.wasm")
	require.NoError(t, err)
	_, err = part.Write(decodeFixture(t))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	resp, raw := postRaw(t, h, "/api/decode", mw.FormDataContentType(), buf.Bytes())
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	body := h.decode(t, raw)
	functions, ok := body["functions"].([]any)
	require.True(t, ok, "functions must be an array, never null: %s", raw)
	assert.Len(t, functions, 7, "a multipart upload must decode exactly what the raw body does")
}

// TestDecodeFormatTS proves ?format=ts swaps the ABI JSON for the generated
// TypeScript client, keyed by the hash of the uploaded bytes.
func TestDecodeFormatTS(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	resp, raw := postRaw(t, h, "/api/decode?format=ts", "application/wasm", decodeFixture(t))
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/plain")

	source := string(raw)
	assert.Contains(t, source, fmt.Sprintf("export const wasmHash = %q", zkvoteHash),
		"the client must name the hash of the bytes it was generated from")
	assert.Contains(t, source, "has_voted", "the client must declare the fixture's functions")
}

// TestDecodeRejectsOversizedBody pins the documented limit: one byte more
// is refused with 413, and the error says which limit was exceeded.
func TestDecodeRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	oversized := bytes.Repeat([]byte{0x00}, decodeBodyLimit+1)

	resp, raw := postRaw(t, h, "/api/decode", "application/wasm", oversized)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode, string(raw))

	body := h.decode(t, raw)
	assert.Contains(t, body["error"], fmt.Sprintf("%d", decodeBodyLimit),
		"a refused upload must state the limit that was exceeded")
}

func TestDecodeRejectsEmptyBody(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	resp, raw := postRaw(t, h, "/api/decode", "application/wasm", nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw))
	assert.Contains(t, h.decode(t, raw)["error"], "empty")
}

// TestDecodeRejectsNonWASMIs422 keeps malformed uploads out of the 500
// bucket: the body was well-formed, the module simply is not one.
func TestDecodeRejectsNonWASMIs422(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	resp, raw := postRaw(t, h, "/api/decode", "application/wasm",
		[]byte("definitely not a wasm module"))
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, string(raw))
	assert.Contains(t, h.decode(t, raw)["error"], "not a WebAssembly module")
}

func TestDecodeRejectsModuleWithoutSpecSection(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	module := wasmtest.Module(wasmtest.Section{Name: "producers", Body: []byte("rustc")})

	resp, raw := postRaw(t, h, "/api/decode", "application/wasm", module)
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, string(raw))
	assert.Contains(t, h.decode(t, raw)["error"], "contractspecv0")
}

func TestDecodeRejectsUnknownFormat(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	resp, raw := postRaw(t, h, "/api/decode?format=xml", "application/wasm", decodeFixture(t))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw))
	assert.Contains(t, h.decode(t, raw)["error"], "format")
}

// TestDecodeRejectsMultipartWithoutFile: a form whose fields are not a file
// must not be decoded as if the field names were a WASM module.
func TestDecodeRejectsMultipartWithoutFile(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	require.NoError(t, mw.WriteField("note", "no module here"))
	require.NoError(t, mw.Close())

	resp, raw := postRaw(t, h, "/api/decode", mw.FormDataContentType(), buf.Bytes())
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(raw))
	assert.Contains(t, h.decode(t, raw)["error"], "no file part")
}
