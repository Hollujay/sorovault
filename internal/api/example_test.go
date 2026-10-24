package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
)

// TestFunctionExample checks the example endpoint: the placeholder argument
// object a client can take and turn into a first request.
func TestFunctionExample(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.fake.Deploy(idA, moduleWith(t, "balance"))
	resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

	t.Run("is a valid argument object", func(t *testing.T) {
		t.Parallel()

		resp, raw := h.do(t, http.MethodGet, "/api/contracts/"+idA+"/functions/balance/example.json", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
		assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

		// The document is what the schema validates: an object whose keys
		// are the argument names, with a deterministic placeholder per
		// type.
		assert.JSONEq(t, `{"who": `+quoted(zeroAddress)+`}`, string(raw), string(raw))
	})

	t.Run("matches its schema", func(t *testing.T) {
		t.Parallel()

		_, example := h.do(t, http.MethodGet, "/api/contracts/"+idA+"/functions/balance/example.json", "")
		_, schema := h.do(t, http.MethodGet, "/api/contracts/"+idA+"/functions/balance/schema.json", "")

		assert.NoError(t, validateExample(t, schema, example),
			"the served example must validate against the served schema")
	})

	t.Run("missing function", func(t *testing.T) {
		t.Parallel()

		resp, raw := h.do(t, http.MethodGet, "/api/contracts/"+idA+"/functions/nope/example.json", "")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Contains(t, h.decode(t, raw)["error"], "nope")
	})

	t.Run("missing contract", func(t *testing.T) {
		t.Parallel()

		resp, _ := h.do(t, http.MethodGet, "/api/contracts/"+idB+"/functions/balance/example.json", "")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

// TestFunctionExampleOfAVersionedInterface checks that ?wasm_hash= selects a
// superseded interface's example, the way the function and schema endpoints
// select one.
func TestFunctionExampleOfAVersionedInterface(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	v1 := moduleWith(t, "balance")
	h.fake.Deploy(idA, v1)
	resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

	oldHash := stellartest.HashOf(v1)

	h.fake.Upgrade(idA, moduleWith(t, "total"))
	resp, raw = h.do(t, http.MethodPost, "/api/contracts/"+idA+"/refresh", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	resp, raw = h.do(t, http.MethodGet,
		"/api/contracts/"+idA+"/functions/balance/example.json?wasm_hash="+oldHash, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	assert.JSONEq(t, `{"who": `+quoted(zeroAddress)+`}`, string(raw))

	// The current interface has no balance any more.
	resp, _ = h.do(t, http.MethodGet, "/api/contracts/"+idA+"/functions/balance/example.json", "")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestRealFixtureExamplesAreServedAndValid is the end-to-end half of the
// issue's guarantee: the API serves an example for every function of the real
// testnet fixture, and each one validates against that function's schema
// fetched from the same API.
func TestRealFixtureExamplesAreServedAndValid(t *testing.T) {
	t.Parallel()

	module, err := os.ReadFile("../spec/testdata/zkvote.wasm")
	require.NoError(t, err)

	h := newHarness(t)
	h.fake.Deploy(idA, module)
	resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

	iface, err := spec.XDRDecoder{}.Decode(module)
	require.NoError(t, err)
	require.NotEmpty(t, iface.Functions)

	for _, fn := range iface.Functions {
		t.Run(fn.Name, func(t *testing.T) {
			t.Parallel()

			_, example := h.do(t, http.MethodGet,
				"/api/contracts/"+idA+"/functions/"+fn.Name+"/example.json", "")
			_, schema := h.do(t, http.MethodGet,
				"/api/contracts/"+idA+"/functions/"+fn.Name+"/schema.json", "")

			assert.NoError(t, validateExample(t, schema, example), string(example))
		})
	}
}

// zeroAddress is the all-zero public key account address, the placeholder the
// example generator writes for an Address.
const zeroAddress = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

// quoted is a JSON string literal.
func quoted(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		// json.Marshal of a string cannot fail.
		return `""`
	}
	return string(raw)
}

// validateExample validates an example document against a schema document
// with a real JSON Schema validator, so the guarantee the issue states is
// checked rather than eyeballed. The validator's own decoder reads the
// example, so numbers keep their exact value.
func validateExample(t *testing.T, schemaDoc, exampleDoc []byte) error {
	t.Helper()

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaDoc))
	require.NoError(t, err)

	c := jsonschema.NewCompiler()
	require.NoError(t, c.AddResource("schema.json", doc))
	compiled, err := c.Compile("schema.json")
	require.NoError(t, err)

	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(exampleDoc))
	require.NoError(t, err)
	return compiled.Validate(instance)
}
