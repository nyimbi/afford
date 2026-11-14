// Package config loads afford.toml over built-in defaults and applies CLI overrides.
package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Run    RunConfig    `toml:"run"`
	Scope  ScopeConfig  `toml:"scope"`
	Auth   AuthConfig   `toml:"auth"`
	Safety SafetyConfig `toml:"safety"`
	Limits LimitsConfig `toml:"limits"`
	MCP    MCPConfig    `toml:"mcp"`
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
	AllowDestructive bool     `toml:"allow_destructive"`
	SubmitForms      bool     `toml:"submit_forms"`
	ExtraDestructive []string `toml:"extra_destructive"`
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
		MCP: MCPConfig{CallTimeoutSecs: 120, MaxControls: 40},
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
allow_destructive = false
submit_forms = true
extra_destructive = []

[mcp]
# The agent-facing server refuses URLs outside the configured origin unless
# this is true. Do not widen it for a server an LLM can drive.
allow_any_host = false
call_timeout_secs = 120
max_controls = 40
`
