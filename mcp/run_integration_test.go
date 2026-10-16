//go:build integration

// Integration tests for the JSON execution layer. Run against a live
// Chrome with: go test -tags integration ./mcp/ -run TestRunChrome -v -count=1

package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/machinae/safewebtool/browser"
)

const runFixtureHTML = `<!DOCTYPE html><html><head><title>Run Fixture</title></head>
<body><h1>Hello</h1><a href="#one">one</a><a href="#two">two</a></body></html>`

// TestRunChrome covers the Phase 3 happy path end to end: a "new" group
// binds the tab its open created, actions run in order with their slots
// filled, the group's declared output and annotations are rendered, and
// a browser-scoped list after the groups sees the tab they opened.
func TestRunChrome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, runFixtureHTML)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b := browser.New()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("connect to Chrome: %v", err)
	}
	defer b.Close()
	s := NewServer(b)

	input := fmt.Sprintf(`{
		"browser": [{"reason": "see what is open now", "action": "list"}],
		"tabs": [{"tab": "new", "output": "snapshot", "actions": [
			{"reason": "open the fixture", "action": "open", "url": %q},
			{"reason": "count the links", "action": "eval", "js": "document.querySelectorAll('a').length"},
			{"reason": "scroll a bit", "action": "scroll", "pixels": 100}
		]}]
	}`, srv.URL)
	c := mustBind(t, input)
	resp, err := s.run(ctx, c)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	tr := resp.Tabs[0]
	if tr.Tab != "new" && len(tr.Tab) == 7 {
		defer func() {
			cc := mustBind(t, fmt.Sprintf(`{"tabs": [{"tab": %q, "output": "none", "actions": [{"reason": "clean up", "action": "close"}]}]}`, tr.Tab))
			_, _ = s.run(ctx, cc)
		}()
	}

	if tr.Error != "" {
		t.Fatalf("group error: %s", tr.Error)
	}
	if len(tr.Tab) != 7 {
		t.Errorf("tab = %q; want the resolved 7-char id", tr.Tab)
	}
	if len(tr.Results) != 3 {
		t.Fatalf("got %d result slots; want 3", len(tr.Results))
	}
	for i, want := range []string{"open", "eval", "scroll"} {
		if tr.Results[i].Action != want || tr.Results[i].Error != "" {
			t.Errorf("slot %d: %+v; want a clean %s", i, tr.Results[i], want)
		}
	}
	if tr.Results[1].Result != "2" {
		t.Errorf("eval result = %q; want \"2\" (two links)", tr.Results[1].Result)
	}
	if tr.Output == "" {
		t.Error("want the group's declared snapshot output")
	}
	if tr.Title != "Run Fixture" || tr.URL == "" {
		t.Errorf("annotations: title=%q url=%q", tr.Title, tr.URL)
	}

	// list ran after the groups, so the roster includes the new tab.
	roster := resp.Browser[0]
	if roster.Error != "" {
		t.Fatalf("list error: %s", roster.Error)
	}
	found := false
	for _, e := range roster.Tabs {
		if e.Tab == tr.Tab {
			found = true
		}
	}
	if !found {
		t.Errorf("roster %+v does not include the opened tab %s", roster.Tabs, tr.Tab)
	}
}

// TestRunChromeFormGroup checks that a group with output "none" stays
// status-only: no group-end output, no annotations.
func TestRunChromeFormGroup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Form</title></head><body><input id="name"></body></html>`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b := browser.New()
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("connect to Chrome: %v", err)
	}
	defer b.Close()
	s := NewServer(b)

	c := mustBind(t, fmt.Sprintf(`{"tabs": [{"tab": "new", "output": "snapshot", "actions": [
		{"reason": "open the form", "action": "open", "url": %q}
	]}]}`, srv.URL))
	resp, err := s.run(ctx, c)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	id := resp.Tabs[0].Tab
	if len(id) != 7 {
		t.Fatalf("open failed: %+v", resp.Tabs[0])
	}
	m := regexp.MustCompile(`\[(\d+)\] textbox`).FindStringSubmatch(resp.Tabs[0].Output)
	if m == nil {
		t.Fatalf("no textbox in snapshot:\n%s", resp.Tabs[0].Output)
	}
	inputID := m[1]
	defer func() {
		cc := mustBind(t, fmt.Sprintf(`{"tabs": [{"tab": %q, "output": "none", "actions": [{"reason": "clean up", "action": "close"}]}]}`, id))
		_, _ = s.run(ctx, cc)
	}()

	c2 := mustBind(t, fmt.Sprintf(`{"tabs": [{"tab": %q, "output": "none", "actions": [
		{"reason": "fill the name", "action": "type", "id": %q, "text": "Ada"}
	]}]}`, id, inputID))
	resp2, err := s.run(ctx, c2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	tr := resp2.Tabs[0]
	if tr.Results[0].Error != "" {
		t.Fatalf("type failed: %s", tr.Results[0].Error)
	}
	if tr.Output != "" {
		t.Error(`output "none" group should not carry a group-end output`)
	}

	// The same tab read action-less as markdown: attach and look.
	c3 := mustBind(t, fmt.Sprintf(`{"tabs": [{"tab": %q, "output": "markdown"}]}`, id))
	resp3, err := s.run(ctx, c3)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	tr3 := resp3.Tabs[0]
	if tr3.Error != "" || len(tr3.Results) != 0 {
		t.Fatalf("action-less group: %+v", tr3)
	}
	if tr3.Output == "" || tr3.Title != "Form" {
		t.Errorf("want markdown output and annotations, got %+v", tr3)
	}
}
