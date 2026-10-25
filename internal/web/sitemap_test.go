package web_test

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
	"github.com/soroworks/sorovault/internal/web"
)

type parsedSitemap struct {
	XMLName xml.Name
	URLs    []struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod"`
	} `xml:"url"`
}

func TestLinkPreviewMetadata(t *testing.T) {
	h := newHarness(t)
	h.register(t, idA)
	_, body := h.get(t, "/contracts/"+idA)
	assert.Contains(t, body, `<meta name="description" content="testnet Soroban contract with 7 functions:`)
	assert.Contains(t, body, `<meta property="og:title" content="Contract `+idA)
	assert.Contains(t, body, `<meta property="og:description" content="testnet Soroban contract with 7 functions:`)
	assert.Contains(t, body, "has_voted")
	assert.NotContains(t, body, `<meta property="og:url"`)
	assert.Less(t, strings.Index(body, `<meta name="description"`), strings.Index(body, "</head>"))

}

func TestSitemapXML(t *testing.T) {
	memory := store.NewMemory()
	memory.SetClock(func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := registry.New(stellartest.New(), spec.XDRDecoder{}, memory, registry.Options{Logger: log})
	ui, err := web.NewServer(reg, log)
	require.NoError(t, err)
	ui.SetPublicURL("https://registry.example.test")
	server := httptest.NewServer(ui.Routes())
	defer server.Close()

	for i := 0; i < 3; i++ {
		c := store.Contract{ContractID: fmt.Sprintf("C%d", i), Network: "testnet", CurrentWasmHash: fmt.Sprintf("hash-%d", i)}
		require.NoError(t, memory.Save(context.Background(), c, &model.Interface{ABIVersion: model.ABIVersion}))
	}
	resp, err := server.Client().Get(server.URL + "/sitemap.xml")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/xml")
	var sitemap parsedSitemap
	require.NoError(t, xml.NewDecoder(resp.Body).Decode(&sitemap))
	assert.Equal(t, "urlset", sitemap.XMLName.Local)
	require.Len(t, sitemap.URLs, 4)
	assert.Equal(t, "https://registry.example.test/", sitemap.URLs[0].Loc)
	assert.Equal(t, "2026-10-21T12:00:00Z", sitemap.URLs[1].LastMod)
	for _, item := range sitemap.URLs[1:] {
		assert.True(t, strings.HasPrefix(item.Loc, "https://registry.example.test/contracts/"))
	}
	resp2, err := server.Client().Get(server.URL + "/sitemap-2.xml")
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusNotFound, resp2.StatusCode)
}
