package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/machinae/safewebtool/browser"
	"github.com/machinae/safewebtool/logger"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func newTestServer() *Server {
	return NewServer(browser.New().WithURL("ws://127.0.0.1:1/fake"))
}

// captureLogger is an in-memory logger.Logger that records every Entry
// written via Log. Tests use it to assert what handlers stamped on the
// entry (Reason, Action, Error, etc.).
type captureLogger struct {
	entries []logger.Entry
}

func (c *captureLogger) Log(_ context.Context, e logger.Entry) {
	c.entries = append(c.entries, e)
}

func (c *captureLogger) Close() error { return nil }

// callTool builds a CallToolRequest for the webtool tool from a JSON
// envelope and invokes the handler directly, bypassing the transport.
func callTool(s *Server, envelope string) (*mcp.CallToolResult, error) {
	var req mcp.CallToolRequest
	req.Params.Name = "webtool"
	req.Params.Arguments = json.RawMessage(envelope)
	return s.handleToolCall(context.Background(), req)
}

// clickEnvelope is a minimal valid call used where the test only
// cares how far the handler gets, not what executes.
const clickEnvelope = `{"tabs": [{"tab": "abc1234", "output": "none", "actions": [
	{"reason": "test", "action": "click", "id": "a3f81c1"}
]}]}`

// resultText returns the first TextContent payload of a tool result, or
// fails the test if the result shape doesn't match what we expect.
func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("empty tool result")
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	return tc.Text
}

// The first call connects lazily. Concurrent calls against an
// unreachable browser each fail with the connect error and none hangs
// on the connect lock.
func TestHandleToolCall_ConnectFails(t *testing.T) {
	s := newTestServer()
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := callTool(s, clickEnvelope)
			if err != nil {
				t.Errorf("handleToolCall: %v", err)
				return
			}
			if !res.IsError || !strings.Contains(resultText(t, res), "Chrome is not running") {
				t.Errorf("expected the connect error, got: %+v", res)
			}
		}()
	}
	wg.Wait()
	if s.browser.IsConnected() {
		t.Fatal("browser reports connected after failed connects")
	}
}

// An invalid call is rejected under the banner before the policy or the
// browser are ever consulted; nothing executes.
func TestHandleToolCall_InvalidCall(t *testing.T) {
	s := newTestServer()

	res, err := callTool(s, `{"tabs": [{"tab": "abc1234", "output": "none", "actions": [
		{"reason": "typo", "action": "click", "selectr": "1"}
	]}]}`)
	if err != nil {
		t.Fatalf("handleToolCall: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true, got %+v", res)
	}
	text := resultText(t, res)
	if !strings.HasPrefix(text, "invalid call, nothing was executed:") {
		t.Fatalf("expected the banner, got: %s", text)
	}
	if !strings.Contains(text, `unknown field \"selectr\"`) {
		t.Fatalf("expected the slot error, got: %s", text)
	}
}

// schemaNode is the slice of JSON Schema the tests walk: enough to reach
// the action items and read their enums, properties, and required lists.
type schemaNode struct {
	Type       string                `json:"type"`
	Enum       []string              `json:"enum"`
	Properties map[string]schemaNode `json:"properties"`
	Required   []string              `json:"required"`
	Items      *schemaNode           `json:"items"`
}

// toolSchemaNodes unmarshals the embedded schema and returns the three
// item schemas the tests care about: tab group, tab action, browser action.
func toolSchemaNodes(t *testing.T) (group, tabAct, browserAct schemaNode) {
	t.Helper()
	var root schemaNode
	if err := json.Unmarshal(toolSchema, &root); err != nil {
		t.Fatalf("tool_schema.json: %v", err)
	}
	tabs, ok := root.Properties["tabs"]
	if !ok || tabs.Items == nil {
		t.Fatal(`schema missing "tabs" array with items`)
	}
	actions, ok := tabs.Items.Properties["actions"]
	if !ok || actions.Items == nil {
		t.Fatal(`schema missing "tabs" items "actions" array with items`)
	}
	browser, ok := root.Properties["browser"]
	if !ok || browser.Items == nil {
		t.Fatal(`schema missing "browser" array with items`)
	}
	return *tabs.Items, *actions.Items, *browser.Items
}

// jsonTags returns the JSON field names of a struct type, including
// fields promoted from embedded structs.
func jsonTags(t reflect.Type) []string {
	var tags []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			tags = append(tags, jsonTags(f.Type)...)
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag != "" && tag != "-" {
			tags = append(tags, tag)
		}
	}
	return tags
}

// The advertised schema must declare an items schema on both arrays
// (strict clients reject array parameters without one) requiring the
// envelope fields every action and group carries.
func TestToolSchemaArrayItems(t *testing.T) {
	group, tabAct, browserAct := toolSchemaNodes(t)

	if want := []string{"tab", "output"}; !slices.Equal(group.Required, want) {
		t.Errorf("tab group required = %v, want %v", group.Required, want)
	}
	if want := []string{"reason", "action"}; !slices.Equal(tabAct.Required, want) {
		t.Errorf("tab action required = %v, want %v", tabAct.Required, want)
	}
	if want := []string{"reason", "action"}; !slices.Equal(browserAct.Required, want) {
		t.Errorf("browser action required = %v, want %v", browserAct.Required, want)
	}
}

// The schema's action inventory must match bindCall's. With
// additionalProperties: false advertised, a field that exists in Go but
// not in the schema reads as forbidden to the model, so drift in either
// direction is a bug: every verb in the enum, every JSON-tagged struct
// field in the properties, and nothing extra on either side.
func TestToolSchemaMatchesCall(t *testing.T) {
	group, tabAct, browserAct := toolSchemaNodes(t)

	if !slices.Equal(tabAct.Properties["action"].Enum, tabActionNames) {
		t.Errorf("tab action enum = %v\nwant tabActionNames %v", tabAct.Properties["action"].Enum, tabActionNames)
	}
	if want := []string{"list"}; !slices.Equal(browserAct.Properties["action"].Enum, want) {
		t.Errorf("browser action enum = %v, want %v", browserAct.Properties["action"].Enum, want)
	}

	// The group envelope: every tabRequest field in the schema and
	// nothing extra.
	groupFields := make(map[string]bool)
	for _, tag := range jsonTags(reflect.TypeOf(tabRequest{})) {
		groupFields[tag] = true
	}
	for field := range groupFields {
		if _, ok := group.Properties[field]; !ok {
			t.Errorf("group field %q missing from the tab group schema properties", field)
		}
	}
	for prop := range group.Properties {
		if !groupFields[prop] {
			t.Errorf("schema property %q matches no tabRequest field", prop)
		}
	}

	// One zero value per verb; the key set is checked against
	// tabActionNames so a new action can't be forgotten here.
	tabStructs := map[string]tabAction{
		"open": openRequest{}, "close": closeRequest{}, "back": backRequest{},
		"forward": forwardRequest{}, "reload": reloadRequest{},
		"click": clickRequest{}, "hover": hoverRequest{}, "fill": fillRequest{},
		"select": selectRequest{}, "type": typeRequest{}, "scroll": scrollRequest{},
		"upload": uploadRequest{},
		"eval":   evalRequest{}, "screenshot": screenshotRequest{},
	}
	for _, name := range tabActionNames {
		if _, ok := tabStructs[name]; !ok {
			t.Fatalf("action %q is in tabActionNames but not in this test's struct map; add it", name)
		}
	}
	if len(tabStructs) != len(tabActionNames) {
		t.Fatalf("struct map has %d actions, tabActionNames has %d", len(tabStructs), len(tabActionNames))
	}

	structFields := make(map[string]bool)
	for _, a := range tabStructs {
		for _, tag := range jsonTags(reflect.TypeOf(a)) {
			structFields[tag] = true
		}
	}
	for field := range structFields {
		if _, ok := tabAct.Properties[field]; !ok {
			t.Errorf("struct field %q missing from the tab action schema properties", field)
		}
	}
	for prop := range tabAct.Properties {
		if !structFields[prop] {
			t.Errorf("schema property %q matches no tab action struct field", prop)
		}
	}

	browserFields := make(map[string]bool)
	for _, tag := range jsonTags(reflect.TypeOf(listRequest{})) {
		browserFields[tag] = true
	}
	for field := range browserFields {
		if _, ok := browserAct.Properties[field]; !ok {
			t.Errorf("struct field %q missing from the browser action schema properties", field)
		}
	}
	for prop := range browserAct.Properties {
		if !browserFields[prop] {
			t.Errorf("schema property %q matches no browser action struct field", prop)
		}
	}
}

// Over stdio, a client can initialize and list exactly the webtool tool.
func TestStdioToolsList(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
`)
	var out strings.Builder
	if err := server.NewStdioServer(newTestServer().mcpServer()).Listen(context.Background(), in, &out); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	var list struct {
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &list); err != nil {
		t.Fatalf("tools/list response: %v\n%s", err, out.String())
	}
	if len(list.Result.Tools) != 1 || list.Result.Tools[0].Name != "webtool" {
		t.Fatalf("tools = %+v, want just webtool", list.Result.Tools)
	}
}
