package browser

import (
	"context"
	"fmt"

	"github.com/machinae/safewebtool/internal/rod/lib/proto"
)

// Open navigates this tab to the given URL.
func (t *Tab) Open(ctx context.Context, url string) (ActionResult, error) {
	if err := checkURL(url); err != nil {
		return ActionResult{}, err
	}
	return t.act(ctx, "open", func() (string, error) { return t.doOpen(ctx, url) })
}

func (t *Tab) doOpen(ctx context.Context, url string) (string, error) {
	if err := waitPageLoad(ctx, t.page, func() (proto.NetworkLoaderID, error) {
		res, err := t.page.Context(ctx).NavigateWithResult(url)
		if err != nil {
			return "", err
		}
		return res.LoaderID, nil
	}); err != nil {
		return "", fmt.Errorf("navigating to %s: %w", url, err)
	}
	return "ok", nil
}

// Back navigates back in history.
func (t *Tab) Back(ctx context.Context) (ActionResult, error) {
	return t.act(ctx, "back", func() (string, error) { return t.doBack(ctx) })
}

func (t *Tab) doBack(ctx context.Context) (string, error) {
	if err := t.page.Context(ctx).NavigateBack(); err != nil {
		return "", fmt.Errorf("navigating back: %w", err)
	}
	// Wait for the page to settle so the back-navigation request has time
	// to reach the hijack router. The worker only surfaces ErrBlocked from
	// policyErr after fn returns (see tab_worker.go run loop), so
	// returning before the request is dispatched would let the action
	// report "ok" even though the policy blocked the request.
	waitPageSettle(ctx, t.page)
	return "ok", nil
}

// Forward navigates forward in history.
func (t *Tab) Forward(ctx context.Context) (ActionResult, error) {
	return t.act(ctx, "forward", func() (string, error) { return t.doForward(ctx) })
}

func (t *Tab) doForward(ctx context.Context) (string, error) {
	if err := t.page.Context(ctx).NavigateForward(); err != nil {
		return "", fmt.Errorf("navigating forward: %w", err)
	}
	// Wait for the page to settle so the forward-navigation request has
	// time to reach the hijack router. The worker only surfaces ErrBlocked
	// from policyErr after fn returns (see tab_worker.go run loop), so
	// returning before the request is dispatched would let the action
	// report "ok" even though the policy blocked the request.
	waitPageSettle(ctx, t.page)
	return "ok", nil
}

// Reload reloads the current page.
func (t *Tab) Reload(ctx context.Context) (ActionResult, error) {
	return t.act(ctx, "reload", func() (string, error) { return t.doReload(ctx) })
}

func (t *Tab) doReload(ctx context.Context) (string, error) {
	if err := t.page.Context(ctx).Reload(); err != nil {
		return "", fmt.Errorf("reloading page: %w", err)
	}
	// Wait for the page to settle so the reload request has time to reach
	// the hijack router. The worker only surfaces ErrBlocked from
	// policyErr after fn returns (see tab_worker.go run loop), so
	// returning before the request is dispatched would let the action
	// report "ok" even though the policy blocked the request.
	waitPageSettle(ctx, t.page)
	return "ok", nil
}

// Activate brings this tab to the foreground in Chrome.
func (t *Tab) Activate(ctx context.Context) (ActionResult, error) {
	return t.act(ctx, "activate", func() (string, error) { return t.doActivate(ctx) })
}

func (t *Tab) doActivate(ctx context.Context) (string, error) {
	if _, err := t.page.Context(ctx).Activate(); err != nil {
		return "", fmt.Errorf("activating tab: %w", err)
	}
	return "ok", nil
}
