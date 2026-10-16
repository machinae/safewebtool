package mcp

import (
	"strings"
	"testing"
)

func TestBindCallClick(t *testing.T) {
	input := `{"tabs": [{"tab": "abc1234", "output": "snapshot", "actions": [
		{"reason": "open the pricing page", "action": "click", "id": "43821fa"},
		{"reason": "dismiss the cookie banner", "action": "click", "id": "43990fb"}
	]}]}`

	c, resp := bindCall([]byte(input))
	if resp != nil {
		t.Fatalf("unexpected validation failure: %+v", resp)
	}
	if len(c.Tabs) != 1 || c.Tabs[0].Tab != "abc1234" || len(c.Tabs[0].Actions) != 2 {
		t.Fatalf("got %+v; want one tab abc1234 with 2 actions", c.Tabs)
	}
	if c.Tabs[0].Output != "snapshot" {
		t.Errorf("got output %q; want snapshot", c.Tabs[0].Output)
	}
	a, ok := c.Tabs[0].Actions[1].(clickRequest)
	if !ok {
		t.Fatalf("got %T; want clickRequest", c.Tabs[0].Actions[1])
	}
	if a.ID != "43990fb" || a.Reason != "dismiss the cookie banner" {
		t.Errorf("got %+v", a)
	}
}

// An invalid call returns the response echo: every bad slot carries its
// error, valid slots stay clean, and no call is returned.
func TestBindCallInvalid(t *testing.T) {
	input := `{"tabs": [
		{"tab": "abc1234", "output": "snapshot", "actions": [
			{"reason": "click ok", "action": "click", "id": "43821fa"},
			{"reason": "typo field", "action": "click", "selectr": "43990"},
			{"action": "click", "id": "43991fb"}
		]},
		{"tab": "ABC1234", "output": "snapshot", "actions": [
			{"reason": "duplicate tab", "action": "click", "id": "a3f81c2"}
		]}
	]}`

	c, resp := bindCall([]byte(input))
	if c != nil || resp == nil {
		t.Fatalf("got call %+v, resp %+v; want nil call and a response", c, resp)
	}
	slots := resp.Tabs[0].Results
	if len(slots) != 3 {
		t.Fatalf("got %d result slots; want 3", len(slots))
	}
	if slots[0].Error != "" {
		t.Errorf("valid slot has error: %s", slots[0].Error)
	}
	if !strings.Contains(slots[1].Error, `unknown field "selectr"`) {
		t.Errorf("slot 1 error: %s", slots[1].Error)
	}
	if !strings.Contains(slots[2].Error, `missing "reason"`) {
		t.Errorf("slot 2 error: %s", slots[2].Error)
	}
	if !strings.Contains(resp.Tabs[1].Error, "appears twice") {
		t.Errorf("duplicate-tab group error: %s", resp.Tabs[1].Error)
	}
	for _, slot := range slots {
		if slot.Action != "click" {
			t.Errorf("slot echoes action %q; want click", slot.Action)
		}
	}
}

// Tab ids: a mixed-case id binds and is normalized to lowercase; a
// malformed id is rejected at validation, before anything executes.
func TestBindCallTabID(t *testing.T) {
	c, resp := bindCall([]byte(`{"tabs": [{"tab": "ABC12F4", "output": "snapshot", "actions": [
		{"reason": "click ok", "action": "click", "id": "43821fa"}
	]}]}`))
	if resp != nil {
		t.Fatalf("unexpected validation failure: %+v", resp)
	}
	if c.Tabs[0].Tab != "abc12f4" {
		t.Errorf("got tab %q; want lowercase abc12f4", c.Tabs[0].Tab)
	}

	c, resp = bindCall([]byte(`{"tabs": [{"tab": "not-a-tab", "output": "snapshot", "actions": [
		{"reason": "click ok", "action": "click", "id": "43821fa"}
	]}]}`))
	if c != nil || resp == nil {
		t.Fatalf("got call %+v; want a validation failure", c)
	}
	if !strings.Contains(resp.Tabs[0].Error, `must be "new" or a 7-char hex id`) {
		t.Errorf("group error: %s", resp.Tabs[0].Error)
	}
}

// The whole verb surface binds: list in the browser scope, a "new" group
// opened by its first action, and one of each field shape.
func TestBindCallAllActions(t *testing.T) {
	input := `{
		"browser": [{"reason": "see what tabs are open", "action": "list"}],
		"tabs": [
			{"tab": "new", "output": "markdown", "includeElementIds": ["43850ab"], "actions": [
				{"reason": "open the pricing page", "action": "open", "url": "https://acme.com/pricing"},
				{"reason": "fill the email field", "action": "fill", "id": "9c04e1d", "text": "ada@example.com"},
				{"reason": "choose the country", "action": "select", "id": "d7e02f3", "option": "United States"},
				{"reason": "submit the form", "action": "type", "key": "Enter"},
				{"reason": "scroll back up", "action": "scroll", "pixels": -500},
				{"reason": "count the links", "action": "eval", "js": "document.querySelectorAll('a').length"},
				{"reason": "clear the search box", "action": "fill", "id": "9c04e1d", "text": ""}
			]}
		]
	}`

	c, resp := bindCall([]byte(input))
	if resp != nil {
		t.Fatalf("unexpected validation failure: %+v", resp)
	}
	if len(c.Browser) != 1 {
		t.Fatalf("got %d browser actions; want 1", len(c.Browser))
	}
	if _, ok := c.Browser[0].(listRequest); !ok {
		t.Errorf("got %T; want listRequest", c.Browser[0])
	}
	if g := c.Tabs[0]; g.Output != "markdown" || len(g.IncludeElementIDs) != 1 || g.IncludeElementIDs[0] != "43850ab" {
		t.Errorf("group output: %+v", g)
	}
	actions := c.Tabs[0].Actions
	if len(actions) != 7 {
		t.Fatalf("got %d actions; want 7", len(actions))
	}
	if o := actions[0].(openRequest); o.URL != "https://acme.com/pricing" {
		t.Errorf("open url: %q", o.URL)
	}
	if s := actions[4].(scrollRequest); s.Pixels != -500 {
		t.Errorf("scroll pixels: %d", s.Pixels)
	}
}

// The group's output field is required and closed; element-id scope must
// be a hex id, not free text.
func TestBindCallOutput(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{
			name:  "missing output",
			input: `{"tabs": [{"tab": "abc1234", "actions": [{"reason": "a", "action": "click", "id": "a3f81c2"}]}]}`,
			want:  `missing "output"`,
		},
		{
			name:  "unknown output",
			input: `{"tabs": [{"tab": "abc1234", "output": "tree", "actions": [{"reason": "a", "action": "click", "id": "a3f81c2"}]}]}`,
			want:  `"output" must be`,
		},
		{
			name:  "includeElementIds must be hex ids",
			input: `{"tabs": [{"tab": "abc1234", "output": "snapshot", "includeElementIds": ["main"]}]}`,
			want:  `must be a 7-char hex element id`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, resp := bindCall([]byte(tc.input))
			if c != nil || resp == nil {
				t.Fatalf("got call %+v; want a validation failure", c)
			}
			if !strings.Contains(resp.Tabs[0].Error, tc.want) {
				t.Errorf("group error %q; want it to contain %q", resp.Tabs[0].Error, tc.want)
			}
		})
	}

	// An action-less group is valid: attach and look.
	c, resp := bindCall([]byte(`{"tabs": [{"tab": "abc1234", "output": "snapshot"}]}`))
	if resp != nil {
		t.Fatalf("unexpected validation failure: %+v", resp)
	}
	if len(c.Tabs[0].Actions) != 0 || c.Tabs[0].Output != "snapshot" {
		t.Errorf("got %+v; want an empty snapshot group", c.Tabs[0])
	}
}

// Element ids must be shaped like snapshot ids at bind time: a CSS
// selector or element text is rejected with a teaching error before any
// browser round trip, instead of missing resolution with a misleading
// "content changed" error.
func TestBindCallID(t *testing.T) {
	c, resp := bindCall([]byte(`{"tabs": [{"tab": "abc1234", "output": "snapshot", "actions": [
		{"reason": "click ok", "action": "click", "id": "#submit"}
	]}]}`))
	if c != nil || resp == nil {
		t.Fatalf("got call %+v; want a validation failure", c)
	}
	slot := resp.Tabs[0].Results[0]
	if !strings.Contains(slot.Error, "7-char hex element id") || !strings.Contains(slot.Error, "#submit") {
		t.Errorf("slot error %q; want it to name the shape rule and the bad id", slot.Error)
	}
}

// Scope errors are symmetric and per-verb rules fire with their examples.
func TestBindCallVerbRules(t *testing.T) {
	input := `{
		"browser": [{"reason": "wrong scope", "action": "click", "id": "a3f81c2"}],
		"tabs": [
			{"tab": "abc1234", "output": "none", "actions": [
				{"reason": "wrong scope", "action": "list"},
				{"reason": "xor", "action": "screenshot", "full": true, "id": "a3f81c2"},
				{"reason": "no files", "action": "upload", "id": "20fa7b8", "files": []},
				{"reason": "both key and text", "action": "type", "key": "Enter", "text": "hi"}
			]}
		]
	}`

	_, resp := bindCall([]byte(input))
	if resp == nil {
		t.Fatal("want a validation failure response")
	}
	if !strings.Contains(resp.Browser[0].Error, "tab action") {
		t.Errorf("browser slot: %s", resp.Browser[0].Error)
	}
	slots := resp.Tabs[0].Results
	for i, want := range []string{"browser action", "mutually exclusive", `requires "files"`, "exactly one of"} {
		if !strings.Contains(slots[i].Error, want) {
			t.Errorf("slot %d: got %q; want it to contain %q", i, slots[i].Error, want)
		}
	}
}

// Every response array matches its request array in length and order,
// including the results of a group that failed validation as a whole.
func TestBindCallAlignment(t *testing.T) {
	input := `{
		"browser": [{"reason": "no browser verbs yet", "action": "bogus"}],
		"tabs": [
			{"tab": "abc1234", "output": "none", "actions": [
				{"reason": "a", "action": "click", "id": "a3f81c1"},
				{"reason": "b", "action": "unknown"},
				{"reason": "c", "action": "click", "id": "a3f81c2"}
			]},
			{"tab": "abc1234", "output": "none", "actions": [
				{"reason": "d", "action": "click", "id": "a3f81c3"},
				{"reason": "e", "action": "click", "id": "a3f81c4"}
			]}
		]
	}`

	_, resp := bindCall([]byte(input))
	if resp == nil {
		t.Fatal("want a validation failure response")
	}
	if len(resp.Browser) != 1 {
		t.Errorf("got %d browser slots; want 1", len(resp.Browser))
	}
	if len(resp.Tabs) != 2 {
		t.Fatalf("got %d tab responses; want 2", len(resp.Tabs))
	}
	if resp.Tabs[1].Error == "" {
		t.Error("duplicate-tab group should carry a group-level error")
	}
	for i, want := range [][]string{{"click", "unknown", "click"}, {"click", "click"}} {
		results := resp.Tabs[i].Results
		if len(results) != len(want) {
			t.Fatalf("tabs[%d]: got %d result slots; want %d", i, len(results), len(want))
		}
		for j, action := range want {
			if results[j].Action != action {
				t.Errorf("tabs[%d] slot %d echoes %q; want %q", i, j, results[j].Action, action)
			}
		}
	}
}
