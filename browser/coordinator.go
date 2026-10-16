package browser

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/machinae/safewebtool/internal/rod"
	"github.com/machinae/safewebtool/internal/rod/lib/proto"
)

// TabInfo is a point-in-time snapshot of a browser tab, returned by
// Tabs(). Display only — Prefix is the 7-char public handle used in
// commands; the full target ID is intentionally not exposed.
type TabInfo struct {
	// Prefix is the 7-char public handle (full target ID truncated).
	Prefix string
	// Title is the page title at enumeration time.
	Title string
	// URL is the page URL at enumeration time.
	URL string
	// Attached is true if this process has a live worker attached to the tab.
	// False for tabs opened in Chrome that we haven't targeted, and quarantined tabs.
	Attached bool
}

// Info returns a point-in-time snapshot of this tab's display metadata.
func (t *Tab) Info(ctx context.Context) (TabInfo, error) {
	var info TabInfo
	_, err := t.enqueue(ctx, func() (string, error) {
		got, err := t.doInfo(ctx)
		if err != nil {
			return "", err
		}
		info = got
		return "", nil
	})
	if err != nil {
		return TabInfo{}, err
	}
	return info, nil
}

func (t *Tab) doInfo(ctx context.Context) (TabInfo, error) {
	page := t.page.Context(ctx)
	res, err := proto.TargetGetTargetInfo{TargetID: proto.TargetTargetID(t.targetID)}.Call(page)
	if err != nil {
		return TabInfo{}, fmt.Errorf("getting page info: %w", err)
	}
	return TabInfo{
		Prefix:   t.ID(),
		Title:    res.TargetInfo.Title,
		URL:      res.TargetInfo.URL,
		Attached: t.ctx.Err() == nil,
	}, nil
}

// Tabs returns every user-visible tab Chrome currently has, as a
// point-in-time snapshot. Doubles as a Chrome-liveness check: if the
// websocket is hung, enumeration times out via ctx.
//
// Side effect: reconciles Browser.known against the live list, so
// entries for tabs the user closed in Chrome UI (or that Chrome killed
// for other reasons) are dropped.
func (b *Browser) Tabs(ctx context.Context) ([]TabInfo, error) {
	targets, err := b.enumerateTargets(ctx)
	if err != nil {
		return nil, err
	}
	attached := b.attachedSnapshot()
	tabs := make([]TabInfo, 0, len(targets))
	for _, t := range targets {
		id := string(t.TargetID)
		tabs = append(tabs, TabInfo{
			Prefix:   prefixOf(id),
			Title:    t.Title,
			URL:      t.URL,
			Attached: attached[id],
		})
	}
	return tabs, nil
}

// attachedSnapshot returns targetID → attached (in known with live ctx).
// Quarantined tabs report false and get reaped on the next enumeration.
func (b *Browser) attachedSnapshot() map[string]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]bool, len(b.known))
	for id, t := range b.known {
		out[id] = t.ctx.Err() == nil
	}
	return out
}

// Tab resolves a 7-char prefix (case-insensitive) to a *Tab. Every call
// enumerates live CDP targets for staleness and connection liveness —
// see plan §8.
//
// On first reference, the matching target is attached into Browser.known:
// the hijack router is installed and the worker goroutine is started.
// Subsequent references return the same *Tab.
//
// Returns ErrTabClosed if the prefix resolves to a tab whose Close() was
// called but did not complete cleanly (quarantine — see Tab.Close contract).
func (b *Browser) Tab(ctx context.Context, prefix string) (*Tab, error) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if len(prefix) != 7 {
		return nil, fmt.Errorf("tab prefix must be 7 chars, got %q", prefix)
	}

	targets, err := b.enumerateTargets(ctx)
	if err != nil {
		return nil, err
	}

	matches := matchPrefix(prefix, targets)
	if len(matches) == 0 {
		return nil, fmt.Errorf("tab not found: %s", prefix)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("ambiguous tab id %s: matches %d tabs", prefix, len(matches))
	}

	return b.attach(ctx, string(matches[0].TargetID))
}

// matchPrefix returns every target whose full ID starts with prefix (case-insensitive).
func matchPrefix(prefix string, targets []*proto.TargetTargetInfo) []*proto.TargetTargetInfo {
	var matches []*proto.TargetTargetInfo
	for _, t := range targets {
		id := string(t.TargetID)
		if len(id) >= 7 && strings.EqualFold(id[:7], prefix) {
			matches = append(matches, t)
		}
	}
	return matches
}

// OpenTab creates a new page in Chrome, attaches it (hijack router
// installed, worker started), and then routes the initial navigation
// THROUGH the worker so that any concurrent command landing on the
// freshly-visible tab queues behind it. Without that, a racing
// `Tab(newPrefix)` → action from another caller could drive t.page
// while OpenTab is still navigating — violating the "worker owns
// t.page" invariant.
//
// Hijack is installed by attachPage BEFORE the navigation enqueue, so
// the policy covers the first request.
func (b *Browser) OpenTab(ctx context.Context, rawURL string) (*Tab, ActionResult, error) {
	if err := checkURL(rawURL); err != nil {
		return nil, ActionResult{}, err
	}
	if err := b.checkTabLimit(ctx); err != nil {
		return nil, ActionResult{}, err
	}

	rod, err := b.getRod()
	if err != nil {
		return nil, ActionResult{}, err
	}
	// Background: the default createTarget opens a foreground tab and calls
	// WebContents::Focus(), which raises the Chrome window and takes OS focus
	// from whatever the user was doing. Background=true lands the tab in the
	// strip inactive, raising nothing. The new tab starts hidden and
	// throttled; initPage's focus emulation un-throttles it at attach.
	// Context bounds the create/attach round trips only; the page's
	// lifetime derives from the browser's root ctx (fork PageFromTarget),
	// so a timeout here cannot kill the page for later callers.
	page, err := rod.Context(ctx).Page(proto.TargetCreateTarget{URL: "about:blank", Background: true})
	if err != nil {
		return nil, ActionResult{}, fmt.Errorf("creating new tab: %w", err)
	}

	tab, err := b.attachPage(ctx, page)
	if err != nil {
		return nil, ActionResult{}, err
	}

	res, err := tab.act(ctx, "open", func() (string, error) {
		return tab.doOpen(ctx, rawURL)
	})
	if err != nil {
		// The tab was never exposed to the caller, so a failed open must
		// not leave a blank attached tab consuming the cap. The navigation
		// error is the one the caller needs; closeAttached fails only on a
		// dead ctx, where its quarantine contract already applies.
		_ = b.closeAttached(ctx, tab)
		return nil, ActionResult{}, err
	}
	return tab, res, nil
}

// checkTabLimit rejects opening a new tab when maxTabs tabs are already
// attached. The count may include tabs the user closed in Chrome UI that
// have not been reconciled away yet, so an at-limit count gets one
// reconciliation pass (enumerateTargets) before the final verdict.
func (b *Browser) checkTabLimit(ctx context.Context) error {
	if b.maxTabs <= 0 || b.attachedCount() < b.maxTabs {
		return nil
	}
	if _, err := b.enumerateTargets(ctx); err != nil {
		return err
	}
	if n := b.attachedCount(); n >= b.maxTabs {
		return fmt.Errorf("tab limit reached: %d tabs attached (max %d); close a tab before opening another", n, b.maxTabs)
	}
	return nil
}

// attachedCount returns the number of tabs currently in known.
func (b *Browser) attachedCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.known)
}

// CloseTab resolves the prefix and closes the tab.
func (b *Browser) CloseTab(ctx context.Context, prefix string) error {
	tab, err := b.Tab(ctx, prefix)
	if err != nil {
		return err
	}
	return b.closeAttached(ctx, tab)
}

// closeAttached signals the tab's worker to stop, stops the hijack
// router, sends Target.closeTarget, and removes the entry from
// Browser.known.
//
// If Tab.Close(ctx) returns a ctx error, the destructive teardown is
// SKIPPED per the Close contract: tearing down the router or target
// while an fn is still live against t.page creates a double-ownership
// race outside t.ctx's reach. The entry stays in Browser.known with
// t.ctx cancelled; future Tab() lookups return ErrTabClosed, and
// reconciliation reaps the entry once Chrome no longer lists the target.
func (b *Browser) closeAttached(ctx context.Context, tab *Tab) error {
	if err := tab.Close(ctx); err != nil {
		// Quarantine: worker may still be running fn against t.page.
		// Do not proceed with destructive cleanup.
		return err
	}

	// Worker has exited; no fn can be in flight. Safe to tear down
	// router and target. Router.Stop() issues Fetch.disable (CDP) — run
	// it OUTSIDE b.mu under t.routerMu so a hung Chrome cannot stall
	// other coordinator ops. Removal from known is a pure map op and
	// stays under b.mu.
	tab.routerMu.Lock()
	if tab.router != nil {
		_ = tab.router.Stop()
		tab.router = nil
	}
	tab.routerMu.Unlock()

	b.mu.Lock()
	delete(b.known, tab.targetID)
	b.mu.Unlock()

	// Best-effort CDP close. Failure is ignored — the target may already
	// be gone (e.g., user closed it manually between Tab() and here), or
	// the browser may have been shut down since we started.
	if rod, err := b.getRod(); err == nil {
		_, _ = proto.TargetCloseTarget{
			TargetID: proto.TargetTargetID(tab.targetID),
		}.Call(rod.Context(ctx))
	}

	return nil
}

// enumerateTargets returns all live user-visible Page targets and
// reconciles Browser.known against them as a side effect: any entry
// whose full target ID is no longer in the live set is dropped.
//
// Bounded by ctx; a hung websocket surfaces as a timeout error here.
func (b *Browser) enumerateTargets(ctx context.Context) ([]*proto.TargetTargetInfo, error) {
	rod, err := b.getRod()
	if err != nil {
		return nil, err
	}
	res, err := proto.TargetGetTargets{}.Call(rod.Context(ctx))
	if err != nil {
		return nil, newConnError(err)
	}

	liveIDs := make(map[string]struct{}, len(res.TargetInfos))
	userTabs := make([]*proto.TargetTargetInfo, 0, len(res.TargetInfos))
	for _, t := range res.TargetInfos {
		liveIDs[string(t.TargetID)] = struct{}{}
		if isUserTab(t) {
			userTabs = append(userTabs, t)
		}
	}

	b.mu.Lock()
	for id := range b.known {
		if _, ok := liveIDs[id]; !ok {
			delete(b.known, id)
		}
	}
	b.mu.Unlock()

	return userTabs, nil
}

// attach returns the *Tab for targetID, creating one on first reference
// (starting the worker, installing hijack). A cancelled existing entry
// returns ErrTabClosed — the tab is quarantined and must not be handed
// back out for use.
//
// Hijack install runs OUTSIDE b.mu under t.routerMu. b.mu is only held
// briefly — to check/insert into known — never across a Rod/CDP call.
// Callers who find the tab already in known wait on t.hijackReady
// (ctx-bounded) so no Tab is handed back before its policy is in effect.
func (b *Browser) attach(ctx context.Context, targetID string) (*Tab, error) {
	b.mu.Lock()
	if tab, ok := b.known[targetID]; ok {
		cancelled := tab.ctx.Err() != nil
		b.mu.Unlock()
		if cancelled {
			return nil, &ErrTabClosed{}
		}
		if err := waitHijackReady(ctx, tab); err != nil {
			return nil, err
		}
		return tab, nil
	}
	b.mu.Unlock()

	rod, err := b.getRod()
	if err != nil {
		return nil, err
	}
	page, err := rod.Context(ctx).PageFromTarget(proto.TargetTargetID(targetID))
	if err != nil {
		return nil, fmt.Errorf("attaching to target %s: %w", targetID, err)
	}

	b.mu.Lock()
	// Shutdown may have fired between our getRod() and now. If so, bail
	// rather than leak a worker+Tab into a fresh post-shutdown known map.
	if b.rod.Load() == nil {
		b.mu.Unlock()
		return nil, fmt.Errorf("browser not connected")
	}
	// Recheck: another goroutine may have attached the same target while
	// we were outside the lock. Our newTab allocation is orphaned but
	// harmless — the worker is never started, the struct is GC'd.
	if tab, ok := b.known[targetID]; ok {
		cancelled := tab.ctx.Err() != nil
		b.mu.Unlock()
		if cancelled {
			return nil, &ErrTabClosed{}
		}
		if err := waitHijackReady(ctx, tab); err != nil {
			return nil, err
		}
		return tab, nil
	}

	tab := newTab(targetID, page)
	go tab.run()
	b.known[targetID] = tab
	b.mu.Unlock()

	// Safe to drive the page outside the worker: nothing can reach this tab
	// until ensureHijacked closes hijackReady. The install runs detached so
	// its Fetch.enable round trip cannot hang this call past ctx; a stuck
	// installer leaves the tab unready and later callers' waits stay
	// ctx-bounded the same way.
	initPage(ctx, page)
	go b.ensureHijacked(tab)
	if err := waitHijackReady(ctx, tab); err != nil {
		return nil, err
	}
	return tab, nil
}

// attachPage is the OpenTab attach path. The *rod.Page was just
// created so its target ID is fresh, but a concurrent enumeration
// could still race us into known — recheck and fall back to waiting on
// the winner's hijackReady in that case.
func (b *Browser) attachPage(ctx context.Context, page *rod.Page) (*Tab, error) {
	targetID := string(page.TargetID)

	b.mu.Lock()
	// Shutdown may have fired between OpenTab's getRod() and now.
	if b.rod.Load() == nil {
		b.mu.Unlock()
		return nil, fmt.Errorf("browser not connected")
	}
	if existing, ok := b.known[targetID]; ok {
		cancelled := existing.ctx.Err() != nil
		b.mu.Unlock()
		if cancelled {
			return nil, &ErrTabClosed{}
		}
		if err := waitHijackReady(ctx, existing); err != nil {
			return nil, err
		}
		return existing, nil
	}
	tab := newTab(targetID, page)
	go tab.run()
	b.known[targetID] = tab
	b.mu.Unlock()

	// Safe to drive the page outside the worker: nothing can reach this tab
	// until ensureHijacked closes hijackReady. The install runs detached so
	// its Fetch.enable round trip cannot hang this call past ctx; a stuck
	// installer leaves the tab unready and later callers' waits stay
	// ctx-bounded the same way.
	initPage(ctx, page)
	go b.ensureHijacked(tab)
	if err := waitHijackReady(ctx, tab); err != nil {
		return nil, err
	}
	return tab, nil
}

// waitHijackReady blocks until the tab's first-install completes or ctx
// fires. A stuck installer cannot hang racing callers — they exit with
// ctx.Err() and the caller's request-level timeout applies.
func waitHijackReady(ctx context.Context, t *Tab) error {
	select {
	case <-t.hijackReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Version returns Chrome's product version string (e.g. "Chrome/120.0.6099.109")
// via the CDP Browser.getVersion call. It is the cheapest available
// round-trip, used by callers as a liveness probe: a dead websocket
// surfaces as a ConnectionError, the same as every other coordinator method.
func (b *Browser) Version(ctx context.Context) (string, error) {
	rod, err := b.getRod()
	if err != nil {
		return "", err
	}
	res, err := proto.BrowserGetVersion{}.Call(rod.Context(ctx))
	if err != nil {
		return "", newConnError(err)
	}
	return res.Product, nil
}

// targetInfo returns CDP target info for a single target, bounded by ctx.
func (b *Browser) targetInfo(ctx context.Context, targetID string) (*proto.TargetTargetInfo, error) {
	rod, err := b.getRod()
	if err != nil {
		return nil, err
	}
	res, err := proto.TargetGetTargetInfo{TargetID: proto.TargetTargetID(targetID)}.Call(rod.Context(ctx))
	if err != nil {
		return nil, err
	}
	return res.TargetInfo, nil
}

// isUserTab returns true if info describes a user-visible page target
// (not DevTools, not an internal chrome:// page, not about:*).
func isUserTab(info *proto.TargetTargetInfo) bool {
	if info.Type != proto.TargetTargetInfoTypePage {
		return false
	}
	if strings.HasPrefix(info.URL, "devtools://") ||
		strings.HasPrefix(info.URL, "about:") ||
		strings.HasPrefix(info.URL, "chrome://") {
		return false
	}
	return true
}

// prefixOf returns the 7-char public handle for a CDP target ID,
// lowercased (CDP target IDs are uppercase hex; the public handle is
// always lowercase, matching case-insensitively on input). Guarded
// against short IDs so test fixtures cannot trigger an index panic.
func prefixOf(targetID string) string {
	if len(targetID) < 7 {
		return strings.ToLower(targetID)
	}
	return strings.ToLower(targetID[:7])
}

// checkURL validates a URL is safe to navigate to. Only http, https,
// and about:blank are allowed — the policy layer enforces finer-grained
// rules on top.
func checkURL(rawURL string) error {
	if rawURL == "about:blank" {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL %s: %w", rawURL, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("URL scheme %s is not allowed: only http and https URLs are supported", parsed.Scheme)
	}
	return nil
}
