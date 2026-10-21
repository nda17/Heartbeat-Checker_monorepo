package seosmoke

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePageCollectsMetadataLinksTitleAndJSONLD(t *testing.T) {
	page, err := ParsePage([]byte(`<html><head><title>  Heartbeat  Checker </title><meta name="description" content="desc"><link rel="canonical alternate" href="/x"><script type="application/ld+json">{"@type":"WebSite"}</script></head></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Heartbeat Checker" || page.Meta["description"][0] != "desc" || len(page.links("canonical")) != 1 || len(page.JSONLD) != 1 {
		t.Fatalf("parsed page contract: %#v", page)
	}
	if _, err = ParsePage([]byte(`<script type="application/ld+json">{broken}</script>`)); err == nil {
		t.Fatal("malformed JSON-LD accepted")
	}
}

func TestCheckAcceptsFullPublicAndPrivateSEOHTTPContract(t *testing.T) {
	fixture := newSEOFixture(t, "")
	smoke := fixture.smoke(t, true)
	if err := smoke.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRejectsRouteMetadataRobotsSitemapImageAndResponseBounds(t *testing.T) {
	for _, scenario := range []string{"canonical", "duplicate-title", "private-robots", "api-header", "sitemap-extra", "og-dimensions", "oversized-html", "release-missing"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSEOFixture(t, scenario)
			s := f.smoke(t, scenario == "api-header")
			if err := s.Check(context.Background()); err == nil {
				t.Fatal("invalid rendered SEO contract accepted")
			}
		})
	}
}

func TestNewAndRunValidateHTTPOriginFlagsAndErrors(t *testing.T) {
	for _, invalid := range []string{"ftp://example.test", "https://example.test/path", "//example.test", ""} {
		if _, err := New(invalid, false); err == nil {
			t.Fatalf("invalid base URL accepted: %q", invalid)
		}
	}
	var out, stderr bytes.Buffer
	if status := Run(context.Background(), []string{"--unknown"}, &out, &stderr); status != 2 {
		t.Fatalf("bad flag status=%d", status)
	}
	if status := Run(context.Background(), []string{"--base-url", "https://example.test/path"}, &out, &stderr); status != 1 {
		t.Fatalf("invalid URL status=%d", status)
	}
}

type seoFixture struct {
	release  string
	scenario string
}

func newSEOFixture(t *testing.T, scenario string) *seoFixture {
	t.Helper()
	f := &seoFixture{scenario: scenario}
	release := filepath.Join(t.TempDir(), "release.json")
	if scenario == "release-missing" {
		release = filepath.Join(t.TempDir(), "absent.json")
	} else if err := os.WriteFile(release, []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	f.release = release
	return f
}

type seoClient struct{ scenario string }

func (c seoClient) Do(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	p := req.URL.Path
	scenario := c.scenario
	(func() {
		switch {
		case contains(PublicRoutes, p) || p == "/og" || p == "/robots.txt" || p == "/sitemap.xml" || p == "/this-page-intentionally-does-not-exist" || contains(PrivateRoutes, p) || contains(SystemRoutes, p):
			if p == "/og" {
				w.Header().Set("Content-Type", "image/png")
				b := make([]byte, 24)
				copy(b, []byte("\x89PNG\r\n\x1a\n"))
				b[18], b[19] = 4, 176
				b[22], b[23] = 2, 118
				if scenario == "og-dimensions" {
					b[19] = 1
				}
				_, _ = w.Write(b)
				return
			}
			if p == "/robots.txt" {
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("X-Robots-Tag", "index,follow")
				_, _ = w.Write([]byte("User-agent: *\nAllow: /\nDisallow: /api/\nDisallow: /downloads/\nDisallow: /.well-known/\nSitemap: https://heartbeat-checker.ru/sitemap.xml\n"))
				return
			}
			if p == "/sitemap.xml" {
				w.Header().Set("Content-Type", "application/xml")
				w.Header().Set("X-Robots-Tag", "index,follow")
				locs := []string{}
				for _, route := range PublicRoutes {
					locs = append(locs, "<loc>"+publicPageURL(route)+"</loc>")
				}
				if scenario == "sitemap-extra" {
					locs = append(locs, "<loc>https://heartbeat-checker.ru/nope</loc>")
				}
				_, _ = fmt.Fprintf(w, "<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">%s</urlset>", strings.Join(locs, ""))
				return
			}
			if p == "/this-page-intentionally-does-not-exist" {
				w.Header().Set("X-Robots-Tag", "noindex,nofollow")
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`<html><meta name="robots" content="noindex,nofollow"></html>`))
				return
			}
			if contains(PrivateRoutes, p) {
				robots := "noindex,nofollow"
				if scenario == "private-robots" {
					robots = "index,follow"
				}
				w.Header().Set("X-Robots-Tag", robots)
				_, _ = w.Write([]byte(`<html><meta name="robots" content="` + robots + `"></html>`))
				return
			}
			if contains(SystemRoutes, p) {
				w.Header().Set("Content-Type", "application/json")
				if scenario != "api-header" {
					w.Header().Set("X-Robots-Tag", "noindex,nofollow")
				}
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
			w.Header().Set("X-Robots-Tag", "index,follow")
			title := strings.TrimPrefix(p, "/")
			if title == "" {
				title = "home"
			}
			if scenario == "duplicate-title" && p == "/android" {
				title = "home"
			}
			url := publicPageURL(p)
			canonical := url
			if scenario == "canonical" && p == "/android" {
				canonical += "/wrong"
			}
			graphType := "WebPage"
			if p == "/contacts" {
				graphType = "ContactPage"
			}
			graph := fmt.Sprintf(`{"@context":"https://schema.org","@graph":[{"@type":"WebSite"},{"@type":"%s"}]}`, graphType)
			robots := "index,follow"
			description := "Description " + title
			html := fmt.Sprintf(`<html><head><title>%s</title><meta name="description" content="%s"><meta name="robots" content="%s"><meta property="og:url" content="%s"><meta property="og:title" content="%s"><meta property="og:description" content="%s"><meta property="og:type" content="website"><meta property="og:locale" content="ru_RU"><meta property="og:site_name" content="Heartbeat Checker"><meta property="og:image" content="https://heartbeat-checker.ru/og"><meta property="og:image:width" content="1200"><meta property="og:image:height" content="630"><meta name="twitter:card" content="summary_large_image"><link rel="canonical" href="%s"><script type="application/ld+json">%s</script></head><body>ok</body></html>`, title, description, robots, url, title, description, canonical, graph)
			if scenario == "oversized-html" && p == "/" {
				html += strings.Repeat("x", maxResponseBytes+1)
			}
			_, _ = w.Write([]byte(html))
		default:
			if p == "/api/v1/info" {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Robots-Tag", "noindex,nofollow")
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
			w.WriteHeader(404)
		}
	})()
	return &http.Response{StatusCode: w.Code, Header: w.Header(), Body: io.NopCloser(bytes.NewReader(w.Body.Bytes())), Request: req}, nil
}
func (f *seoFixture) smoke(t *testing.T, require bool) *Smoke {
	t.Helper()
	s, err := New("http://127.0.0.1:3139", require)
	if err != nil {
		t.Fatal(err)
	}
	s.ReleasePath = f.release
	s.Client = seoClient{scenario: f.scenario}
	return s
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
