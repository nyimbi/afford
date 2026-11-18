// Package config loads afford.toml over built-in defaults and applies CLI overrides.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/nyimbi/afford/internal/model"
)

type Config struct {
	Run     RunConfig     `toml:"run"`
	Scope   ScopeConfig   `toml:"scope"`
	Auth    AuthConfig    `toml:"auth"`
	Safety  SafetyConfig  `toml:"safety"`
	Limits  LimitsConfig  `toml:"limits"`
	MCP     MCPConfig     `toml:"mcp"`
	Uploads UploadsConfig `toml:"uploads"`
	Popups  PopupsConfig  `toml:"popups"`
	Visual  VisualConfig  `toml:"visual"`

	Readiness ReadinessConfig `toml:"readiness"`
	Policy    PolicyConfig    `toml:"policy"`
	Baseline  BaselineConfig  `toml:"baseline"`
	Privacy   PrivacyConfig   `toml:"privacy"`
	Fixtures  FixturesConfig  `toml:"fixtures"`
	Routes    RoutesConfig    `toml:"routes"`
}

// PrivacyConfig controls what a screenshot may show. Password fields are always
// masked; screenshots land in CI artifacts that more people can open than can
// open the app.
type PrivacyConfig struct {
	Mask []string `toml:"mask"`
}

// ScreenshotMasks returns every selector masked in every screenshot.
func (c Config) ScreenshotMasks() []string {
	out := []string{"input[type=password]"}
	out = append(out, c.Privacy.Mask...)
	return out
}

// Profile is one role in a multi-role run. The first authentication in
// [auth] is the primary role; each profile is replayed against the pages it
// found, with its own session.
type Profile struct {
	Name         string   `toml:"name"`
	Mode         string   `toml:"mode"`
	UsernameEnv  string   `toml:"username_env"`
	PasswordEnv  string   `toml:"password_env"`
	StorageState string   `toml:"storage_state"`
	Deny         []string `toml:"deny"`
}

// FixturesConfig names commands that seed and clean the data a run needs. Both
// receive AFFORD_RUN_ID and AFFORD_BASE_URL in their environment.
type FixturesConfig struct {
	Setup       string `toml:"setup"`
	Teardown    string `toml:"teardown"`
	TimeoutSecs int    `toml:"timeout_secs"`
}

// RoutesConfig enumerates URLs a link crawl cannot reach.
type RoutesConfig struct {
	Sitemap  bool     `toml:"sitemap"`
	Manifest bool     `toml:"manifest"`
	Extra    []string `toml:"extra"`
}

// ReadinessConfig tells afford when a page is done rendering. The default
// heuristic (load, quiet network, stable node count) measures a page mid-render
// whenever data arrives late; a selector is the app's own statement that it is
// ready.
type ReadinessConfig struct {
	// Selector must be visible before the page counts as ready.
	Selector string `toml:"selector"`
	// Loading selectors must be gone (hidden or detached) before the page is ready.
	Loading []string `toml:"loading"`
	// Routes overrides Selector per path glob.
	Routes    map[string]string `toml:"routes"`
	TimeoutMS int               `toml:"timeout_ms"`
}

// PolicyConfig adjusts what counts as a finding without editing the catalogue.
type PolicyConfig struct {
	// Severity maps a finding code to blocker|critical|major|minor|info|off.
	Severity map[string]string `toml:"severity"`
	// IgnoreConsole and IgnoreRequests are regexes; a console message or request
	// URL that matches never reaches an oracle (third-party noise).
	IgnoreConsole  []string `toml:"ignore_console"`
	IgnoreRequests []string `toml:"ignore_requests"`
}

// BaselineConfig is the findings baseline: known defects that must not fail the
// gate, so the gate can be switched on for an app that already has some.
type BaselineConfig struct {
	File         string `toml:"file"`
	SuppressFile string `toml:"suppress_file"`
	Disabled     bool   `toml:"disabled"`
	// Accept is set by --accept: write the current findings as the baseline.
	Accept bool `toml:"-"`
}

// VisualConfig governs screenshot comparison. Off by default: a visual gate is
// only meaningful once you have decided what a baseline is and where it lives.
type VisualConfig struct {
	Enabled        bool     `toml:"enabled"`
	UpdateBaseline bool     `toml:"update_baseline"`
	ThresholdPct   float64  `toml:"threshold_pct"`
	Tolerance      int      `toml:"tolerance"`
	MaxDiffPixels  int      `toml:"max_diff_pixels"`
	Mask           []string `toml:"mask"`
	Ignore         []string `toml:"ignore"`
	Dir            string   `toml:"dir"`
	// UpdatePages limits baseline updates to routes matching these globs; empty
	// means --update-baseline updates every page.
	UpdatePages []string `toml:"update_pages"`
	// MaxAgeDays notes baselines older than this (0 turns it off).
	MaxAgeDays int `toml:"max_age_days"`
}

// PopupsConfig controls what happens to pages a control opens in a new tab.
type PopupsConfig struct {
	Mode string `toml:"mode"` // capture | off
	Max  int    `toml:"max"`
}

// UploadsConfig maps a file input to a fixture. Every rule is a local path or
// a name inside Dir; the browser never receives a path the operator did not
// choose, and a missing fixture degrades to a synthesized file.
type UploadsConfig struct {
	Dir      string            `toml:"dir"`
	Default  string            `toml:"default"`
	ByName   map[string]string `toml:"by_name"`
	ByAccept map[string]string `toml:"by_accept"`
}

// MCPConfig governs the agent-facing server. An agent can name any URL, so the
// host allowlist is the default; widening it is an explicit act.
type MCPConfig struct {
	AllowAnyHost    bool `toml:"allow_any_host"`
	CallTimeoutSecs int  `toml:"call_timeout_secs"`
	MaxControls     int  `toml:"max_controls"`
	DefaultReadOnly bool `toml:"default_read_only"`
}

type RunConfig struct {
	BaseURL     string `toml:"base_url"`
	Out         string `toml:"out"`
	Depth       string `toml:"depth"`
	Headful     bool   `toml:"headful"`
	Viewport    []int  `toml:"viewport"`
	Seed        int64  `toml:"seed"`
	EmbedImages bool   `toml:"embed_images"`
	Browser     string `toml:"browser"`
	TimeoutSecs int    `toml:"timeout_secs"`
	// Confirm re-probes a control that produced a suspect finding once from a
	// fresh load; a finding that does not reproduce is reported as unconfirmed
	// and does not fail the gate.
	Confirm bool `toml:"confirm"`
	// Reveal is how many newly visible controls a single click may add to the
	// probe queue (menu items, dialog buttons). 0 turns the rescan off.
	Reveal int `toml:"reveal"`
	// ScrollSteps bounds scroll-to-load before each scan; 0 turns it off.
	ScrollSteps int `toml:"scroll_steps"`
	// A11y runs the bounded axe-core rule set on each page; Keyboard runs the
	// Tab pass; Text and Images run the rendered-text and broken-image oracles.
	A11y     bool `toml:"a11y"`
	Keyboard bool `toml:"keyboard"`
	// Viewports are extra [width, height] pairs captured after the primary
	// viewport: screenshot, overflow and visual baseline only, no probing.
	Viewports [][]int `toml:"viewports"`
}

type ScopeConfig struct {
	Include    []string `toml:"include"`
	Exclude    []string `toml:"exclude"`
	AllowHosts []string `toml:"allow_hosts"`
}

type AuthConfig struct {
	Mode               string            `toml:"mode"`
	LoginURL           string            `toml:"login_url"`
	UsernameField      string            `toml:"username_field"`
	PasswordField      string            `toml:"password_field"`
	SubmitSelector     string            `toml:"submit_selector"`
	StorageState       string            `toml:"storage_state"`
	SuccessURLContains string            `toml:"success_url_contains"`
	UsernameEnv        string            `toml:"username_env"`
	PasswordEnv        string            `toml:"password_env"`
	ExtraHeaders       map[string]string `toml:"extra_headers"`
	LoginPathPatterns  []string          `toml:"login_path_patterns"`
	Profiles           []Profile         `toml:"profiles"`
}

type SafetyConfig struct {
	// AllowDestructive is the umbrella grant: it covers both classes below,
	// and remains the shorthand for "I know what this will do".
	AllowDestructive bool `toml:"allow_destructive"`
	// AllowDelete permits controls that delete or erase data.
	AllowDelete bool `toml:"allow_delete"`
	// AllowSideEffects permits irreversible actions that are not deletion:
	// paying, sending, publishing, approving, logging out.
	AllowSideEffects bool     `toml:"allow_side_effects"`
	SubmitForms      bool     `toml:"submit_forms"`
	ExtraDestructive []string `toml:"extra_destructive"`
	// MutationHosts lists host globs where afford may submit forms or click
	// granted controls. Empty means the built-in non-production set; "*" lifts
	// the guard.
	MutationHosts []string `toml:"mutation_hosts"`
}

// DefaultMutationHosts are hosts a test run may safely write to: loopback,
// private networks, and conventionally non-production names.
var DefaultMutationHosts = []string{
	"localhost", "127.*", "[::1]", "10.*", "192.168.*", "172.16.*", "172.17.*", "172.18.*",
	"172.19.*", "172.2?.*", "172.30.*", "172.31.*", "*.localhost", "*.test", "*.local",
	"*.internal", "*.lan", "staging.*", "*.staging.*", "stage.*", "*.stage.*", "dev.*",
	"*.dev.*", "qa.*", "*.qa.*", "uat.*", "*.uat.*", "test.*", "*.test.*", "preview.*",
	"*-staging.*", "*-stage.*", "*-dev.*", "*-qa.*", "*-uat.*", "*-test.*",
}

// Mutates reports whether this configuration would write to the application:
// submit forms or click anything in a granted class.
func (s SafetyConfig) Mutates() bool {
	return s.SubmitForms || s.AllowDestructive || s.AllowDelete || s.AllowSideEffects
}

// MutationAllowed reports whether host (no port) may be written to.
func (s SafetyConfig) MutationAllowed(host string) bool {
	list := s.MutationHosts
	if len(list) == 0 {
		list = DefaultMutationHosts
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pat := range list {
		pat = strings.ToLower(strings.TrimSpace(pat))
		if pat == "*" {
			return true
		}
		if ok, _ := path.Match(pat, host); ok {
			return true
		}
	}
	return false
}

// CheckMutationGuard returns an error when the config would write to a host
// the allowlist does not cover. This is the production guard: a crawl that
// submits forms against a live site creates real rows.
func (c Config) CheckMutationGuard() error {
	if !c.Safety.Mutates() {
		return nil
	}
	u, err := url.Parse(c.Run.BaseURL)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	if c.Safety.MutationAllowed(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("refusing to write to %s: it is not in [safety] mutation_hosts "+
		"(default: localhost, private ranges, *.test, *.local, staging/dev/qa names). "+
		"Use --no-submit for a read-only run, or set mutation_hosts = [\"%s\"] to allow this host, "+
		"or [\"*\"] to lift the guard", u.Hostname(), u.Hostname())
}

// Permits reports whether the configured grants cover a permission class.
func (s SafetyConfig) Permits(kind string) bool {
	if s.AllowDestructive {
		return true
	}
	switch kind {
	case "delete":
		return s.AllowDelete
	case "side_effect":
		return s.AllowSideEffects
	}
	return false
}

type LimitsConfig struct {
	MaxPages                int `toml:"max_pages"`
	MaxDepth                int `toml:"max_depth"`
	MaxInstancesPerTemplate int `toml:"max_instances_per_template"`
	MaxControlsPerPage      int `toml:"max_controls_per_page"`
	MaxFormsPerPage         int `toml:"max_forms_per_page"`
	Concurrency             int `toml:"concurrency"`
	NavigationTimeoutMS     int `toml:"navigation_timeout_ms"`
	ProbeTimeoutMS          int `toml:"probe_timeout_ms"`
	SettleMS                int `toml:"settle_ms"`
	QuietMS                 int `toml:"quiet_ms"`
}

func Default() Config {
	return Config{
		Run: RunConfig{
			Out: ".afford", Depth: "standard", Viewport: []int{1280, 900},
			Browser: "chromium", TimeoutSecs: 900, Confirm: true,
			Reveal: 4, ScrollSteps: 5, A11y: true, Keyboard: true,
		},
		Scope: ScopeConfig{
			Include: []string{"/**"},
			Exclude: []string{"/logout", "/signout", "/sign-out", "/api/**"},
		},
		Auth: AuthConfig{
			Mode: "none", LoginURL: "/login",
			UsernameEnv: "AFFORD_USERNAME", PasswordEnv: "AFFORD_PASSWORD",
			LoginPathPatterns: []string{"login", "signin", "sign-in", "auth", "sso"},
		},
		Safety: SafetyConfig{SubmitForms: true},
		Limits: LimitsConfig{
			MaxPages: 200, MaxDepth: 4, MaxInstancesPerTemplate: 3,
			MaxControlsPerPage: 80, MaxFormsPerPage: 8, Concurrency: 3,
			NavigationTimeoutMS: 15000, ProbeTimeoutMS: 8000,
			SettleMS: 1200, QuietMS: 350,
		},
		MCP:       MCPConfig{CallTimeoutSecs: 120, MaxControls: 40},
		Uploads:   UploadsConfig{},
		Popups:    PopupsConfig{Mode: "capture", Max: 5},
		Visual:    VisualConfig{ThresholdPct: 0.1, Tolerance: 12, Dir: ".afford/baselines", MaxAgeDays: 90},
		Readiness: ReadinessConfig{TimeoutMS: 8000},
		Baseline:  BaselineConfig{File: ".afford/findings-baseline.json", SuppressFile: "afford.suppress.toml"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		md, err := toml.DecodeFile(path, &cfg)
		if err != nil {
			return cfg, fmt.Errorf("read %s: %w", path, err)
		}
		// A key in the wrong table is a setting that reads as applied and is
		// not: `[run] concurrency` was inert for months because the field lives
		// under [limits]. Refuse it by name instead of silently ignoring it.
		if keys := md.Undecoded(); len(keys) > 0 {
			names := make([]string, 0, len(keys))
			for _, k := range keys {
				names = append(names, k.String())
			}
			return cfg, fmt.Errorf("%s: unknown key(s): %s", path, strings.Join(names, ", "))
		}
	}
	return cfg, cfg.Validate()
}

func (c *Config) Validate() error {
	var problems []string
	c.Run.BaseURL = strings.TrimRight(strings.TrimSpace(c.Run.BaseURL), "/")
	if !strings.HasPrefix(c.Run.BaseURL, "http://") && !strings.HasPrefix(c.Run.BaseURL, "https://") {
		problems = append(problems, "run.base_url must be an http(s) URL")
	}
	switch c.Run.Depth {
	case "smoke", "standard", "deep":
	default:
		problems = append(problems, "run.depth must be smoke, standard or deep")
	}
	switch strings.ToLower(c.Run.Browser) {
	case "", "chromium", "firefox", "webkit":
	default:
		problems = append(problems, "run.browser must be chromium, firefox or webkit")
	}
	switch c.Popups.Mode {
	case "capture", "off":
	default:
		problems = append(problems, "popups.mode must be capture or off")
	}
	switch c.Auth.Mode {
	case "none", "form", "basic", "bearer", "storage":
	default:
		problems = append(problems, "auth.mode must be none|form|basic|bearer|storage")
	}
	if len(c.Run.Viewport) != 2 || c.Run.Viewport[0] < 320 || c.Run.Viewport[1] < 320 {
		problems = append(problems, "run.viewport must be [width, height] of at least 320")
	}
	if c.Limits.Concurrency < 1 {
		problems = append(problems, "limits.concurrency must be at least 1")
	}
	if c.Limits.MaxPages < 1 {
		problems = append(problems, "limits.max_pages must be at least 1")
	}
	if c.Run.TimeoutSecs < 0 {
		problems = append(problems, "run.timeout_secs must be >= 0")
	}
	if c.MCP.CallTimeoutSecs < 0 {
		problems = append(problems, "mcp.call_timeout_secs must be >= 0")
	}
	if c.Visual.ThresholdPct < 0 {
		problems = append(problems, "visual.threshold_pct must be >= 0")
	}
	for code, level := range c.Policy.Severity {
		if _, ok := model.Catalogue[code]; !ok {
			problems = append(problems, "policy.severity: unknown finding code "+code)
		}
		switch level {
		case "blocker", "critical", "major", "minor", "info", "off":
		default:
			problems = append(problems, "policy.severity."+code+" must be blocker|critical|major|minor|info|off")
		}
	}
	for _, pat := range append(append([]string(nil), c.Policy.IgnoreConsole...), c.Policy.IgnoreRequests...) {
		if _, err := regexp.Compile(pat); err != nil {
			problems = append(problems, "policy ignore pattern "+pat+": "+err.Error())
		}
	}
	if c.Readiness.TimeoutMS < 0 {
		problems = append(problems, "readiness.timeout_ms must be >= 0")
	}
	if c.MCP.MaxControls < 1 {
		problems = append(problems, "mcp.max_controls must be at least 1")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

const Example = `# afford.toml — copy next to the app you are testing.

[run]
base_url = "http://localhost:3000"
depth = "standard"        # smoke | standard | deep
concurrency = 3
timeout_secs = 900        # hard wall for CI
# viewport = [1280, 900]

[scope]
include = ["/**"]
exclude = ["/logout", "/signout", "/api/**"]

[auth]
mode = "none"             # none | form | basic | bearer | storage
# login_url = "/login"
# username_field = "input[name=email]"
# password_field = "input[type=password]"
# storage_state = ".afford/state/session.json"

[safety]
submit_forms = true       # may create or change data by submitting forms
allow_delete = false      # click controls that delete or erase data
allow_side_effects = false # click pay/send/publish/logout/approve controls
allow_destructive = false  # umbrella: sets both grants above
extra_destructive = []

[visual]
enabled = false           # compare screenshots against .afford/baselines
threshold_pct = 0.1       # percent of pixels that may differ
tolerance = 12            # per-channel difference treated as identical
# max_diff_pixels = 100
# mask = [".timestamp", ".live-indicator"]
# ignore = ["/admin/**"]
# dir = ".afford/baselines"

[readiness]
# The app's own statement that it has finished rendering.
# selector = "[data-ready]"
# loading = [".spinner", "[aria-busy=true]"]
# timeout_ms = 8000
# [readiness.routes]
# "/reports/**" = "#chart-rendered"

[policy]
# Re-label or silence a check for this app.
# [policy.severity]
# E_UUID_EXPOSED = "major"
# E_NO_TITLE = "off"
# ignore_console = ["googletagmanager", "Failed to load resource.*analytics"]
# ignore_requests = ["^https://(www\\.)?google-analytics\\.com/"]

[baseline]
# afford run --accept writes this file; later runs fail only on new findings.
file = ".afford/findings-baseline.json"
suppress_file = "afford.suppress.toml"

[popups]
mode = "capture"          # capture | off
max = 5

[uploads]
# dir = "fixtures/uploads"
# default = "sample.pdf"
# [uploads.by_name]
# avatar = "avatar.png"
# [uploads.by_accept]
# "application/pdf" = "sample.pdf"

[mcp]
# The agent-facing server refuses URLs outside the configured origin unless
# this is true. Do not widen it for a server an LLM can drive.
allow_any_host = false
call_timeout_secs = 120
max_controls = 40
`
