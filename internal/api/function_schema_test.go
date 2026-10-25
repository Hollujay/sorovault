package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/wasm"
	"github.com/soroworks/sorovault/internal/wasm/wasmtest"
)

// moduleWithNoArgs builds a module whose contract spec declares a function
// that takes no arguments, so the endpoint has an empty argument list to
// describe.
func moduleWithNoArgs(t testing.TB, fnName string) []byte {
	t.Helper()

	entry := xdr.ScSpecEntry{
		Kind: xdr.ScSpecEntryKindScSpecEntryFunctionV0,
		FunctionV0: &xdr.ScSpecFunctionV0{
			Name:    xdr.ScSymbol(fnName),
			Outputs: []xdr.ScSpecTypeDef{{Type: xdr.ScSpecTypeScSpecTypeU32}},
		},
	}
	body, err := entry.MarshalBinary()
	require.NoError(t, err)

	return wasmtest.Module(wasmtest.Section{Name: wasm.SectionContractSpec, Body: body})
}

// compileFunctionSchema compiles a served schema with a JSON Schema
// validator, so the endpoint test proves the document is a valid 2020-12
// schema and not merely well-formed JSON.
func compileFunctionSchema(t *testing.T, raw []byte) *jsonschema.Schema {
	t.Helper()

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)

	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("schema.json", doc))
	schema, err := compiler.Compile("schema.json")
	require.NoError(t, err)
	return schema
}

func validateFunctionArguments(t *testing.T, schema *jsonschema.Schema, arguments string) error {
	t.Helper()

	value, err := jsonschema.UnmarshalJSON(strings.NewReader(arguments))
	require.NoError(t, err)
	return schema.Validate(value)
}

func TestFunctionSchema(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	module := moduleWith(t, "balance")
	h.fake.Deploy(idA, module)
	resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

	const address = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

	t.Run("known function", func(t *testing.T) {
		resp, raw := h.do(t, http.MethodGet, "/api/contracts/"+idA+"/functions/balance/schema.json", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
		assert.Equal(t, "application/schema+json", resp.Header.Get("Content-Type"))

		var schema struct {
			Schema               string `json:"$schema"`
			ID                   string `json:"$id"`
			Title                string `json:"title"`
			Type                 string `json:"type"`
			Required             []string
			AdditionalProperties bool `json:"additionalProperties"`
			Properties           map[string]struct {
				Type    string `json:"type"`
				Pattern string `json:"pattern"`
			} `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(raw, &schema), "response was not JSON: %s", raw)

		assert.Equal(t, "https://json-schema.org/draft/2020-12/schema", schema.Schema)
		assert.Equal(t, "balance arguments", schema.Title)
		assert.Equal(t, "object", schema.Type)
		assert.Equal(t, []string{"who"}, schema.Required)
		assert.False(t, schema.AdditionalProperties)
		assert.Equal(t, "string", schema.Properties["who"].Type)
		assert.Equal(t, "^(?:C|G)[A-Z2-7]{55}$", schema.Properties["who"].Pattern)
		assert.Equal(t,
			"urn:sorovault:contract:"+idA+":testnet:"+stellartest.HashOf(module)+":balance",
			schema.ID)

		compiled := compileFunctionSchema(t, raw)
		require.NoError(t, validateFunctionArguments(t, compiled, `{"who":"`+address+`"}`))
		assert.Error(t, validateFunctionArguments(t, compiled, `{}`),
			"the only argument is required")
	})

	t.Run("unknown function", func(t *testing.T) {
		resp, raw := h.do(t, http.MethodGet, "/api/contracts/"+idA+"/functions/nope/schema.json", "")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(raw))
		assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

		got := h.decode(t, raw)
		assert.Contains(t, got["error"], `no function "nope" in contract `+idA)
	})

	t.Run("unknown contract", func(t *testing.T) {
		resp, raw := h.do(t, http.MethodGet, "/api/contracts/"+idB+"/functions/balance/schema.json", "")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, string(raw))
		assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

		got := h.decode(t, raw)
		assert.NotEmpty(t, got["error"])
		assert.Contains(t, got["error"], idB)
	})
}

// TestFunctionSchemaWithNoArguments pins the empty argument list: the schema
// still describes an object, but one that accepts no properties.
func TestFunctionSchemaWithNoArguments(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.fake.Deploy(idA, moduleWithNoArgs(t, "ping"))
	resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

	resp, raw = h.do(t, http.MethodGet, "/api/contracts/"+idA+"/functions/ping/schema.json", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	assert.Equal(t, "application/schema+json", resp.Header.Get("Content-Type"))

	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema), "response was not JSON: %s", raw)

	assert.Equal(t, "https://json-schema.org/draft/2020-12/schema", schema["$schema"])
	assert.Equal(t, "ping arguments", schema["title"])
	assert.Equal(t, "object", schema["type"])
	assert.Equal(t, false, schema["additionalProperties"])
	assert.Equal(t, map[string]any{}, schema["properties"])
	_, required := schema["required"]
	assert.False(t, required, "an empty argument list has no required properties")

	compiled := compileFunctionSchema(t, raw)
	require.NoError(t, validateFunctionArguments(t, compiled, `{}`))
	assert.Error(t, validateFunctionArguments(t, compiled, `{"unexpected":1}`),
		"the schema must not accept undeclared arguments")
}
