package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/api"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

// fetchSpec reads the OpenAPI document the server actually serves.
func fetchSpec(t *testing.T) map[string]any {
	t.Helper()
	h := newHarness(t)
	resp, raw := h.do(t, http.MethodGet, "/api/openapi.json", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc), "openapi.json must be valid JSON")
	return doc
}

// TestOpenAPICoversEveryRoute keeps the document honest: every route the API
// serves is documented with its method, and every documented /api operation
// exists.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	t.Parallel()

	paths := fetchSpec(t)["paths"].(map[string]any)

	documented := map[string]bool{}
	for path, item := range paths {
		if !strings.HasPrefix(path, "/api/") {
			continue // /healthz is mounted outside the API router
		}
		for method := range item.(map[string]any) {
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := registry.New(stellartest.New(), spec.XDRDecoder{}, store.NewMemory(), registry.Options{Logger: log})
	served := map[string]bool{}
	err := chi.Walk(api.NewServer(reg, log).Routes(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[method+" /api"+route] = true
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, sortedKeys(served), sortedKeys(documented),
		"openapi.json and the router disagree; document new routes in internal/api/openapi.json")
}

// TestOpenAPIRefsResolve catches a typo in a $ref, which most tooling would
// otherwise only report when someone generates a client.
func TestOpenAPIRefsResolve(t *testing.T) {
	t.Parallel()

	doc := fetchSpec(t)
	components := doc["components"].(map[string]any)

	var refs []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				if k == "$ref" {
					refs = append(refs, child.(string))
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(doc)
	require.NotEmpty(t, refs)

	for _, ref := range refs {
		parts := strings.Split(strings.TrimPrefix(ref, "#/components/"), "/")
		require.Len(t, parts, 2, "unexpected $ref shape %q", ref)
		section, ok := components[parts[0]].(map[string]any)
		require.True(t, ok, "%s: no components.%s", ref, parts[0])
		_, ok = section[parts[1]]
		assert.True(t, ok, "%s does not resolve", ref)
	}
}

func TestOpenAPIDocumentsTheABIKinds(t *testing.T) {
	t.Parallel()

	kinds := fetchSpec(t)["components"].(map[string]any)["schemas"].(map[string]any)["Type"].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)["enum"].([]any)
	var got []string
	for _, k := range kinds {
		got = append(got, k.(string))
	}
	// Every kind the model can emit must be listed, or schema validation of
	// a real response would fail.
	for _, k := range []string{"u32", "i128", "address", "option", "vec", "map", "result", "tuple", "bytes_n", "udt", "muxed_address"} {
		assert.Contains(t, got, k)
	}
}

// TestOpenAPIDocumentsTheDownloadParameter proves the parameter that turns a
// response into a file is documented, not just the route that accepts it.
//
// The routes test above cannot catch this: a document that lists the route
// but not the parameter is still a valid OpenAPI document.
func TestOpenAPIDocumentsTheDownloadParameter(t *testing.T) {
	t.Parallel()

	paths := fetchSpec(t)["paths"].(map[string]any)

	for _, path := range []string{"/api/contracts/{id}", "/api/contracts/{id}/client.ts"} {
		t.Run(path, func(t *testing.T) {
			operation := paths[path].(map[string]any)["get"].(map[string]any)
			params := operation["parameters"].([]any)

			var found bool
			for _, p := range params {
				param := p.(map[string]any)
				if param["name"] == "download" {
					found = true
					assert.Equal(t, "query", param["in"], "the parameter is a query parameter")
					assert.NotEmpty(t, param["description"], "the parameter should say what it does")
				}
			}
			assert.True(t, found, "openapi.json must document the download parameter of "+path)
		})
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
