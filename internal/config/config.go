// Package config loads afford.toml over built-in defaults and applies CLI overrides.
package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
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
			Seed: 7, Browser: "chromium", TimeoutSecs: 900,
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
		MCP:     MCPConfig{CallTimeoutSecs: 120, MaxControls: 40},
		Uploads: UploadsConfig{},
		Popups:  PopupsConfig{Mode: "capture", Max: 5},
		Visual:  VisualConfig{ThresholdPct: 0.1, Tolerance: 12, Dir: ".afford/baselines"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return cfg, fmt.Errorf("read %s: %w", path, err)
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
