package browser

import (
	"context"
	"fmt"
	"strings"

	"github.com/machinae/safewebtool/config"
)

// ActionResult is the structured result for browser actions that mutate tab
// state. Action methods leave Snapshot empty; the caller decides when to
// capture one (Tab.Snapshot) and fills it in.
type ActionResult struct {
	Action   string
	TabID    string
	Desc     string
	Snapshot string
}

func (r ActionResult) String() string {
	var parts []string
	if r.Action != "" {
		parts = append(parts, fmt.Sprintf("action: %s", r.Action))
	}
	if r.TabID != "" {
		parts = append(parts, fmt.Sprintf("tab: %s", r.TabID))
	}
	if r.Desc != "" {
		parts = append(parts, r.Desc)
	}
	if r.Snapshot != "" {
		parts = append(parts, r.Snapshot)
	}
	return strings.Join(parts, "\n")
}

// act runs a mutating tab action as one worker job and returns its
// confirmation.
//
// fn must be the raw do* implementation, not a public action method. Public
// action methods call act/enqueue themselves, so using one here would
// deadlock on the tab worker.
func (t *Tab) act(ctx context.Context, action string, fn func() (string, error)) (ActionResult, error) {
	job := fn
	// In demo mode, bring this tab to the foreground before the action runs so
	// screen recordings show it happening in the right tab, and so an occluded,
	// throttled renderer doesn't queue synthetic input past our settle waits.
	// Bundled into one worker job so it stays serialized with the action.
	// Best-effort: a foregrounding failure must not fail the action itself.
	// Skipped for "activate" so an explicit Activate doesn't pre-activate.
	if config.AppConfig.DemoMode && action != "activate" {
		job = func() (string, error) {
			_, _ = t.doActivate(ctx)
			return fn()
		}
	}
	desc, err := t.enqueue(ctx, job)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Action: action, TabID: t.ID(), Desc: desc}, nil
}
