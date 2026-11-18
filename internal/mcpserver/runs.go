package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nyimbi/afford/internal/history"
	"github.com/nyimbi/afford/internal/model"
	"github.com/nyimbi/afford/internal/report"
)

type listRunsIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"how many recent runs to return (default 10)"`
}

type listRunsOut struct {
	Runs []history.Entry `json:"runs"`
}

func (s *Server) listRuns(_ context.Context, _ *mcp.CallToolRequest, in listRunsIn) (*mcp.CallToolResult, listRunsOut, error) {
	entries, err := history.Load(filepath.Join(s.cfg.Run.Out, "history.jsonl"))
	if err != nil {
		return nil, listRunsOut{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	if entries == nil {
		entries = []history.Entry{}
	}
	var b strings.Builder
	for i, e := range entries {
		var prev *history.Entry
		if i > 0 {
			prev = &entries[i-1]
		}
		fmt.Fprintf(&b, "%s %s: %s\n", e.RunID, e.BaseURL, history.Delta(e, prev))
	}
	if b.Len() == 0 {
		b.WriteString("no runs recorded")
	}
	return textResult(strings.TrimSpace(b.String())), listRunsOut{Runs: entries}, nil
}

type getRunIn struct {
	RunID string `json:"run_id" jsonschema:"a run id from list_runs or crawl_site"`
	Limit int    `json:"limit,omitempty" jsonschema:"most findings to return, worst first (default 25)"`
}

type getRunOut struct {
	Summary  model.Summary   `json:"summary"`
	Findings []model.Finding `json:"findings"`
	Omitted  int             `json:"omitted"`
}

var runIDPattern = regexp.MustCompile(`^[0-9A-Za-z_-]+$`)

func (s *Server) getRun(_ context.Context, _ *mcp.CallToolRequest, in getRunIn) (*mcp.CallToolResult, getRunOut, error) {
	if !runIDPattern.MatchString(in.RunID) {
		return nil, getRunOut{}, fmt.Errorf("run_id %q is not a run id", in.RunID)
	}
	blob, err := os.ReadFile(filepath.Join(s.cfg.Run.Out, "runs", in.RunID, "report.json"))
	if err != nil {
		// Crawls started over MCP keep their artifacts under mcp/.
		blob, err = os.ReadFile(filepath.Join(s.cfg.Run.Out, "mcp", "runs", in.RunID, "report.json"))
	}
	if err != nil {
		return nil, getRunOut{}, fmt.Errorf("no report for run %s", in.RunID)
	}
	var payload report.Payload
	if err := json.Unmarshal(blob, &payload); err != nil {
		return nil, getRunOut{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}
	findings := payload.Findings
	omitted := 0
	if len(findings) > limit {
		omitted = len(findings) - limit
		findings = findings[:limit]
	}
	if findings == nil {
		findings = []model.Finding{}
	}
	text := fmt.Sprintf("run %s: %d pages, %d findings (%d shown)", in.RunID, payload.Summary.PagesVisited, len(payload.Findings), len(findings))
	return textResult(text), getRunOut{Summary: payload.Summary, Findings: findings, Omitted: omitted}, nil
}

type getScreenshotIn struct {
	Path string `json:"path" jsonschema:"a screenshot path from a finding or result; must be inside the output directory"`
}

type getScreenshotOut struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// getScreenshot returns one PNG as an image block. It is a separate tool so an
// agent pays for pixels only when it asks, and it refuses any path outside the
// output directory.
func (s *Server) getScreenshot(_ context.Context, _ *mcp.CallToolRequest, in getScreenshotIn) (*mcp.CallToolResult, getScreenshotOut, error) {
	root, err := filepath.Abs(s.cfg.Run.Out)
	if err != nil {
		return nil, getScreenshotOut{}, err
	}
	root, _ = filepath.EvalSymlinks(root)
	path := in.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, getScreenshotOut{}, fmt.Errorf("no such screenshot")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, getScreenshotOut{}, fmt.Errorf("path is outside the output directory")
	}
	if strings.ToLower(filepath.Ext(path)) != ".png" {
		return nil, getScreenshotOut{}, fmt.Errorf("only .png screenshots are served")
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, getScreenshotOut{}, err
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: blob, MIMEType: "image/png"}}}
	return result, getScreenshotOut{Path: path, Bytes: len(blob)}, nil
}
