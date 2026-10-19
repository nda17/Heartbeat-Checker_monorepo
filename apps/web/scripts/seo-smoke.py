#!/usr/bin/env python3
"""Validate public SEO contracts against rendered HTTP responses."""

import argparse
from html.parser import HTMLParser
import json
from pathlib import Path
import struct
import sys
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET


ORIGIN = "https://heartbeat-checker.ru"
PUBLIC_ROUTES = ("/", "/android", "/contacts", "/terms", "/privacy", "/cookies")
PRIVATE_ROUTES = (
    "/login", "/register", "/forgot-password", "/reset-password", "/verify-email",
    "/app", "/app/settings", "/app/billing", "/app/admin",
)
SYSTEM_ROUTES = ("/api/v1/info",)
DISALLOWED_RATING_KEYS = {"rating", "ratingvalue", "aggregaterating", "reviewcount", "review"}


class Page(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.title_parts = []
        self.meta = {}
        self.links = []
        self.json_ld = []
        self._in_title = False
        self._json_ld_parts = None

    def handle_starttag(self, tag, attrs):
        attributes = dict(attrs)
        if tag == "title":
            self._in_title = True
        elif tag == "meta":
            key = attributes.get("property") or attributes.get("name")
            if key:
                self.meta.setdefault(key.lower(), []).append(attributes.get("content", ""))
        elif tag == "link":
            self.links.append(attributes)
        elif tag == "script" and attributes.get("type", "").lower() == "application/ld+json":
            self._json_ld_parts = []

    def handle_endtag(self, tag):
        if tag == "title":
            self._in_title = False
        elif tag == "script" and self._json_ld_parts is not None:
            raw = "".join(self._json_ld_parts).strip()
            try:
                self.json_ld.append(json.loads(raw))
            except json.JSONDecodeError as error:
                raise AssertionError("invalid JSON-LD script") from error
            self._json_ld_parts = None

    def handle_data(self, data):
        if self._in_title:
            self.title_parts.append(data)
        if self._json_ld_parts is not None:
            self._json_ld_parts.append(data)

    @property
    def title(self):
        return " ".join(" ".join(self.title_parts).split())


class Smoke:
    def __init__(self, base_url, require_api_header=False):
        parsed = urllib.parse.urlsplit(base_url)
        if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.path not in ("", "/"):
            raise ValueError("base URL must be an HTTP(S) origin")
        self.base_url = base_url.rstrip("/")
        self.require_api_header = require_api_header
        self.documents = {}

    def get(self, path):
        request = urllib.request.Request(self.base_url + path, headers={"User-Agent": "HeartbeatSEOHealthCheck/1.0"})
        try:
            with urllib.request.urlopen(request, timeout=15) as response:
                return response.status, response.headers, response.read(2 * 1024 * 1024 + 1)
        except urllib.error.HTTPError as error:
            return error.code, error.headers, error.read(2 * 1024 * 1024 + 1)

    def check(self):
        titles = set()
        descriptions = set()
        for path in PUBLIC_ROUTES:
            status, headers, body = self.get(path)
            expect(status == 200, "{} returned HTTP {}".format(path, status))
            expect(len(body) <= 2 * 1024 * 1024, "{} HTML exceeds 2 MiB".format(path))
            doc = Page()
            doc.feed(body.decode("utf-8", errors="replace"))
            self.documents[path] = doc
            title = doc.title
            description = one(doc, "description")
            titles.add(title)
            descriptions.add(description)
            expect(bool(title.strip()), "{} has an empty title".format(path))
            expect(bool(description.strip()), "{} has an empty description".format(path))
            expected_url = public_page_url(path)
            canonical = links(doc, "canonical")
            expect(canonical == [expected_url], "{} canonical is {}, expected {}".format(path, canonical, expected_url))
            expect(one(doc, "robots").replace(" ", "").lower() == "index,follow",
                   "{} must render robots index,follow".format(path))
            expect(header_value(headers, "X-Robots-Tag").replace(" ", "").lower() == "index,follow",
                   "{} must send X-Robots-Tag index,follow".format(path))
            expect(one(doc, "og:url") == expected_url, "{} OpenGraph URL differs from canonical".format(path))
            expect(one(doc, "og:title") == title, "{} OpenGraph title differs from HTML title".format(path))
            expect(one(doc, "og:description") == description,
                   "{} OpenGraph description differs from page description".format(path))
            expect(one(doc, "og:type") == "website", "{} OpenGraph type must be website".format(path))
            expect(one(doc, "og:locale") == "ru_RU", "{} OpenGraph locale must be ru_RU".format(path))
            expect(one(doc, "og:site_name") == "Heartbeat Checker",
                   "{} OpenGraph site name is incorrect".format(path))
            image = one(doc, "og:image")
            expect(image == ORIGIN + "/og", "{} uses unexpected OpenGraph image {}".format(path, image))
            expect(one(doc, "og:image:width") == "1200" and one(doc, "og:image:height") == "630",
                   "{} OpenGraph image dimensions must be 1200x630".format(path))
            expect(one(doc, "twitter:card") == "summary_large_image",
                   "{} Twitter card must use summary_large_image".format(path))
            self.check_json_ld(path, doc)

        expect(len(titles) == len(PUBLIC_ROUTES), "public pages must have distinct titles")
        expect(len(descriptions) == len(PUBLIC_ROUTES), "public pages must have distinct descriptions")
        self.check_og_image()
        self.check_private_routes()
        self.check_robots_and_sitemap()
        self.check_404()

    def check_json_ld(self, path, doc):
        expect(len(doc.json_ld) == 1, "{} must render one JSON-LD graph".format(path))
        graph = doc.json_ld[0]
        expect(graph.get("@context") == "https://schema.org", "{} has unsafe or unexpected JSON-LD context".format(path))
        nodes = graph.get("@graph")
        expect(isinstance(nodes, list), "{} JSON-LD must use an @graph array".format(path))
        types = {node.get("@type") for node in nodes if isinstance(node, dict)}
        expect("WebSite" in types and ("WebPage" in types or "ContactPage" in types),
               "{} JSON-LD must describe WebSite and a page".format(path))
        if path == "/contacts":
            expect("ContactPage" in types, "contacts JSON-LD must describe a ContactPage")
        release_path = Path(__file__).resolve().parents[1] / "src/shared/android/release.json"
        release_value = json.loads(release_path.read_text(encoding="utf-8"))
        if path == "/android" and release_value is not None:
            expect("SoftwareApplication" in types, "released Android page should describe its SoftwareApplication")
        if path == "/android" and release_value is None:
            expect("SoftwareApplication" not in types, "AndroidSoftwareApplication requires a published release")
        forbidden = set()
        collect_forbidden_keys(graph, forbidden)
        expect(not forbidden, "{} JSON-LD contains unsupported rating/review data: {}".format(path, sorted(forbidden)))

    def check_og_image(self):
        status, headers, body = self.get("/og")
        expect(status == 200, "OpenGraph image returned HTTP {}".format(status))
        expect(header_value(headers, "Content-Type").split(";")[0].strip().lower() == "image/png",
               "OpenGraph image has the wrong content type")
        expect(len(body) >= 24 and body[:8] == b"\x89PNG\r\n\x1a\n", "OpenGraph image is not a valid PNG")
        dimensions = struct.unpack(">II", body[16:24])
        expect(dimensions == (1200, 630), "OpenGraph image dimensions are {}".format(dimensions))

    def check_private_routes(self):
        for path in PRIVATE_ROUTES:
            status, headers, body = self.get(path)
            expect(status in (200, 404), "private/system route {} returned HTTP {}".format(path, status))
            doc = Page()
            doc.feed(body.decode("utf-8", errors="replace"))
            robots = set().union(*(directives(value) for value in all(doc, "robots")))
            header = directives(header_value(headers, "X-Robots-Tag"))
            expect({"noindex", "nofollow"}.issubset(robots),
                   "{} must render noindex/nofollow".format(path))
            expect({"noindex", "nofollow"}.issubset(header),
                   "{} must send X-Robots-Tag noindex/nofollow".format(path))

        for path in SYSTEM_ROUTES:
            status, headers, body = self.get(path)
            expect(200 <= status < 600, "system route {} did not return an HTTP response".format(path))
            robots = directives(header_value(headers, "X-Robots-Tag"))
            content_type = header_value(headers, "Content-Type").split(";", 1)[0].strip().lower()
            if content_type == "text/html":
                doc = Page()
                doc.feed(body.decode("utf-8", errors="replace"))
                page_robots = set().union(*(directives(value) for value in all(doc, "robots")))
                expect({"noindex", "nofollow"}.issubset(page_robots | robots),
                       "HTML response from {} must remain noindex,nofollow".format(path))
            else:
                expect(b"<html" not in body.lower(),
                       "system route {} must not render public HTML with noindex metadata absent".format(path))
            if self.require_api_header:
                expect({"noindex", "nofollow"}.issubset(robots),
                       "{} must send X-Robots-Tag noindex,nofollow".format(path))
            elif robots:
                expect({"noindex", "nofollow"}.issubset(robots),
                       "{} X-Robots-Tag must remain noindex,nofollow when present".format(path))

    def check_robots_and_sitemap(self):
        status, headers, body = self.get("/robots.txt")
        expect(status == 200, "robots.txt returned HTTP {}".format(status))
        expect(header_value(headers, "Content-Type").split(";", 1)[0].strip().lower() == "text/plain",
               "robots.txt must have text/plain content type")
        expect_index_follow_headers(headers, "/robots.txt")
        robots = body.decode("utf-8", errors="replace")
        normalized = robots.lower()
        expect("user-agent: *" in normalized, "robots.txt must include User-agent: *")
        expect("allow: /" in normalized, "robots.txt must allow crawlable pages")
        expect("sitemap: {}".format((ORIGIN + "/sitemap.xml").lower()) in normalized,
               "robots.txt must advertise the canonical sitemap URL")
        disallowed = [line.split(":", 1)[1].strip().lower() for line in robots.splitlines()
                      if line.strip().lower().startswith("disallow:")]
        for technical_path in ("/api/", "/downloads/", "/.well-known/"):
            expect(technical_path in disallowed,
                   "robots.txt must keep technical path {} out of crawls".format(technical_path))
        for path in ("/login", "/register", "/app", "/app/settings", "/app/admin"):
            expect(not any(line.strip().lower().startswith("disallow:") and path in line.lower()
                           for line in robots.splitlines()),
                   "robots.txt must not block crawl access to private HTML {}".format(path))

        status, headers, body = self.get("/sitemap.xml")
        expect(status == 200, "sitemap.xml returned HTTP {}".format(status))
        expect(header_value(headers, "Content-Type").split(";", 1)[0].strip().lower() == "application/xml",
               "sitemap.xml must have application/xml content type")
        expect_index_follow_headers(headers, "/sitemap.xml")
        try:
            root = ET.fromstring(body)
        except ET.ParseError as error:
            raise AssertionError("sitemap is not valid XML") from error
        locations = [element.text for element in root.iter() if element.tag.endswith("}loc")]
        expected = [public_page_url(path) for path in PUBLIC_ROUTES]
        expect(set(locations) == set(expected) and len(locations) == len(expected),
               "sitemap locations must be exactly the six public HTTPS URLs: {}".format(locations))
        expect(not any(element.tag.endswith("}lastmod") for element in root.iter()),
               "sitemap must omit fabricated lastmod values")

    def check_404(self):
        status, headers, body = self.get("/this-page-intentionally-does-not-exist")
        expect(status == 404, "missing page returned HTTP {} instead of 404".format(status))
        doc = Page()
        doc.feed(body.decode("utf-8", errors="replace"))
        robots = set().union(*(directives(value) for value in all(doc, "robots")))
        header = directives(header_value(headers, "X-Robots-Tag"))
        expect({"noindex", "nofollow"}.issubset(robots), "404 page must render noindex,nofollow")
        expect({"noindex", "nofollow"}.issubset(header), "404 response must send noindex,nofollow")


def one(doc, key):
    values = all(doc, key)
    expect(len(values) == 1, "expected one {} metadata value, found {}".format(key, values))
    return values[0]


def all(doc, key):
    return doc.meta.get(key.lower(), [])


def links(doc, rel):
    return [link.get("href", "") for link in doc.links if rel in link.get("rel", "").lower().split()]


def public_page_url(path):
    # URL serialization removes the optional trailing slash from a bare origin.
    return ORIGIN if path == "/" else ORIGIN + path


def header_value(headers, name):
    return headers.get(name, "")


def header_values(headers, name):
    return headers.get_all(name, [])


def expect_index_follow_headers(headers, path):
    values = header_values(headers, "X-Robots-Tag")
    tokens = set().union(*(directives(value) for value in values))
    agent_tokens = {part.split(":", 1)[-1].strip().lower()
                    for value in values for part in value.split(",") if part.strip()}
    expect({"index", "follow"}.issubset(tokens),
           "{} must send X-Robots-Tag index,follow; found {}".format(path, values))
    expect(not (tokens | agent_tokens).intersection({"noindex", "nofollow", "none"}),
           "{} must not send blocking X-Robots-Tag directives: {}".format(path, values))


def directives(value):
    return {part.strip().lower() for part in value.split(",") if part.strip()}


def collect_forbidden_keys(value, found):
    if isinstance(value, dict):
        for key, child in value.items():
            if key.casefold() in DISALLOWED_RATING_KEYS:
                found.add(key)
            collect_forbidden_keys(child, found)
    elif isinstance(value, list):
        for item in value:
            collect_forbidden_keys(item, found)


def expect(condition, message):
    if not condition:
        raise AssertionError(message)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:3139")
    parser.add_argument("--require-api-header", action="store_true",
                        help="require edge X-Robots-Tag on API routes, for Caddy/public postflight")
    args = parser.parse_args()
    try:
        Smoke(args.base_url, require_api_header=args.require_api_header).check()
    except Exception as error:
        print("SEO smoke failed: {}".format(error), file=sys.stderr)
        return 1
    print("SEO HTTP smoke passed for six public pages, private noindex routes, robots, sitemap and 404.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
