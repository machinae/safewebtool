package browser

import (
	"fmt"
	"log"

	"github.com/machinae/safewebtool/internal/rod"
	"github.com/machinae/safewebtool/internal/rod/lib/proto"
	"github.com/machinae/safewebtool/policy"
)

// ensureHijacked installs a hijack router on the tab if a policy is
// configured and the tab does not already have one. No-op if the policy
// is nil, disabled, or a router is already running. Closes t.hijackReady
// exactly once on every call path so callers blocked on it always
// unblock.
//
// Runs OUTSIDE b.mu and the worker's t.mu. Serialization with other
// hijack lifecycle operations (Stop, reinstall) is via t.routerMu.
func (b *Browser) ensureHijacked(t *Tab) {
	defer t.readyOnce.Do(func() { close(t.hijackReady) })

	b.mu.Lock()
	pol := b.policy
	b.mu.Unlock()

	t.routerMu.Lock()
	defer t.routerMu.Unlock()

	if pol == nil || !pol.Network.IsEnabled() || t.router != nil {
		return
	}
	t.router = setupHijackRouter(t, pol)
}

// resetHijack tears down the existing router (if any) and installs a
// fresh one from the current policy. Called by ReloadPolicy per-tab
// OUTSIDE b.mu, so a slow Chrome on one tab cannot stall reloads on
// other tabs. Does not reset t.hijackReady — that is a first-install
// signal and stays closed once reached.
func (b *Browser) resetHijack(t *Tab) error {
	t.routerMu.Lock()
	defer t.routerMu.Unlock()

	if t.router != nil {
		if err := t.router.Stop(); err != nil {
			return fmt.Errorf("stopping hijack router: %w", err)
		}
		t.router = nil
	}

	b.mu.Lock()
	pol := b.policy
	b.mu.Unlock()

	if pol == nil || !pol.Network.IsEnabled() {
		return nil
	}
	t.router = setupHijackRouter(t, pol)
	return nil
}

// setupHijackRouter builds and starts a hijack router on t.page using
// the given policy. The policy is captured by the handler closure —
// resetHijack stops this router before installing a new one, so the
// captured pointer can never reference a retired policy.
//
// Caller must hold t.routerMu.
func setupHijackRouter(t *Tab, pol *policy.Policy) *rod.HijackRouter {
	router := t.page.HijackRequests()

	handler := func(h *rod.Hijack) {
		allowed, rule, err := pol.Network.IsAllowed(h.Request.Req())
		if err != nil {
			// Fail closed: a request the rules cannot be checked against
			// is blocked. URL is left empty because the usual cause is a
			// URL Go cannot parse.
			log.Printf("policy error: %v", err)
			t.setPolicyErr(&ErrBlocked{Method: h.Request.Method(), Rule: "could not evaluate request: " + err.Error()})
			h.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
			return
		}
		if allowed {
			h.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}
		t.setPolicyErr(&ErrBlocked{
			Method: h.Request.Method(),
			URL:    h.Request.URL().String(),
			Rule:   rule.String(),
		})
		h.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
	}

	for _, pattern := range pol.Network.DenyPatterns() {
		if err := router.Add(pattern, "", handler); err != nil {
			log.Printf("hijack router add %q: %v", pattern, err)
		}
	}

	go func() {
		// Run blocks for the router's life; a non-nil error means the event
		// loop died with requests possibly still paused. Surfacing that
		// properly is the Tier 1.4 hijack work; log it until then.
		if err := router.Run(); err != nil {
			log.Printf("hijack router stopped: %v", err)
		}
	}()
	return router
}
