package routes

import (
	"reflect"
	"testing"
)

func TestParseSitemap(t *testing.T) {
	pages, nested, err := parseSitemap([]byte(`<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
 <url><loc> https://a.test/one </loc></url><url><loc>https://a.test/two</loc></url>
</urlset>`))
	if err != nil || !reflect.DeepEqual(pages, []string{"https://a.test/one", "https://a.test/two"}) || len(nested) != 0 {
		t.Fatalf("pages=%v nested=%v err=%v", pages, nested, err)
	}
	pages, nested, err = parseSitemap([]byte(`<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><sitemap><loc>https://a.test/s2.xml</loc></sitemap></sitemapindex>`))
	if err != nil || len(pages) != 0 || !reflect.DeepEqual(nested, []string{"https://a.test/s2.xml"}) {
		t.Fatalf("index: pages=%v nested=%v err=%v", pages, nested, err)
	}
	if _, _, err := parseSitemap([]byte("not xml")); err == nil {
		t.Fatal("garbage must be an error, not an empty sitemap")
	}
}

func TestParseNextManifest(t *testing.T) {
	static, dynamic := parseNextManifest([]byte(
		`self.__BUILD_MANIFEST={"/":["a.js"],"/_app":["b.js"],"/pricing":["c.js"],"/users/[id]":["d.js"],"sortedPages":["/"]}`))
	if !reflect.DeepEqual(static, []string{"/", "/pricing"}) {
		t.Fatalf("static=%v: internals and dynamic keys must be excluded", static)
	}
	if len(dynamic) != 1 || dynamic[0] != "/users/[id" && dynamic[0] != "/users/[id]" {
		t.Fatalf("dynamic=%v", dynamic)
	}
}
