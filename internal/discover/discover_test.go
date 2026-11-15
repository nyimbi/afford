package discover

import (
	"testing"

	"afford/internal/config"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"http://x.test/orders/1/?utm_source=a&b=2&a=1#top": "http://x.test/orders/1?a=1&b=2",
		"http://x.test/":          "http://x.test/",
		"http://x.test//a//b":     "http://x.test/a/b",
		"http://x.test/p?z=1&z=2": "http://x.test/p?z=1&z=2",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTemplate(t *testing.T) {
	cases := map[string]string{
		"/orders/1042":            "/orders/{id}",
		"/users/9f8e7d6c5b4a3f21": "/users/{id}",
		"/invoices/2026-01-15":    "/invoices/{id}",
		"/products/widget":        "/products/widget",
		"/":                       "/",
	}
	for in, want := range cases {
		if got := Template("http://x.test" + in); got != want {
			t.Errorf("Template(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInScope(t *testing.T) {
	cfg := config.Default()
	cfg.Run.BaseURL = "http://app.test"
	cfg.Scope.Include = []string{"/**"}
	cfg.Scope.Exclude = []string{"/api/**"}
	cases := map[string]bool{
		"http://app.test/":           true,
		"http://app.test/orders/1":   true,
		"http://app.test/api/health": false,
		"http://other.test/orders":   false,
		"https://app.test/orders":    false,
	}
	for in, want := range cases {
		if got := InScope(in, cfg); got != want {
			t.Errorf("InScope(%q) = %v, want %v", in, got, want)
		}
	}
	cfg.Scope.AllowHosts = []string{"cdn.app.test"}
	if !InScope("http://cdn.app.test/x", cfg) {
		t.Error("allow_hosts should admit the listed host")
	}
}

func TestAbsoluteRejectsUncrawlable(t *testing.T) {
	for _, href := range []string{"#", "javascript:void(0)", "mailto:a@b.test", "tel:123", "data:text/plain,x"} {
		if got := Absolute("http://x.test/", href); got != "" {
			t.Errorf("Absolute(%q) = %q, want empty", href, got)
		}
	}
	if got := Absolute("http://x.test/a/b", "../c"); got != "http://x.test/c" {
		t.Errorf("relative resolution = %q", got)
	}
}

func TestIsLoginURL(t *testing.T) {
	patterns := []string{"login", "signin", "sign-in"}
	cases := map[string]bool{
		"http://x.test/login":        true,
		"http://x.test/auth/signin":  true,
		"http://x.test/author/alice": false,
		"http://x.test/dashboard":    false,
	}
	for in, want := range cases {
		if got := IsLoginURL(in, patterns); got != want {
			t.Errorf("IsLoginURL(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		page, frame string
		want        bool
	}{
		{"http://a.test/x", "http://a.test/widget", true},
		{"http://a.test/x", "about:blank", true},
		{"http://a.test/x", "", true},
		{"http://a.test/x", "https://a.test/widget", false},
		{"http://a.test:3000/x", "http://a.test:4000/y", false},
		{"http://a.test/x", "https://pay.example.com/f", false},
	}
	for _, c := range cases {
		if got := sameOrigin(c.page, c.frame); got != c.want {
			t.Errorf("sameOrigin(%q, %q) = %v, want %v", c.page, c.frame, got, c.want)
		}
	}
}
