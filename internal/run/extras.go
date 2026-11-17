package run

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"

	"afford/internal/auth"
	"afford/internal/browser"
	"afford/internal/discover"
	"afford/internal/model"
	"afford/internal/probe"
	"afford/internal/routes"
)

func matchAny(patterns []string, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	for _, p := range patterns {
		if discover.MatchPath(p, u.Path) {
			return true
		}
	}
	return false
}

// orphanRoutes enumerates routes the app declares (sitemap, framework
// manifest, config) and returns those the link crawl never reached.
func (r *Runner) orphanRoutes() []string {
	rc := r.cfg.Routes
	if !rc.Sitemap && !rc.Manifest && len(rc.Extra) == 0 {
		return nil
	}
	res := routes.Enumerate(r.ctx, r.cfg, r.session.Headers)
	for _, e := range res.Errors {
		r.bump(r.notTested, "route enumeration: "+e, 1)
	}
	var out []string
	done := map[string]bool{}
	for _, rt := range res.Routes {
		u := discover.Normalize(rt.URL)
		if done[u] || !discover.InScope(u, r.cfg) {
			continue
		}
		done[u] = true
		r.mu.Lock()
		reached := r.seen[u]
		r.mu.Unlock()
		if reached {
			continue
		}
		f := model.NewFinding("E_ORPHAN_ROUTE", u)
		f.Detail["source"] = rt.Source
		r.store.AddFinding(f)
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

func (r *Runner) visited() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.pagesSeen...)
	sort.Strings(out)
	return out
}

// viewportPass revisits every page at each extra viewport. Capture only: no
// control is clicked, so the pass costs a load per page per viewport.
func (r *Runner) viewportPass() {
	for _, vp := range r.cfg.Run.Viewports {
		if len(vp) != 2 || r.ctx.Err() != nil {
			continue
		}
		for _, u := range r.visited() {
			if r.ctx.Err() != nil {
				return
			}
			r.captureAt(u, vp)
		}
	}
}

func (r *Runner) captureAt(u string, vp []int) {
	r.sessionMu.Lock()
	headers := map[string]string{"X-Afford-Run": r.runID}
	for k, v := range r.session.Headers {
		headers[k] = v
	}
	state, creds := r.session.StatePath, r.session.Creds
	r.sessionMu.Unlock()
	bctx, err := r.host.NewSizedContext(state, creds, headers, vp[0], vp[1])
	if err != nil {
		r.bump(r.notTested, "viewport pass: could not open context", 1)
		return
	}
	defer func() { _ = bctx.Close() }()
	page, _, err := browser.NewProbePage(bctx, r.cfg)
	if err != nil {
		r.bump(r.notTested, "viewport pass: could not open page", 1)
		return
	}
	defer func() { _ = page.Close() }()
	load := probe.Reset(page, r.cfg, u)
	if !load.OK {
		r.bump(r.notTested, "viewport pass: page did not load", 1)
		return
	}
	scan, err := discover.Scan(page)
	if err != nil {
		r.bump(r.notTested, "viewport pass: DOM scan failed", 1)
		return
	}
	r.bump(r.notTested, "viewport pass: pages captured", 1)
	size := fmt.Sprintf("%dx%d", vp[0], vp[1])
	if scan.OverflowX {
		f := model.NewFinding("E_OVERFLOW_X", u)
		f.Detail["viewport"] = size
		r.store.AddFinding(f)
	}
	sum := sha1.Sum([]byte(u + size))
	rel := "screenshots/" + hex.EncodeToString(sum[:])[:12] + "-" + size + ".png"
	masks := r.cfg.ScreenshotMasks()
	masks = append(masks, r.cfg.Visual.Mask...)
	if _, err := browser.Screenshot(page, filepath.Join(r.baseDir, rel), masks); err == nil {
		r.compareVisual(u, discover.Template(u), rel, vp)
	}
}

// controlKeys names each visible control by tag, type and accessible name, the
// way a person would tell two buttons apart. Sorted and de-duplicated.
func controlKeys(controls []model.Element) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range controls {
		if !c.Visible || c.Frame != 0 {
			continue
		}
		key := c.Tag + "[" + c.Type + "] " + c.Name
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func subtract(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if !in[x] {
			out = append(out, x)
		}
	}
	return out
}

// rolePass is a capture-only subrun per declared profile: it logs in as the
// profile, loads every page the primary role found, and compares what it sees.
// Two things come out. A page matching the profile's deny list that still loads
// is E_AUTHZ_REACHABLE (the menu hides the route, the server does not enforce
// it). A page that loads for both roles with different controls is an
// E_ROLE_AFFORDANCE_DIFF, an informational record of what each role is offered.
func (r *Runner) rolePass() {
	for _, prof := range r.cfg.Auth.Profiles {
		if r.ctx.Err() != nil {
			return
		}
		pcfg := r.cfg
		pcfg.Auth.Mode = prof.Mode
		pcfg.Auth.UsernameEnv, pcfg.Auth.PasswordEnv = prof.UsernameEnv, prof.PasswordEnv
		if prof.StorageState != "" {
			pcfg.Auth.StorageState = prof.StorageState
		}
		session, err := auth.Authenticate(r.host, pcfg, false)
		if err != nil {
			r.bump(r.notTested, "role "+prof.Name+": authentication failed: "+err.Error(), 1)
			continue
		}
		for _, u := range r.visited() {
			if r.ctx.Err() != nil {
				return
			}
			loaded, controls := r.loadAs(u, session)
			if !loaded {
				r.bump(r.notTested, "role "+prof.Name+": page not available to this role", 1)
				continue
			}
			if matchAny(prof.Deny, u) {
				f := model.NewFinding("E_AUTHZ_REACHABLE", u)
				f.Detail["role"] = prof.Name
				f.Detail["deny"] = prof.Deny
				r.store.AddFinding(f)
			}
			r.mu.Lock()
			primary := r.affordance[u]
			r.mu.Unlock()
			onlyPrimary, onlyProfile := subtract(primary, controls), subtract(controls, primary)
			if len(onlyPrimary) > 0 || len(onlyProfile) > 0 {
				f := model.NewFinding("E_ROLE_AFFORDANCE_DIFF", u)
				f.Detail["role"] = prof.Name
				f.Detail["only_primary"] = onlyPrimary
				f.Detail["only_"+prof.Name] = onlyProfile
				r.store.AddFinding(f)
			}
		}
	}
}

// loadAs reports whether u loads for the session (no login redirect, status
// below 400, same URL) and the visible controls it shows.
func (r *Runner) loadAs(u string, session auth.Result) (bool, []string) {
	headers := map[string]string{"X-Afford-Run": r.runID}
	for k, v := range session.Headers {
		headers[k] = v
	}
	bctx, err := r.host.NewContext(session.StatePath, session.Creds, headers)
	if err != nil {
		return false, nil
	}
	defer func() { _ = bctx.Close() }()
	page, _, err := browser.NewProbePage(bctx, r.cfg)
	if err != nil {
		return false, nil
	}
	defer func() { _ = page.Close() }()
	load := probe.Reset(page, r.cfg, u)
	if !load.OK || load.Status >= 400 ||
		discover.IsLoginURL(load.FinalURL, r.cfg.Auth.LoginPathPatterns) ||
		discover.Normalize(load.FinalURL) != discover.Normalize(u) {
		return false, nil
	}
	scan, err := discover.Scan(page)
	if err != nil {
		return true, nil
	}
	return true, controlKeys(scan.Controls)
}
