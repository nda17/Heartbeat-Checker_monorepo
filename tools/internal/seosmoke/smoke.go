// Package seosmoke validates the site's rendered SEO contracts over HTTP.
package seosmoke

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const Origin = "https://heartbeat-checker.ru"

var PublicRoutes = []string{"/", "/android", "/contacts", "/terms", "/privacy", "/cookies"}
var PrivateRoutes = []string{"/login", "/register", "/forgot-password", "/reset-password", "/verify-email", "/app", "/app/settings", "/app/billing", "/app/admin"}
var SystemRoutes = []string{"/api/v1/info"}

const maxResponseBytes = 2 * 1024 * 1024
const successMessage = "SEO HTTP smoke passed for six public pages, private noindex routes, robots, sitemap and 404."

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}
type Smoke struct {
	BaseURL          string
	RequireAPIHeader bool
	ReleasePath      string
	Client           HTTPClient
}
type Page struct {
	Title  string
	Meta   map[string][]string
	Links  []map[string]string
	JSONLD []any
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("seo-smoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	base := flags.String("base-url", "http://127.0.0.1:3139", "HTTP(S) origin to check")
	requireHeader := flags.Bool("require-api-header", false, "require edge X-Robots-Tag on API routes")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "seo-smoke accepts flags only")
		return 2
	}
	smoke, err := New(*base, *requireHeader)
	if err == nil {
		err = smoke.Check(ctx)
	}
	if err != nil {
		fmt.Fprintf(stderr, "SEO smoke failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, successMessage)
	return 0
}
func New(baseURL string, requireAPIHeader bool) (*Smoke, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("base URL must be an HTTP(S) origin")
	}
	return &Smoke{BaseURL: strings.TrimRight(baseURL, "/"), RequireAPIHeader: requireAPIHeader, Client: &http.Client{Timeout: 15 * time.Second}, ReleasePath: findReleasePath()}, nil
}
func findReleasePath() string {
	directory, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		for _, candidate := range []string{filepath.Join(directory, "apps", "web", "src", "shared", "android", "release.json"), filepath.Join(directory, "src", "shared", "android", "release.json")} {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
		directory = parent
	}
}
func ParsePage(body []byte) (Page, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return Page{}, err
	}
	page := Page{Meta: map[string][]string{}}
	titles := []string{}
	var walk func(*html.Node) error
	walk = func(node *html.Node) error {
		if node.Type == html.ElementNode {
			attributes := map[string]string{}
			for _, a := range node.Attr {
				attributes[a.Key] = a.Val
			}
			switch node.Data {
			case "title":
				titles = append(titles, nodeText(node))
			case "meta":
				key := attributes["property"]
				if key == "" {
					key = attributes["name"]
				}
				if key != "" {
					key = strings.ToLower(key)
					page.Meta[key] = append(page.Meta[key], attributes["content"])
				}
			case "link":
				page.Links = append(page.Links, attributes)
			case "script":
				if strings.ToLower(attributes["type"]) == "application/ld+json" {
					var data any
					if json.Unmarshal([]byte(strings.TrimSpace(nodeText(node))), &data) != nil {
						return errors.New("invalid JSON-LD script")
					}
					page.JSONLD = append(page.JSONLD, data)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err = walk(root); err != nil {
		return Page{}, err
	}
	page.Title = strings.Join(strings.Fields(strings.Join(titles, " ")), " ")
	return page, nil
}
func nodeText(node *html.Node) string {
	var result strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			result.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return result.String()
}
func (p Page) one(key string) (string, error) {
	values := p.Meta[strings.ToLower(key)]
	if len(values) != 1 {
		return "", fmt.Errorf("expected one %s metadata value, found %v", key, values)
	}
	return values[0], nil
}
func (p Page) links(rel string) []string {
	var values []string
	for _, link := range p.Links {
		for _, value := range strings.Fields(strings.ToLower(link["rel"])) {
			if value == rel {
				values = append(values, link["href"])
				break
			}
		}
	}
	return values
}
func publicPageURL(path string) string {
	if path == "/" {
		return Origin
	}
	return Origin + path
}
func (s *Smoke) get(ctx context.Context, path string) (int, http.Header, []byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, s.BaseURL+path, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("User-Agent", "HeartbeatSEOHealthCheck/1.0")
	response, err := s.Client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s HTTP request failed", path)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s HTTP response read failed", path)
	}
	return response.StatusCode, response.Header, body, nil
}
func (s *Smoke) Check(ctx context.Context) error {
	releaseBody, err := os.ReadFile(s.ReleasePath)
	if err != nil {
		return errors.New("Android release manifest is unavailable")
	}
	var release any
	if json.Unmarshal(releaseBody, &release) != nil {
		return errors.New("Android release manifest is invalid JSON")
	}
	titles, descriptions := map[string]bool{}, map[string]bool{}
	for _, path := range PublicRoutes {
		status, headers, body, err := s.get(ctx, path)
		if err != nil {
			return err
		}
		if status != 200 {
			return fmt.Errorf("%s returned HTTP %d", path, status)
		}
		if len(body) > maxResponseBytes {
			return fmt.Errorf("%s HTML exceeds 2 MiB", path)
		}
		doc, err := ParsePage(body)
		if err != nil {
			return err
		}
		title := doc.Title
		description, err := doc.one("description")
		if err != nil {
			return err
		}
		titles[title] = true
		descriptions[description] = true
		if strings.TrimSpace(title) == "" {
			return fmt.Errorf("%s has an empty title", path)
		}
		if strings.TrimSpace(description) == "" {
			return fmt.Errorf("%s has an empty description", path)
		}
		expectedURL := publicPageURL(path)
		canonical := doc.links("canonical")
		if len(canonical) != 1 || canonical[0] != expectedURL {
			return fmt.Errorf("%s canonical is %v, expected %s", path, canonical, expectedURL)
		}
		expected := []struct{ key, value, message string }{{"robots", "index,follow", "must render robots index,follow"}, {"og:url", expectedURL, "OpenGraph URL differs from canonical"}, {"og:title", title, "OpenGraph title differs from HTML title"}, {"og:description", description, "OpenGraph description differs from page description"}, {"og:type", "website", "OpenGraph type must be website"}, {"og:locale", "ru_RU", "OpenGraph locale must be ru_RU"}, {"og:site_name", "Heartbeat Checker", "OpenGraph site name is incorrect"}, {"og:image", Origin + "/og", "uses unexpected OpenGraph image"}, {"og:image:width", "1200", "OpenGraph image dimensions must be 1200x630"}, {"og:image:height", "630", "OpenGraph image dimensions must be 1200x630"}, {"twitter:card", "summary_large_image", "Twitter card must use summary_large_image"}}
		for _, entry := range expected {
			value, err := doc.one(entry.key)
			if err != nil {
				return err
			}
			if entry.key == "robots" {
				value = strings.ToLower(strings.ReplaceAll(value, " ", ""))
			}
			if value != entry.value {
				return fmt.Errorf("%s %s", path, entry.message)
			}
		}
		if strings.ToLower(strings.ReplaceAll(headers.Get("X-Robots-Tag"), " ", "")) != "index,follow" {
			return fmt.Errorf("%s must send X-Robots-Tag index,follow", path)
		}
		if err = s.checkJSONLD(path, doc, release != nil); err != nil {
			return err
		}
	}
	if len(titles) != len(PublicRoutes) {
		return errors.New("public pages must have distinct titles")
	}
	if len(descriptions) != len(PublicRoutes) {
		return errors.New("public pages must have distinct descriptions")
	}
	for _, check := range []func(context.Context) error{s.checkOGImage, s.checkPrivateRoutes, s.checkRobotsAndSitemap, s.check404} {
		if err = check(ctx); err != nil {
			return err
		}
	}
	return nil
}
func (s *Smoke) checkJSONLD(path string, doc Page, released bool) error {
	if len(doc.JSONLD) != 1 {
		return fmt.Errorf("%s must render one JSON-LD graph", path)
	}
	graph, ok := doc.JSONLD[0].(map[string]any)
	if !ok || graph["@context"] != "https://schema.org" {
		return fmt.Errorf("%s has unsafe or unexpected JSON-LD context", path)
	}
	nodes, ok := graph["@graph"].([]any)
	if !ok {
		return fmt.Errorf("%s JSON-LD must use an @graph array", path)
	}
	types := map[string]bool{}
	for _, raw := range nodes {
		if node, ok := raw.(map[string]any); ok {
			if kind, ok := node["@type"].(string); ok {
				types[kind] = true
			}
		}
	}
	if !types["WebSite"] || (!types["WebPage"] && !types["ContactPage"]) {
		return fmt.Errorf("%s JSON-LD must describe WebSite and a page", path)
	}
	if path == "/contacts" && !types["ContactPage"] {
		return errors.New("contacts JSON-LD must describe a ContactPage")
	}
	if path == "/android" && types["SoftwareApplication"] != released {
		if released {
			return errors.New("released Android page should describe its SoftwareApplication")
		}
		return errors.New("Android SoftwareApplication requires a published release")
	}
	forbidden := map[string]bool{}
	collectForbiddenKeys(graph, forbidden)
	if len(forbidden) > 0 {
		return fmt.Errorf("%s JSON-LD contains unsupported rating/review data", path)
	}
	return nil
}
func collectForbiddenKeys(value any, found map[string]bool) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			switch strings.ToLower(key) {
			case "rating", "ratingvalue", "aggregaterating", "reviewcount", "review":
				found[key] = true
			}
			collectForbiddenKeys(child, found)
		}
	case []any:
		for _, child := range v {
			collectForbiddenKeys(child, found)
		}
	}
}
func (s *Smoke) checkOGImage(ctx context.Context) error {
	status, headers, body, err := s.get(ctx, "/og")
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("OpenGraph image returned HTTP %d", status)
	}
	if mediaType(headers) != "image/png" {
		return errors.New("OpenGraph image has the wrong content type")
	}
	if len(body) < 24 || !bytes.Equal(body[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return errors.New("OpenGraph image is not a valid PNG")
	}
	width, height := binary.BigEndian.Uint32(body[16:20]), binary.BigEndian.Uint32(body[20:24])
	if width != 1200 || height != 630 {
		return fmt.Errorf("OpenGraph image dimensions are (%d, %d)", width, height)
	}
	return nil
}
func directives(value string) map[string]bool {
	tokens := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			tokens[part] = true
		}
	}
	return tokens
}
func pageRobots(doc Page) map[string]bool {
	tokens := map[string]bool{}
	for _, value := range doc.Meta["robots"] {
		for token := range directives(value) {
			tokens[token] = true
		}
	}
	return tokens
}
func blocked(tokens map[string]bool) bool { return tokens["noindex"] && tokens["nofollow"] }
func mediaType(headers http.Header) string {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(headers.Get("Content-Type"), ";", 2)[0]))
}
func (s *Smoke) checkPrivateRoutes(ctx context.Context) error {
	for _, path := range PrivateRoutes {
		status, headers, body, err := s.get(ctx, path)
		if err != nil {
			return err
		}
		if status != 200 && status != 404 {
			return fmt.Errorf("private/system route %s returned HTTP %d", path, status)
		}
		doc, err := ParsePage(body)
		if err != nil {
			return err
		}
		if !blocked(pageRobots(doc)) {
			return fmt.Errorf("%s must render noindex/nofollow", path)
		}
		if !blocked(directives(headers.Get("X-Robots-Tag"))) {
			return fmt.Errorf("%s must send X-Robots-Tag noindex/nofollow", path)
		}
	}
	for _, path := range SystemRoutes {
		status, headers, body, err := s.get(ctx, path)
		if err != nil {
			return err
		}
		if status < 200 || status >= 600 {
			return fmt.Errorf("system route %s did not return an HTTP response", path)
		}
		robots := directives(headers.Get("X-Robots-Tag"))
		if mediaType(headers) == "text/html" {
			doc, err := ParsePage(body)
			if err != nil {
				return err
			}
			tokens := pageRobots(doc)
			for token := range robots {
				tokens[token] = true
			}
			if !blocked(tokens) {
				return fmt.Errorf("HTML response from %s must remain noindex,nofollow", path)
			}
		} else if bytes.Contains(bytes.ToLower(body), []byte("<html")) {
			return fmt.Errorf("system route %s must not render public HTML with noindex metadata absent", path)
		}
		if s.RequireAPIHeader || len(robots) > 0 {
			if !blocked(robots) {
				return fmt.Errorf("%s must send X-Robots-Tag noindex,nofollow", path)
			}
		}
	}
	return nil
}
func expectIndexFollowHeaders(headers http.Header, path string) error {
	tokens := map[string]bool{}
	agentTokens := map[string]bool{}
	for _, value := range headers.Values("X-Robots-Tag") {
		for token := range directives(value) {
			tokens[token] = true
		}
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				parts := strings.SplitN(part, ":", 2)
				agentTokens[strings.ToLower(strings.TrimSpace(parts[len(parts)-1]))] = true
			}
		}
	}
	if !tokens["index"] || !tokens["follow"] {
		return fmt.Errorf("%s must send X-Robots-Tag index,follow", path)
	}
	for _, blocking := range []string{"noindex", "nofollow", "none"} {
		if tokens[blocking] || agentTokens[blocking] {
			return fmt.Errorf("%s must not send blocking X-Robots-Tag directives", path)
		}
	}
	return nil
}
func (s *Smoke) checkRobotsAndSitemap(ctx context.Context) error {
	status, headers, body, err := s.get(ctx, "/robots.txt")
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("robots.txt returned HTTP %d", status)
	}
	if mediaType(headers) != "text/plain" {
		return errors.New("robots.txt must have text/plain content type")
	}
	if err = expectIndexFollowHeaders(headers, "/robots.txt"); err != nil {
		return err
	}
	robots := string(body)
	normalized := strings.ToLower(robots)
	if !strings.Contains(normalized, "user-agent: *") {
		return errors.New("robots.txt must include User-agent: *")
	}
	if !strings.Contains(normalized, "allow: /") {
		return errors.New("robots.txt must allow crawlable pages")
	}
	if !strings.Contains(normalized, "sitemap: "+Origin+"/sitemap.xml") {
		return errors.New("robots.txt must advertise the canonical sitemap URL")
	}
	disallowed := map[string]bool{}
	for _, line := range strings.Split(robots, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(line, "disallow:") {
			disallowed[strings.TrimSpace(strings.SplitN(line, ":", 2)[1])] = true
			for _, path := range []string{"/login", "/register", "/app", "/app/settings", "/app/admin"} {
				if strings.Contains(line, path) {
					return fmt.Errorf("robots.txt must not block crawl access to private HTML %s", path)
				}
			}
		}
	}
	for _, path := range []string{"/api/", "/downloads/", "/.well-known/"} {
		if !disallowed[path] {
			return fmt.Errorf("robots.txt must keep technical path %s out of crawls", path)
		}
	}
	status, headers, body, err = s.get(ctx, "/sitemap.xml")
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("sitemap.xml returned HTTP %d", status)
	}
	if mediaType(headers) != "application/xml" {
		return errors.New("sitemap.xml must have application/xml content type")
	}
	if err = expectIndexFollowHeaders(headers, "/sitemap.xml"); err != nil {
		return err
	}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	locations := []string{}
	depth, roots := 0, 0
	for {
		token, e := decoder.Token()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return errors.New("sitemap is not valid XML")
		}
		switch element := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots > 1 {
					return errors.New("sitemap is not valid XML")
				}
			}
			depth++
			if element.Name.Space != "" && element.Name.Local == "lastmod" {
				return errors.New("sitemap must omit fabricated lastmod values")
			}
			if element.Name.Space != "" && element.Name.Local == "loc" {
				var location string
				if e = decoder.DecodeElement(&location, &element); e != nil {
					return errors.New("sitemap is not valid XML")
				}
				depth--
				locations = append(locations, location)
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(element)) != "" {
				return errors.New("sitemap is not valid XML")
			}
		}
	}
	if depth != 0 || roots != 1 {
		return errors.New("sitemap is not valid XML")
	}
	expected := map[string]bool{}
	for _, path := range PublicRoutes {
		expected[publicPageURL(path)] = true
	}
	if len(locations) != len(PublicRoutes) {
		return errors.New("sitemap locations must be exactly the six public HTTPS URLs")
	}
	for _, location := range locations {
		if !expected[location] {
			return errors.New("sitemap locations must be exactly the six public HTTPS URLs")
		}
		delete(expected, location)
	}
	if len(expected) > 0 {
		return errors.New("sitemap locations must be exactly the six public HTTPS URLs")
	}
	return nil
}
func (s *Smoke) check404(ctx context.Context) error {
	status, headers, body, err := s.get(ctx, "/this-page-intentionally-does-not-exist")
	if err != nil {
		return err
	}
	if status != 404 {
		return fmt.Errorf("missing page returned HTTP %d instead of 404", status)
	}
	doc, err := ParsePage(body)
	if err != nil {
		return err
	}
	if !blocked(pageRobots(doc)) {
		return errors.New("404 page must render noindex,nofollow")
	}
	if !blocked(directives(headers.Get("X-Robots-Tag"))) {
		return errors.New("404 response must send noindex,nofollow")
	}
	return nil
}
