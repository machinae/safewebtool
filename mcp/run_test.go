package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
	"github.com/machinae/safewebtool/policy"
)

// mustBind binds input that the test requires to be valid.
func mustBind(t *testing.T, input string) *call {
	t.Helper()
	c, resp := bindCall([]byte(input))
	if resp != nil {
		t.Fatalf("unexpected validation failure: %+v", resp)
	}
	return c
}

// isFatal covers exactly the errors that abort a run: a dead CDP
// connection or a dead call context. Per-command timeouts land in slots.
func TestIsFatal(t *testing.T) {
	if !isFatal(context.Background(), &browser.ErrChromeUnreachable{}) {
		t.Error("connection error should be fatal")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !isFatal(ctx, fmt.Errorf("clicking: %w", context.Canceled)) {
		t.Error("dead call context should be fatal")
	}
	if isFatal(context.Background(), fmt.Errorf("clicking: %w", context.DeadlineExceeded)) {
		t.Error("per-command timeout should not be fatal while the call context is alive")
	}
	if isFatal(context.Background(), errors.New("element not found")) {
		t.Error("ordinary action errors are not fatal")
	}
}

// A group whose tab cannot be resolved gets a group-level error and one
// skipped slot per action; the alignment invariant holds. ("browser not
// connected" is a plain error, not a typed ConnectionError, so it is
// not fatal; the wiring layer's IsConnected pre-flight owns that case.)
func TestRunUnresolvableTab(t *testing.T) {
	s := newTestServer()
	c := mustBind(t, `{"tabs": [{"tab": "abc1234", "output": "snapshot", "actions": [
		{"reason": "a", "action": "click", "id": "a3f81c1"},
		{"reason": "b", "action": "click", "id": "a3f81c2"}
	]}]}`)

	resp, err := s.run(context.Background(), c)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(resp.Tabs) != 1 {
		t.Fatalf("got %d tab responses; want 1", len(resp.Tabs))
	}
	tr := resp.Tabs[0]
	if tr.Error == "" {
		t.Error("want a group-level error for the unresolvable tab")
	}
	if len(tr.Results) != 2 {
		t.Fatalf("got %d result slots; want 2", len(tr.Results))
	}
	for i, slot := range tr.Results {
		if slot.Action != "click" || !strings.Contains(slot.Error, "skipped") {
			t.Errorf("slot %d: %+v; want a skipped click", i, slot)
		}
	}
}

// A "new" group whose open fails reports the error on the open slot and
// skips the rest.
func TestRunNewGroupOpenFails(t *testing.T) {
	s := newTestServer()
	c := mustBind(t, `{"tabs": [{"tab": "new", "output": "snapshot", "actions": [
		{"reason": "open it", "action": "open", "url": "https://example.com"},
		{"reason": "then click", "action": "click", "id": "a3f81c1"}
	]}]}`)

	resp, err := s.run(context.Background(), c)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	tr := resp.Tabs[0]
	if tr.Tab != "new" {
		t.Errorf("tab = %q; want the unresolved \"new\"", tr.Tab)
	}
	if tr.Results[0].Action != "open" || tr.Results[0].Error == "" {
		t.Errorf("open slot: %+v; want an error", tr.Results[0])
	}
	if !strings.Contains(tr.Results[1].Error, "skipped: the tab failed to open") {
		t.Errorf("click slot: %+v; want skipped", tr.Results[1])
	}
}

// Each action carries its own reason in the JSON grammar; logRun stamps
// it on that action's audit entry. (The ftp URL fails fast and
// connection-free, but the failure is logged like any action.)
func TestRunLogsPerActionReason(t *testing.T) {
	cl := &captureLogger{}
	s := NewServer(browser.New().WithURL("ws://127.0.0.1:1/fake"), WithLogger(cl))
	c := mustBind(t, `{"tabs": [{"tab": "new", "output": "snapshot", "actions": [
		{"reason": "open the docs", "action": "open", "url": "ftp://example.com"}
	]}]}`)

	_, _ = s.run(context.Background(), c)

	if len(cl.entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(cl.entries))
	}
	if got := cl.entries[0].Reason; got != "open the docs" {
		t.Fatalf("expected the action's reason on the entry, got %q", got)
	}
}

// Browser actions still produce their slot when they fail.
func TestRunListDisconnected(t *testing.T) {
	s := newTestServer()
	c := mustBind(t, `{"browser": [{"reason": "see open tabs", "action": "list"}]}`)

	resp, err := s.run(context.Background(), c)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(resp.Browser) != 1 {
		t.Fatalf("got %d browser slots; want 1", len(resp.Browser))
	}
	if resp.Browser[0].Action != "list" || resp.Browser[0].Error == "" {
		t.Errorf("list slot: %+v; want an error", resp.Browser[0])
	}
}

// authorizeCall is all-or-nothing across every action in the call.
func TestAuthorizeCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte("actions:\n  deny: [eval]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := policy.Load(context.Background(), path)
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	s := NewServer(browser.New().WithURL("ws://127.0.0.1:1/fake").WithPolicy(p))
	c := mustBind(t, `{"tabs": [{"tab": "abc1234", "output": "none", "actions": [
		{"reason": "a", "action": "click", "id": "a3f81c1"},
		{"reason": "b", "action": "eval", "js": "1+1"}
	]}]}`)

	err = s.authorizeCall(c)
	if err == nil || !strings.Contains(err.Error(), `action "eval" is blocked by policy`) {
		t.Errorf("got %v; want the eval denial", err)
	}
}

// TestTargetOf_FriendlyTargetOnError locks down the regression where failed
// browser actions logged a raw element id instead of the resolved
// element's role+name. The contract: when a typed BrowserError carries a
// friendly Sel (filled by el.String() at the post-resolve error site
// in browser/click.go etc.), targetOf must surface it on the error path.
func TestTargetOf_FriendlyTargetOnError(t *testing.T) {
	tests := []struct {
		name string
		id   string
		desc string
		err  error
		want string
	}{
		{
			name: "success path uses desc",
			id:   "880",
			desc: `combobox "Where to?"`,
			err:  nil,
			want: `combobox "Where to?"`,
		},
		{
			name: `success path with desc="ok" falls back to id`,
			id:   "880",
			desc: "ok",
			err:  nil,
			want: "880",
		},
		{
			name: "error path uses BrowserError.Selector when desc empty",
			id:   "880",
			desc: "",
			err:  &browser.ErrNotStable{Sel: `combobox "Where to?"`},
			want: `combobox "Where to?"`,
		},
		{
			name: "wrapped BrowserError still extracted via errors.As",
			id:   "880",
			desc: "",
			err:  fmt.Errorf("worker: %w", &browser.ErrObscured{Sel: `button "Submit"`, Blocker: `dialog "Cookie consent"`}),
			want: `button "Submit"`,
		},
		{
			name: "non-BrowserError on error path falls back to id",
			id:   "880",
			desc: "",
			err:  errors.New("scrolling element into view: rod boom"),
			want: "880",
		},
		{
			name: "BrowserError with empty Sel falls back to id",
			id:   "880",
			desc: "",
			err:  &browser.ErrNotVisible{Sel: ""},
			want: "880",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := targetOf(tc.id, tc.desc, tc.err)
			if got != tc.want {
				t.Errorf("targetOf(%q, %q, %v) = %q, want %q", tc.id, tc.desc, tc.err, got, tc.want)
			}
		})
	}
}
