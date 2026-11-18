// Package routes finds URLs a link crawl cannot reach: sitemaps, robots.txt,
// framework build manifests and a hand-written list. A crawl that follows links
// proves only that linked pages work; orphan routes are where regressions hide.
package routes

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nyimbi/afford/internal/config"
)

// Route is one URL with the source that named it.
type Route struct {
	URL    string
	Source string
}

type fetcher struct {
	client  *http.Client
	headers map[string]string
}

func (f fetcher) get(ctx context.Context, raw string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range f.headers {
		req.Header.Set(k, v)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", raw, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

type urlset struct {
	URLs []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

// parseSitemap returns page locations and nested sitemap locations.
func parseSitemap(blob []byte) (pages, nested []string, err error) {
	var set urlset
	if err := xml.Unmarshal(blob, &set); err != nil {
		return nil, nil, err
	}
	for _, u := range set.URLs {
		pages = append(pages, strings.TrimSpace(u.Loc))
	}
	for _, s := range set.Sitemaps {
		nested = append(nested, strings.TrimSpace(s.Loc))
	}
	return pages, nested, nil
}

var sitemapLine = regexp.MustCompile(`(?im)^\s*sitemap:\s*(\S+)`)

// manifestPath matches the route strings inside a Next.js _buildManifest.js.
var (
	manifestSrc  = regexp.MustCompile(`/_next/static/[^"'\s]+/_buildManifest\.js`)
	manifestPath = regexp.MustCompile(`"(/[A-Za-z0-9_\-/]*)"\s*:`)
)

// parseNextManifest extracts static page routes. Dynamic routes ("[id]") need a
// real value, so they are reported by the caller as skipped rather than guessed.
func parseNextManifest(blob []byte) (static, dynamic []string) {
	seen := map[string]bool{}
	for _, m := range manifestPath.FindAllSubmatch(blob, -1) {
		p := string(m[1])
		if seen[p] || p == "/_app" || p == "/_error" || strings.HasPrefix(p, "/_") {
			continue
		}
		seen[p] = true
		static = append(static, p)
	}
	// Dynamic segments are written "[id]" inside the quoted key.
	dyn := regexp.MustCompile(`"(/[A-Za-z0-9_\-/]*\[[^"]+)"`)
	for _, m := range dyn.FindAllSubmatch(blob, -1) {
		dynamic = append(dynamic, string(m[1]))
	}
	return static, dynamic
}

// Result is what enumeration found and what it declined to guess.
type Result struct {
	Routes  []Route
	Skipped []string
	Errors  []string
}

// Enumerate gathers routes per the [routes] configuration. Every route is
// resolved against base and dropped if it leaves the configured origin; a
// sitemap is untrusted input and must not steer the browser off-site.
func Enumerate(ctx context.Context, cfg config.Config, headers map[string]string) Result {
	var res Result
	if !cfg.Routes.Sitemap && !cfg.Routes.Manifest && len(cfg.Routes.Extra) == 0 {
		return res
	}
	base, err := url.Parse(cfg.Run.BaseURL)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		return res
	}
	f := fetcher{client: &http.Client{Timeout: 15 * time.Second}, headers: headers}
	seen := map[string]bool{}
	add := func(raw, source string) {
		ref, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return
		}
		abs := base.ResolveReference(ref)
		if !strings.EqualFold(abs.Host, base.Host) || abs.Scheme != base.Scheme {
			res.Skipped = append(res.Skipped, "off-origin route from "+source+": "+abs.String())
			return
		}
		abs.Fragment = ""
		key := abs.String()
		if seen[key] {
			return
		}
		seen[key] = true
		res.Routes = append(res.Routes, Route{URL: key, Source: source})
	}

	for _, extra := range cfg.Routes.Extra {
		add(extra, "config")
	}

	if cfg.Routes.Sitemap {
		queue := []string{base.ResolveReference(&url.URL{Path: "/sitemap.xml"}).String()}
		if robots, err := f.get(ctx, base.ResolveReference(&url.URL{Path: "/robots.txt"}).String()); err == nil {
			for _, m := range sitemapLine.FindAllSubmatch(robots, -1) {
				queue = append(queue, string(m[1]))
			}
		}
		visited := map[string]bool{}
		for len(queue) > 0 && len(visited) < 20 {
			next := queue[0]
			queue = queue[1:]
			if visited[next] {
				continue
			}
			visited[next] = true
			// A nested sitemap on another host is as untrusted as a page on one.
			if u, err := url.Parse(next); err != nil || !strings.EqualFold(u.Host, base.Host) {
				res.Skipped = append(res.Skipped, "off-origin sitemap: "+next)
				continue
			}
			blob, err := f.get(ctx, next)
			if err != nil {
				if !strings.HasSuffix(next, "/sitemap.xml") {
					res.Errors = append(res.Errors, err.Error())
				}
				continue
			}
			pages, nested, err := parseSitemap(blob)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("parse %s: %v", next, err))
				continue
			}
			for _, p := range pages {
				add(p, "sitemap")
			}
			queue = append(queue, nested...)
		}
	}

	if cfg.Routes.Manifest {
		if html, err := f.get(ctx, cfg.Run.BaseURL+"/"); err == nil {
			if src := manifestSrc.Find(html); src != nil {
				if blob, err := f.get(ctx, base.ResolveReference(&url.URL{Path: string(src)}).String()); err == nil {
					static, dynamic := parseNextManifest(blob)
					for _, p := range static {
						add(p, "next-manifest")
					}
					for _, d := range dynamic {
						res.Skipped = append(res.Skipped, "dynamic route needs a real id: "+d)
					}
				} else {
					res.Errors = append(res.Errors, err.Error())
				}
			} else {
				res.Skipped = append(res.Skipped, "manifest: no Next.js _buildManifest.js found on the home page")
			}
		}
	}

	sort.Slice(res.Routes, func(i, j int) bool { return res.Routes[i].URL < res.Routes[j].URL })
	return res
}
