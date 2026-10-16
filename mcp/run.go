package mcp

// Execution layer for the JSON tool grammar (plans/json-refactor.md).
// Runs a validated call and fills the response structs; render.go turns
// them into tool output. Wired in via handleToolCall.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"

	"github.com/machinae/safewebtool/browser"
	"github.com/machinae/safewebtool/config"
	"github.com/machinae/safewebtool/logger"
)

// run executes a validated call. Tab groups go first, then browser
// actions, so a list in the same call sees the tabs the groups opened.
// A fatal error aborts the run and discards the response; everything
// else lands in a slot and execution continues.
func (s *Server) run(ctx context.Context, c *call) (*response, error) {
	tabs, err := s.runGroups(ctx, c.Tabs)
	if err != nil {
		return nil, err
	}
	resp := &response{Tabs: tabs}
	for _, a := range c.Browser {
		actionCtx, cancel := context.WithTimeout(ctx, config.AppConfig.Timeout)
		slot, err := a.run(actionCtx, s)
		cancel()
		slot.Action = a.name()
		if err != nil {
			if isFatal(ctx, err) {
				return nil, err
			}
			slot.Error = err.Error()
		}
		resp.Browser = append(resp.Browser, slot)
	}
	return resp, nil
}

// isFatal reports whether an error makes the rest of the call
// pointless: the CDP connection is gone, or the parent call context is
// dead. Action methods run under per-command child contexts, so an
// action may return context.DeadlineExceeded while the parent ctx is
// still alive; that is a slot-level timeout and the batch can continue.
func isFatal(ctx context.Context, err error) bool {
	var cerr browser.ConnectionError
	if errors.As(err, &cerr) {
		return true
	}
	return ctx.Err() != nil
}

// runGroups is the golden rule in code: actions inside a tab run in
// series, tabs run in parallel. One goroutine per group, capped by
// MaxWorkers.
func (s *Server) runGroups(ctx context.Context, groups []tabActions) ([]tabResponse, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	workers := config.AppConfig.MaxWorkers
	if workers < 1 {
		workers = 1
	}
	out := make([]tabResponse, len(groups))
	errs := make([]error, len(groups))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, g := range groups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			out[i], errs[i] = s.runGroup(ctx, g)
		}()
	}
	wg.Wait()
	// Every goroutine finishes before we report; a dead connection or
	// context fails the others quickly on its own, no cancellation
	// needed. First fatal error wins.
	for _, err := range errs {
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// runGroup binds the group's tab and runs its actions in order,
// continuing past per-action errors; only a fatal error aborts the
// group. A "new" group is created by its first open; if that fails,
// the remaining actions are skipped. After the last action the group's
// declared output is rendered, unconditionally: there is no logic based
// on which actions ran.
func (s *Server) runGroup(ctx context.Context, g tabActions) (tabResponse, error) {
	res := tabResponse{Tab: g.Tab}

	var tab *browser.Tab
	rest := g.Actions
	if g.Tab == "new" {
		// Validation guarantees a "new" group starts with open.
		open := g.Actions[0].(openRequest)
		openCtx, cancel := context.WithTimeout(ctx, config.AppConfig.Timeout)
		t, _, err := s.browser.OpenTab(openCtx, open.URL)
		entry := logger.Entry{Action: "open", Target: open.URL, Error: err}
		if t != nil {
			entry.Data = t.ID()
		}
		s.logRun(openCtx, t, open.Reason, entry)
		cancel()
		slot := actionResponse{Action: "open", Target: open.URL}
		if err != nil {
			if isFatal(ctx, err) {
				return res, err
			}
			slot.Error = err.Error()
			res.Results = append(res.Results, slot)
			for _, a := range g.Actions[1:] {
				res.Results = append(res.Results, actionResponse{Action: a.name(), Error: "skipped: the tab failed to open"})
			}
			return res, nil
		}
		tab = t
		res.Tab = tab.ID()
		res.Results = append(res.Results, slot)
		rest = g.Actions[1:]
	} else {
		bindCtx, cancel := context.WithTimeout(ctx, config.AppConfig.Timeout)
		t, err := s.browser.Tab(bindCtx, g.Tab)
		cancel()
		if err != nil {
			if isFatal(ctx, err) {
				return res, err
			}
			res.Error = err.Error()
			for _, a := range g.Actions {
				res.Results = append(res.Results, actionResponse{Action: a.name(), Error: "skipped"})
			}
			return res, nil
		}
		tab = t
	}

	for _, a := range rest {
		actionCtx, cancel := context.WithTimeout(ctx, config.AppConfig.Timeout)
		slot, err := a.run(actionCtx, s, tab)
		cancel()
		slot.Action = a.name()
		if err != nil {
			if isFatal(ctx, err) {
				return res, err
			}
			slot.Error = err.Error()
		}
		res.Results = append(res.Results, slot)
	}

	if g.Output != outputNone {
		outCtx, cancel := context.WithTimeout(ctx, config.AppConfig.Timeout)
		filter := browser.OutputFilter{
			IncludeElementIDs: g.IncludeElementIDs,
			IncludeAXScope:    g.IncludeAXScope,
			ExcludeAXScope:    g.ExcludeAXScope,
			Match:             g.Match,
			Section:           g.Section,
		}
		var out string
		var err error
		switch g.Output {
		case outputSnapshot:
			out, err = tab.Snapshot(outCtx, browser.ModeDefault, filter)
		case outputMarkdown:
			out, err = tab.Extract(outCtx, false, filter)
		case outputHTML:
			out, err = tab.Extract(outCtx, true, filter)
		}
		cancel()
		switch {
		case err != nil && isFatal(ctx, err):
			return res, err
		case err != nil:
			res.Error = g.Output + " output failed: " + err.Error()
		default:
			res.Output = out
		}

		infoCtx, cancel := context.WithTimeout(ctx, config.AppConfig.Timeout)
		info, err := tab.Info(infoCtx)
		cancel()
		if err == nil {
			res.Title = info.Title
			res.URL = info.URL
		}
	}
	return res, nil
}

// authorizeCall gates every action name in the call through the action
// policy before anything executes. Like validation, a rejection rejects
// the whole call.
func (s *Server) authorizeCall(c *call) error {
	for _, a := range c.Browser {
		if err := s.authorizeAction(a.name()); err != nil {
			return err
		}
	}
	for _, g := range c.Tabs {
		for _, a := range g.Actions {
			if err := s.authorizeAction(a.name()); err != nil {
				return err
			}
		}
	}
	return nil
}

// alwaysAllowedActions bypass the action policy: read-only or cleanup
// verbs the model must never be locked out of. Group outputs are not
// actions and never pass through the policy.
var alwaysAllowedActions = map[string]bool{
	"list":  true,
	"close": true,
}

func (s *Server) authorizeAction(name string) error {
	if alwaysAllowedActions[name] {
		return nil
	}
	if p := s.browser.Policy(); p != nil && !p.IsActionAllowed(name) {
		return fmt.Errorf("action %q is blocked by policy", name)
	}
	return nil
}

// targetOf picks the friendliest identifier available for a slot's or
// log entry's target. Preference order: the success-path description,
// the typed BrowserError's Sel (which the browser layer fills with
// el.String() after resolution succeeds), then the raw element id
// the caller passed. Pass nil for err on the success path.
func targetOf(id, desc string, err error) string {
	if desc != "" && desc != "ok" {
		return desc
	}
	var be browser.BrowserError
	if errors.As(err, &be) && be.Selector() != "" {
		return be.Selector()
	}
	return id
}

// logRun writes one audit entry for an executed action. The reason
// travels on the action itself in the JSON grammar, so it is passed in
// explicitly.
func (s *Server) logRun(ctx context.Context, tab *browser.Tab, reason string, entry logger.Entry) {
	if s.logger == nil {
		return
	}
	entry.Reason = reason
	if tab != nil && entry.URL == "" {
		if info, err := tab.Info(ctx); err == nil {
			entry.URL = info.URL
		}
	}
	s.logger.Log(ctx, entry)
}

// --- Action run methods ---

// name returns the wire action name; promotion gives it to every action
// struct.
func (a actionRequest) name() string { return a.Action }

// open inside an existing group navigates the group's tab. The "new"
// case never reaches here; runGroup turns it into OpenTab.
func (o openRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	_, err := tab.Open(ctx, o.URL)
	s.logRun(ctx, tab, o.Reason, logger.Entry{Action: "open", Target: o.URL, Data: tab.ID(), Error: err})
	return actionResponse{Target: o.URL}, err
}

func (c closeRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	// Capture the URL before the close; afterwards Info has nothing to ask.
	entry := logger.Entry{Action: "close", Data: tab.ID()}
	if info, err := tab.Info(ctx); err == nil {
		entry.URL = info.URL
	}
	err := s.browser.CloseTab(ctx, tab.ID())
	entry.Error = err
	s.logRun(ctx, nil, c.Reason, entry)
	return actionResponse{}, err
}

func (b backRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	_, err := tab.Back(ctx)
	s.logRun(ctx, tab, b.Reason, logger.Entry{Action: "back", Data: tab.ID(), Error: err})
	return actionResponse{}, err
}

func (f forwardRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	_, err := tab.Forward(ctx)
	s.logRun(ctx, tab, f.Reason, logger.Entry{Action: "forward", Data: tab.ID(), Error: err})
	return actionResponse{}, err
}

func (r reloadRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	_, err := tab.Reload(ctx)
	s.logRun(ctx, tab, r.Reason, logger.Entry{Action: "reload", Data: tab.ID(), Error: err})
	return actionResponse{}, err
}

func (c clickRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	result, err := tab.Click(ctx, c.ID)
	target := targetOf(c.ID, result.Desc, err)
	s.logRun(ctx, tab, c.Reason, logger.Entry{Action: "click", Target: target, Data: tab.ID(), Error: err})
	return actionResponse{Target: target}, err
}

func (h hoverRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	result, err := tab.Hover(ctx, h.ID)
	target := targetOf(h.ID, result.Desc, err)
	s.logRun(ctx, tab, h.Reason, logger.Entry{Action: "hover", Target: target, Data: tab.ID(), Error: err})
	return actionResponse{Target: target}, err
}

func (f fillRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	result, err := tab.Fill(ctx, f.ID, f.Text)
	target := targetOf(f.ID, result.Desc, err)
	s.logRun(ctx, tab, f.Reason, logger.Entry{Action: "fill", Target: target, Data: tab.ID(), Error: err})
	return actionResponse{Target: target}, err
}

func (sel selectRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	result, err := tab.Select(ctx, sel.ID, sel.Option)
	target := targetOf(sel.ID, result.Desc, err)
	s.logRun(ctx, tab, sel.Reason, logger.Entry{Action: "select", Target: target, Data: tab.ID(), Error: err})
	return actionResponse{Target: target}, err
}

func (t typeRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	target := t.Key
	var err error
	if t.Text != "" {
		// The typed text may hold user content; log its size, not its value,
		// matching fill, which never logs the entered text.
		target = fmt.Sprintf("text (%d chars)", utf8.RuneCountInString(t.Text))
		_, err = tab.TypeText(ctx, t.Text)
	} else {
		_, err = tab.Type(ctx, t.Key)
	}
	s.logRun(ctx, tab, t.Reason, logger.Entry{Action: "type", Target: target, Data: tab.ID(), Error: err})
	return actionResponse{Target: target}, err
}

func (sc scrollRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	var err error
	var target string
	if sc.Pixels == 0 {
		_, err = tab.ScrollPage(ctx, false)
		target = "down 1 viewport"
	} else {
		_, err = tab.Scroll(ctx, sc.Pixels)
		target = fmt.Sprintf("%dpx", sc.Pixels)
	}
	s.logRun(ctx, tab, sc.Reason, logger.Entry{Action: "scroll", Target: target, Data: tab.ID(), Error: err})
	return actionResponse{Target: target}, err
}

func (u uploadRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	result, err := tab.Upload(ctx, u.ID, u.Files)
	target := targetOf(u.ID, result.Desc, err)
	s.logRun(ctx, tab, u.Reason, logger.Entry{Action: "upload", Target: target, Data: tab.ID(), Error: err})
	return actionResponse{Target: target}, err
}

func (e evalRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	out, err := tab.Eval(ctx, e.JS)
	s.logRun(ctx, tab, e.Reason, logger.Entry{Action: "eval", Target: e.JS, Data: tab.ID(), Error: err})
	return actionResponse{Result: out}, err
}

func (sc screenshotRequest) run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error) {
	b64, err := tab.Screenshot(ctx, browser.ScreenshotOpts{
		Full:      sc.Full,
		Element:   sc.ID,
		MaxHeight: sc.MaxHeight,
	})
	if err != nil {
		s.logRun(ctx, tab, sc.Reason, logger.Entry{Action: "screenshot", Data: tab.ID(), Error: err})
		return actionResponse{}, err
	}
	if sc.Path == "" {
		s.logRun(ctx, tab, sc.Reason, logger.Entry{Action: "screenshot", Target: "inline", Data: tab.ID()})
		return actionResponse{Target: "inline", image: b64}, nil
	}
	abs, werr := writeScreenshot(sc.Path, b64)
	s.logRun(ctx, tab, sc.Reason, logger.Entry{Action: "screenshot", Target: abs, Data: tab.ID(), Error: werr})
	return actionResponse{Target: abs}, werr
}

// writeScreenshot decodes the capture and writes it to path, returning
// the absolute path it wrote.
func writeScreenshot(path, b64 string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return path, fmt.Errorf("decoding screenshot: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return abs, fmt.Errorf("writing screenshot to %s: %w", abs, err)
	}
	return abs, nil
}

func (l listRequest) run(ctx context.Context, s *Server) (actionResponse, error) {
	tabs, err := s.browser.Tabs(ctx)
	s.logRun(ctx, nil, l.Reason, logger.Entry{Action: "list", Error: err})
	if err != nil {
		return actionResponse{}, err
	}
	entries := make([]tabEntry, 0, len(tabs))
	for _, t := range tabs {
		entries = append(entries, tabEntry{Tab: t.Prefix, Title: t.Title, URL: t.URL})
	}
	return actionResponse{Tabs: entries}, nil
}
