// Package fixtures runs the operator's data lifecycle hooks around a run.
//
// afford cannot know how to seed an arbitrary application, so it does not try:
// it runs a command the operator wrote, with the run id in the environment, and
// runs the teardown even when the crawl fails.
package fixtures

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"afford/internal/config"
)

// Hooks runs setup and teardown for one run.
type Hooks struct {
	cfg   config.FixturesConfig
	env   []string
	ran   bool
	Notes []string
}

// New prepares the hooks. RunID and BaseURL reach the commands as environment
// variables so seed data can be namespaced and cleaned by run.
func New(cfg config.FixturesConfig, runID, baseURL string) *Hooks {
	return &Hooks{cfg: cfg, env: append(os.Environ(),
		"AFFORD_RUN_ID="+runID, "AFFORD_BASE_URL="+baseURL)}
}

func (h *Hooks) exec(ctx context.Context, name, command string) error {
	timeout := time.Duration(h.cfg.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", command)
	cmd.Env = h.env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	tail := strings.TrimSpace(out.String())
	if len(tail) > 600 {
		tail = "…" + tail[len(tail)-600:]
	}
	if err != nil {
		return fmt.Errorf("%s hook failed: %v\n%s", name, err, tail)
	}
	return nil
}

// Setup runs the setup command. A failing setup means the data the crawl would
// see is wrong, so the run must not start.
func (h *Hooks) Setup(ctx context.Context) error {
	if strings.TrimSpace(h.cfg.Setup) == "" {
		return nil
	}
	h.ran = true
	return h.exec(ctx, "setup", h.cfg.Setup)
}

// Teardown runs the teardown command if setup ran, on a fresh context so a
// cancelled run still cleans up.
func (h *Hooks) Teardown() error {
	if strings.TrimSpace(h.cfg.Teardown) == "" || !h.ran {
		return nil
	}
	return h.exec(context.Background(), "teardown", h.cfg.Teardown)
}
