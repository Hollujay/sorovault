package web

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/soroworks/sorovault/internal/store"
)

const (
	// Each query is bounded by the store limit; each sitemap by the XML limit.
	sitemapPageSize     = 200
	contractsPerSitemap = 49999
)

type sitemapEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type sitemapURLSet struct {
	XMLName xml.Name       `xml:"urlset"`
	XMLNS   string         `xml:"xmlns,attr"`
	URLs    []sitemapEntry `xml:"url"`
}

type sitemapIndex struct {
	XMLName  xml.Name       `xml:"sitemapindex"`
	XMLNS    string         `xml:"xmlns,attr"`
	Sitemaps []sitemapEntry `xml:"sitemap"`
}

func sitemapDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) {
	if s.publicURL == "" {
		http.NotFound(w, r)
		return
	}
	s.writeSitemap(w, r, 0, true)
}

func (s *Server) sitemapPage(w http.ResponseWriter, r *http.Request) {
	if s.publicURL == "" {
		http.NotFound(w, r)
		return
	}
	raw := chi.URLParam(r, "page")
	page, err := strconv.Atoi(raw)
	if err != nil || page < 1 {
		http.NotFound(w, r)
		return
	}
	s.writeSitemap(w, r, page, false)
}

func (s *Server) writeSitemap(w http.ResponseWriter, r *http.Request, page int, root bool) {
	first, err := s.reg.Store().ListContracts(r.Context(), store.ListFilter{Limit: 1})
	if err != nil {
		s.failSitemap(w, r, err)
		return
	}
	pages := (first.Total + contractsPerSitemap - 1) / contractsPerSitemap
	if pages == 0 {
		pages = 1
	}
	if root && pages > 1 {
		idx := sitemapIndex{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
		for n := 1; n <= pages; n++ {
			idx.Sitemaps = append(idx.Sitemaps, sitemapEntry{Loc: s.publicURL + "/sitemap-" + strconv.Itoa(n) + ".xml"})
		}
		s.sendSitemap(w, idx)
		return
	}
	if !root && (pages == 1 || page > pages) {
		http.NotFound(w, r)
		return
	}
	if root {
		page = 1
	}
	// The store caps each ListContracts call at 200 rows. Walk the selected
	// sitemap page in bounded queries instead of assuming one call returns 50k.
	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/contractsPerSitemap {
		http.NotFound(w, r)
		return
	}
	start := (page - 1) * contractsPerSitemap
	set := sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	if page == 1 {
		set.URLs = append(set.URLs, sitemapEntry{Loc: s.publicURL + "/"})
	}
	for offset := start; offset < first.Total && offset-start < contractsPerSitemap; {
		limit := sitemapPageSize
		if remaining := contractsPerSitemap - (offset - start); remaining < limit {
			limit = remaining
		}
		result, err := s.reg.Store().ListContracts(r.Context(), store.ListFilter{Limit: limit, Offset: offset})
		if err != nil {
			s.failSitemap(w, r, err)
			return
		}
		if len(result.Contracts) == 0 {
			break
		}
		for _, c := range result.Contracts {
			link := s.publicURL + "/contracts/" + url.PathEscape(c.ContractID) + "?network=" + url.QueryEscape(c.Network)
			set.URLs = append(set.URLs, sitemapEntry{Loc: link, LastMod: sitemapDate(c.LastRefreshed)})
		}
		offset += len(result.Contracts)
	}
	s.sendSitemap(w, set)
}

func (s *Server) sendSitemap(w http.ResponseWriter, data any) {
	body, err := xml.MarshalIndent(data, "", "  ")
	if err != nil {
		http.Error(w, "sitemap generation failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(body)
}

func (s *Server) failSitemap(w http.ResponseWriter, r *http.Request, err error) {
	// Do not expose database errors in a public XML response.
	s.log.ErrorContext(r.Context(), "sitemap listing failed", "error", err)
	http.Error(w, "sitemap unavailable", http.StatusInternalServerError)
}
