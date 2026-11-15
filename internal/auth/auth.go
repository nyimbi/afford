// Package auth resolves credentials and establishes a session.
//
// Credentials never enter the run database, the report, or argv. The only place
// they survive is the storage-state file, which the operator creates knowingly.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"
	"golang.org/x/term"

	"afford/internal/browser"
	"afford/internal/config"
	"afford/internal/discover"
)

type Credentials struct {
	Username string
	Password string
}

type Result struct {
	StatePath string
	Creds     *playwright.HttpCredentials
	Headers   map[string]string
	Note      string
}

var usernameSelectors = []string{
	"input[autocomplete=username]",
	"input[type=email]",
	"input[name*=user i]",
	"input[name*=email i]",
	"input[id*=user i]",
	"input[id*=email i]",
	"input[type=text]",
}

func ResolveCredentials(cfg config.Config, allowPrompt bool) (Credentials, error) {
	var creds Credentials
	creds.Username = strings.TrimSpace(os.Getenv(cfg.Auth.UsernameEnv))
	creds.Password = os.Getenv(cfg.Auth.PasswordEnv)
	if !allowPrompt || (creds.Username != "" && creds.Password != "") {
		if creds.Username == "" || creds.Password == "" {
			return creds, fmt.Errorf(
				"credentials not found: set %s and %s in the environment",
				cfg.Auth.UsernameEnv, cfg.Auth.PasswordEnv)
		}
		return creds, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return creds, fmt.Errorf(
			"credentials not found and stdin is not a terminal: set %s and %s",
			cfg.Auth.UsernameEnv, cfg.Auth.PasswordEnv)
	}
	if creds.Username == "" {
		fmt.Fprintf(os.Stderr, "Username (%s not set): ", cfg.Auth.UsernameEnv)
		fmt.Fscanln(os.Stdin, &creds.Username)
	}
	if creds.Password == "" {
		fmt.Fprint(os.Stderr, "Password: ")
		blob, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return creds, fmt.Errorf("read password: %w", err)
		}
		creds.Password = string(blob)
	}
	return creds, nil
}

// Authenticate returns everything the workers need to start logged in.
func Authenticate(host *browser.Host, cfg config.Config, allowPrompt bool) (Result, error) {
	patterns := cfg.Auth.LoginPathPatterns
	// Extra headers apply to every mode, not only bearer: staging proxies and
	// preview hosts routinely want a header on each request.
	headers := map[string]string{}
	for key, value := range cfg.Auth.ExtraHeaders {
		headers[key] = value
	}
	switch cfg.Auth.Mode {
	case "none":
		return Result{Headers: headers, Note: "no auth"}, nil
	case "storage":
		if _, err := os.Stat(cfg.Auth.StorageState); err != nil {
			return Result{}, fmt.Errorf("storage state file not found: %s", cfg.Auth.StorageState)
		}
		return Result{StatePath: cfg.Auth.StorageState, Headers: headers, Note: "reused storage state"}, nil
	case "bearer":
		token := os.Getenv(cfg.Auth.PasswordEnv)
		if token == "" {
			return Result{}, fmt.Errorf("bearer token not found: set %s", cfg.Auth.PasswordEnv)
		}
		if _, ok := headers["Authorization"]; !ok {
			headers["Authorization"] = "Bearer " + token
		}
		return Result{Headers: headers, Note: "bearer header"}, nil
	}

	creds, err := ResolveCredentials(cfg, allowPrompt)
	if err != nil {
		return Result{}, err
	}
	if cfg.Auth.Mode == "basic" {
		return Result{
			Creds:   &playwright.HttpCredentials{Username: creds.Username, Password: creds.Password},
			Headers: headers,
			Note:    "http basic",
		}, nil
	}

	ctx, err := host.NewContext("", nil, nil)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = ctx.Close() }()
	statePath, err := FormLogin(ctx, cfg, creds)
	if err != nil {
		return Result{}, err
	}
	_ = patterns
	return Result{StatePath: statePath, Headers: headers, Note: "form login ok"}, nil
}

func FormLogin(ctx playwright.BrowserContext, cfg config.Config, creds Credentials) (string, error) {
	patterns := cfg.Auth.LoginPathPatterns
	page, err := ctx.NewPage()
	if err != nil {
		return "", err
	}
	defer func() { _ = page.Close() }()
	page.SetDefaultTimeout(12000)

	loginURL := cfg.Auth.LoginURL
	if discover.Absolute(cfg.Run.BaseURL, loginURL) != "" {
		loginURL = discover.Absolute(cfg.Run.BaseURL, loginURL)
	}
	response, err := page.Goto(loginURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	})
	if err != nil {
		return "", fmt.Errorf("open login page %s: %w", loginURL, err)
	}
	if response != nil && response.Status() >= 400 {
		return "", fmt.Errorf("login page returned HTTP %d: %s", response.Status(), loginURL)
	}

	passwordSelector := cfg.Auth.PasswordField
	if passwordSelector == "" {
		passwordSelector = "input[type=password]"
	}
	password := page.Locator(passwordSelector).First()
	if count, _ := page.Locator(passwordSelector).Count(); count == 0 {
		if !discover.IsLoginURL(page.URL(), patterns) {
			return saveState(ctx, cfg)
		}
		return "", fmt.Errorf("no password field found on %s", page.URL())
	}

	selectors := usernameSelectors
	if cfg.Auth.UsernameField != "" {
		selectors = []string{cfg.Auth.UsernameField}
	}
	username := firstVisible(page, selectors)
	if username == nil {
		return "", fmt.Errorf("no username field found on %s", page.URL())
	}
	if err := username.Fill(creds.Username); err != nil {
		return "", fmt.Errorf("fill username: %w", err)
	}
	if err := password.Fill(creds.Password); err != nil {
		return "", fmt.Errorf("fill password: %w", err)
	}

	submitted := false
	if cfg.Auth.SubmitSelector != "" {
		submit := page.Locator(cfg.Auth.SubmitSelector).First()
		if count, _ := page.Locator(cfg.Auth.SubmitSelector).Count(); count > 0 {
			if err := submit.Click(); err != nil {
				return "", fmt.Errorf("click submit: %w", err)
			}
			submitted = true
		}
	}
	if !submitted {
		form := password.Locator("xpath=ancestor::form[1]")
		if count, _ := form.Count(); count > 0 {
			button := form.Locator("button[type=submit], input[type=submit], button:not([type=button]):not([type=reset])")
			if count, _ := button.Count(); count > 0 {
				if err := button.First().Click(); err != nil {
					return "", fmt.Errorf("click login button: %w", err)
				}
				submitted = true
			}
		}
	}
	if !submitted {
		if err := password.Press("Enter"); err != nil {
			return "", fmt.Errorf("press enter: %w", err)
		}
	}

	if !waitOffLogin(page, patterns, 15*time.Second) {
		message := ""
		if box := page.Locator("[role=alert], .error, .alert, [class*=error i], [id*=error i]").First(); box != nil {
			if count, _ := box.Count(); count > 0 {
				if text, err := box.InnerText(); err == nil {
					message = strings.TrimSpace(text)
				}
			}
		}
		if message != "" {
			return "", fmt.Errorf("still on %s after submit: %s", page.URL(), message)
		}
		return "", fmt.Errorf("still on %s after submit", page.URL())
	}
	if cfg.Auth.SuccessURLContains != "" && !strings.Contains(page.URL(), cfg.Auth.SuccessURLContains) {
		return "", fmt.Errorf("login landed on %s, expected URL containing %q",
			page.URL(), cfg.Auth.SuccessURLContains)
	}
	return saveState(ctx, cfg)
}

func saveState(ctx playwright.BrowserContext, cfg config.Config) (string, error) {
	state, err := ctx.StorageState()
	if err != nil {
		return "", fmt.Errorf("capture storage state: %w", err)
	}
	blob, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", err
	}
	path := cfg.Auth.StorageState
	if path == "" {
		path = ".afford/state/session.json"
	}
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}

func firstVisible(page playwright.Page, selectors []string) playwright.Locator {
	for _, selector := range selectors {
		locator := page.Locator(selector)
		count, err := locator.Count()
		if err != nil {
			continue
		}
		for i := 0; i < count && i < 5; i++ {
			candidate := locator.Nth(i)
			visible, _ := candidate.IsVisible()
			editable, _ := candidate.IsEditable()
			if visible && editable {
				return candidate
			}
		}
	}
	return nil
}

func waitOffLogin(page playwright.Page, patterns []string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		page.WaitForTimeout(150)
		if !discover.IsLoginURL(page.URL(), patterns) {
			return true
		}
		if count, _ := page.Locator("input[type=password]").Count(); count == 0 {
			return true
		}
	}
	return false
}
