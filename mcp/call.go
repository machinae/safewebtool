package mcp

// Input layer for the JSON tool grammar (plans/json-refactor.md):
// bindCall decodes and validates the raw tool input; run.go executes
// the result.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/machinae/safewebtool/browser"
)

// request is the wire envelope. Action objects stay raw because their
// concrete type depends on their "action" field; bindTabAction and
// bindBrowserAction do the second decode pass.
type request struct {
	Browser []json.RawMessage `json:"browser"`
	Tabs    []tabRequest      `json:"tabs"`
}

func (r request) Validate() error {
	if len(r.Browser) == 0 && len(r.Tabs) == 0 {
		return fmt.Errorf("empty call. Example: %s", envelopeExample)
	}
	return nil
}

// Output modes: what a group returns about the page's end state.
const (
	outputSnapshot = "snapshot" // the AX tree as [id] role "name" lines, for acting next
	outputMarkdown = "markdown" // page content converted to markdown, for reading
	outputHTML     = "html"     // raw page HTML, when markdown conversion loses structure
	outputNone     = "none"     // no end state: status slots only, no title/URL
)

// Output modifier limits. Selectors and the match pattern are accepted as
// opaque strings (no AX-role validation; an unknown role just matches
// nothing), capped only so a malformed call cannot pass unbounded input
// through to the browser.
const (
	maxScopeSelectors = 20  // selectors per include/exclude array
	maxScopeSelLen    = 64  // chars per "role:name" selector
	maxMatchLen       = 200 // chars in a match pattern
	maxSectionLen     = 120 // chars in a section heading name
)

// tabRequest is one tab's batch: the tab binding, its ordered actions, the
// output the group returns about the page's end state, and the output
// modifiers that shape that output (AX-region scope and a line filter).
type tabRequest struct {
	Tab               string            `json:"tab"`
	Output            string            `json:"output"`
	IncludeElementIDs []string          `json:"includeElementIds"`
	IncludeAXScope    []string          `json:"includeAXScope"`
	ExcludeAXScope    []string          `json:"excludeAXScope"`
	Match             string            `json:"match"`
	Section           string            `json:"section"`
	Actions           []json.RawMessage `json:"actions"`
}

func (t tabRequest) Validate() error {
	if t.Tab == "" {
		return errors.New(`missing "tab": a 7-char tab id, or "new" for a tab this group's first "open" creates`)
	}
	switch t.Output {
	case outputSnapshot, outputMarkdown, outputHTML, outputNone:
	case "":
		return fmt.Errorf(`missing "output": "snapshot" to act next, "markdown" or "html" to read the page, "none" for nothing. Example: %s`, groupExample)
	default:
		return fmt.Errorf(`"output" must be "snapshot", "markdown", "html", or "none", got %q`, t.Output)
	}
	if len(t.IncludeElementIDs) > maxScopeSelectors {
		return fmt.Errorf(`"includeElementIds" has too many ids (%d, max %d)`, len(t.IncludeElementIDs), maxScopeSelectors)
	}
	for _, id := range t.IncludeElementIDs {
		if err := checkElementID("includeElementIds", id, groupExample); err != nil {
			return err
		}
	}
	if err := validateScope("includeAXScope", t.IncludeAXScope); err != nil {
		return err
	}
	if err := validateScope("excludeAXScope", t.ExcludeAXScope); err != nil {
		return err
	}
	if t.Match != "" {
		if len(t.Match) > maxMatchLen {
			return fmt.Errorf(`"match" is too long (%d chars, max %d)`, len(t.Match), maxMatchLen)
		}
		if _, err := regexp.Compile(t.Match); err != nil {
			return fmt.Errorf(`"match" is not a valid regular expression: %v`, err)
		}
	}
	if t.Section != "" {
		if len(t.Section) > maxSectionLen {
			return fmt.Errorf(`"section" is too long (%d chars, max %d)`, len(t.Section), maxSectionLen)
		}
		if t.Output != outputSnapshot && t.Output != outputMarkdown {
			return fmt.Errorf(`"section" needs "output" of "snapshot" or "markdown", got %q`, t.Output)
		}
	}
	if t.Tab == "new" {
		if len(t.Actions) == 0 {
			return errors.New(`a "new" group's first action must be "open"; it creates the tab the rest of the group acts on`)
		}
		if head, err := actionHead(t.Actions[0]); err == nil && head.Action != "open" {
			return errors.New(`a "new" group's first action must be "open"; it creates the tab the rest of the group acts on`)
		}
	} else if !isHexID(t.Tab) {
		return fmt.Errorf(`"tab" must be "new" or a 7-char hex id from list, got %q`, t.Tab)
	}
	return nil
}

// idShape matches the shape of a snapshot id: 7+ chars of hex and dots. The
// dots admit composite iframe element ids ("abc1234.def5678", an element inside
// an iframe); a tab handle or main-frame element is the no-dot case. Loose by
// design: the browser is the real resolver, so this only rejects obvious
// non-ids (CSS selectors, element text). Case-insensitive.
var idShape = regexp.MustCompile(`^[0-9a-fA-F.]{7,}$`)

// isHexID reports whether s looks like a snapshot id (see idShape), the shape
// of both tab prefixes and snapshot element ids. bindCall normalizes tab ids
// to lowercase after this.
func isHexID(s string) bool {
	return idShape.MatchString(s)
}

// checkElementID rejects ids that are not shaped like snapshot element
// ids before they reach the browser. A CSS selector or element text here
// would burn the resolution retry and come back with a misleading
// "content changed" error; this names the actual mistake instead.
func checkElementID(field, id, example string) error {
	if isHexID(id) {
		return nil
	}
	return fmt.Errorf(`%q must be a 7-char hex element id from a snapshot, got %q; CSS selectors and element text are not ids. Example: %s`, field, id, example)
}

// validateScope bounds an AX-scope array: at most maxScopeSelectors
// entries, each a non-empty string no longer than maxScopeSelLen. It does
// NOT check the role against a known set; an unrecognized selector simply
// matches nothing in the browser.
func validateScope(field string, sels []string) error {
	if len(sels) > maxScopeSelectors {
		return fmt.Errorf("%q has too many selectors (%d, max %d)", field, len(sels), maxScopeSelectors)
	}
	for _, s := range sels {
		if s == "" {
			return fmt.Errorf("%q has an empty selector; each is an ARIA role or role:name, e.g. \"main\" or \"region:About\"", field)
		}
		if len(s) > maxScopeSelLen {
			return fmt.Errorf("%q selector %q is too long (max %d chars)", field, s, maxScopeSelLen)
		}
	}
	return nil
}

// actionRequest holds the two fields every action carries.
type actionRequest struct {
	Reason string `json:"reason"`
	Action string `json:"action"`
}

func (a actionRequest) Validate() error {
	if a.Reason == "" {
		return errors.New(`missing "reason": one short sentence on why you are running this action`)
	}
	return nil
}

// tabAction is implemented by every tab-scoped action struct. run
// executes the action against the group's tab and returns the verb's own
// outputs (target, result) plus the raw error; the runner owns slot
// assembly, error stringification, and the group-end output. name
// returns the wire action name and comes promoted from actionRequest.
type tabAction interface {
	Validate() error
	name() string
	run(ctx context.Context, s *Server, tab *browser.Tab) (actionResponse, error)
}

// browserAction is implemented by every browser-scoped action struct.
// Same contract as tabAction, minus the tab.
type browserAction interface {
	Validate() error
	name() string
	run(ctx context.Context, s *Server) (actionResponse, error)
}

// --- Tab actions ---

// openRequest navigates this group's tab, or creates it in a "new" group.
type openRequest struct {
	actionRequest
	URL string `json:"url"`
}

const openExample = `{"reason": "open the pricing page", "action": "open", "url": "https://acme.com/pricing"}`

func (o openRequest) Validate() error {
	if err := o.actionRequest.Validate(); err != nil {
		return err
	}
	if o.URL == "" {
		return fmt.Errorf(`"open" requires "url". Example: %s`, openExample)
	}
	return nil
}

type closeRequest struct{ actionRequest }

const closeExample = `{"reason": "done with the search results", "action": "close"}`

type backRequest struct{ actionRequest }

const backExample = `{"reason": "return to the results list", "action": "back"}`

type forwardRequest struct{ actionRequest }

const forwardExample = `{"reason": "go forward to the article again", "action": "forward"}`

type reloadRequest struct{ actionRequest }

const reloadExample = `{"reason": "refresh the stale listings", "action": "reload"}`

type clickRequest struct {
	actionRequest
	ID string `json:"id"`
}

const clickExample = `{"reason": "dismiss the cookie banner", "action": "click", "id": "a3f81c2"}`

func (c clickRequest) Validate() error {
	if err := c.actionRequest.Validate(); err != nil {
		return err
	}
	if c.ID == "" {
		return fmt.Errorf(`"click" requires "id". Example: %s`, clickExample)
	}
	return checkElementID("id", c.ID, clickExample)
}

type hoverRequest struct {
	actionRequest
	ID string `json:"id"`
}

const hoverExample = `{"reason": "reveal the row actions menu", "action": "hover", "id": "5b29c44"}`

func (h hoverRequest) Validate() error {
	if err := h.actionRequest.Validate(); err != nil {
		return err
	}
	if h.ID == "" {
		return fmt.Errorf(`"hover" requires "id". Example: %s`, hoverExample)
	}
	return checkElementID("id", h.ID, hoverExample)
}

// fillRequest sets an input element's value, replacing its content.
type fillRequest struct {
	actionRequest
	ID   string `json:"id"`
	Text string `json:"text"`
}

const fillExample = `{"reason": "fill the email field", "action": "fill", "id": "9c04e1d", "text": "ada@example.com"}`

func (f fillRequest) Validate() error {
	if err := f.actionRequest.Validate(); err != nil {
		return err
	}
	if f.ID == "" {
		return fmt.Errorf(`"fill" requires "id". Example: %s`, fillExample)
	}
	if err := checkElementID("id", f.ID, fillExample); err != nil {
		return err
	}
	// Empty (or omitted) text is valid: it clears the field, matching the
	// schema's "replaces the input's existing content".
	return nil
}

// selectRequest picks a dropdown option by its visible text.
type selectRequest struct {
	actionRequest
	ID     string `json:"id"`
	Option string `json:"option"`
}

const selectExample = `{"reason": "choose the shipping country", "action": "select", "id": "d7e02f3", "option": "United States"}`

func (s selectRequest) Validate() error {
	if err := s.actionRequest.Validate(); err != nil {
		return err
	}
	if s.ID == "" {
		return fmt.Errorf(`"select" requires "id". Example: %s`, selectExample)
	}
	if err := checkElementID("id", s.ID, selectExample); err != nil {
		return err
	}
	if s.Option == "" {
		return fmt.Errorf(`"select" requires "option": the visible text of the option to pick. Example: %s`, selectExample)
	}
	return nil
}

// typeRequest sends real keystrokes to the focused element: one key or
// chord via Key, or a string typed character by character via Text. The
// strings are opaque here; the browser layer parses them.
type typeRequest struct {
	actionRequest
	Key  string `json:"key"`
	Text string `json:"text"`
}

const typeExample = `{"reason": "submit the search", "action": "type", "key": "Enter"}`

const typeTextExample = `{"reason": "write the greeting in the doc", "action": "type", "text": "Hello, world"}`

func (t typeRequest) Validate() error {
	if err := t.actionRequest.Validate(); err != nil {
		return err
	}
	if (t.Key == "") == (t.Text == "") {
		return fmt.Errorf(`"type" requires exactly one of "key" (a key name or chord) or "text" (a string sent as keystrokes). Examples: %s %s`, typeExample, typeTextExample)
	}
	return nil
}

// scrollRequest scrolls by Pixels: negative scrolls up, zero or absent
// scrolls one viewport down.
type scrollRequest struct {
	actionRequest
	Pixels int `json:"pixels"`
}

const scrollExample = `{"reason": "load more results", "action": "scroll", "pixels": 1200}`

type uploadRequest struct {
	actionRequest
	ID    string   `json:"id"`
	Files []string `json:"files"`
}

const uploadExample = `{"reason": "attach the resume", "action": "upload", "id": "20fa7b8", "files": ["/Users/me/resume.pdf"]}`

func (u uploadRequest) Validate() error {
	if err := u.actionRequest.Validate(); err != nil {
		return err
	}
	if u.ID == "" {
		return fmt.Errorf(`"upload" requires "id". Example: %s`, uploadExample)
	}
	if err := checkElementID("id", u.ID, uploadExample); err != nil {
		return err
	}
	if len(u.Files) == 0 {
		return fmt.Errorf(`"upload" requires "files": at least one file path. Example: %s`, uploadExample)
	}
	return nil
}

type evalRequest struct {
	actionRequest
	JS string `json:"js"`
}

const evalExample = `{"reason": "count the result links", "action": "eval", "js": "document.querySelectorAll('a').length"}`

func (e evalRequest) Validate() error {
	if err := e.actionRequest.Validate(); err != nil {
		return err
	}
	if e.JS == "" {
		return fmt.Errorf(`"eval" requires "js". Example: %s`, evalExample)
	}
	return nil
}

// screenshotRequest captures an image: the viewport by default, the whole
// page with Full, or one element with ID. Without Path the image
// returns inline in the tool result.
type screenshotRequest struct {
	actionRequest
	Path      string `json:"path"`
	Full      bool   `json:"full"`
	MaxHeight int    `json:"maxHeight"`
	ID        string `json:"id"`
}

const screenshotExample = `{"reason": "save the chart for the user", "action": "screenshot", "path": "/Users/me/shots/chart.jpg"}`

func (s screenshotRequest) Validate() error {
	if err := s.actionRequest.Validate(); err != nil {
		return err
	}
	if s.Full && s.ID != "" {
		return errors.New(`"full" and "id" are mutually exclusive: "full" captures the whole page, "id" captures one element`)
	}
	if s.ID != "" {
		if err := checkElementID("id", s.ID, screenshotExample); err != nil {
			return err
		}
	}
	if s.MaxHeight < 0 {
		return fmt.Errorf(`"maxHeight" must be positive, got %d`, s.MaxHeight)
	}
	if s.MaxHeight != 0 && !s.Full {
		return errors.New(`"maxHeight" only applies when "full" is true`)
	}
	if s.Path != "" {
		ext := strings.ToLower(filepath.Ext(s.Path))
		if ext != ".jpg" && ext != ".jpeg" {
			return fmt.Errorf(`"path" must end in .jpg or .jpeg. Example: %s`, screenshotExample)
		}
	}
	return nil
}

// --- Browser actions ---

// listRequest lists open tabs: id, title, and url for each.
type listRequest struct{ actionRequest }

const listExample = `{"reason": "find the tab the user mentioned", "action": "list"}`

const browserExample = `{"browser": [` + listExample + `]}`

// --- Parsed call ---

// call is a validated invocation, ready to execute.
type call struct {
	Browser []browserAction
	Tabs    []tabActions
}

// tabActions is a tabRequest after binding: same tab, output, and output
// modifiers, with the actions parsed.
type tabActions struct {
	Tab               string
	Output            string
	IncludeElementIDs []string
	IncludeAXScope    []string
	ExcludeAXScope    []string
	Match             string
	Section           string
	Actions           []tabAction
}

// response is the wire envelope of a result, mirroring request. It is
// marshaled with a placeholder per content value, then the placeholders
// are string-replaced with the fenced multiline content (snapshots keep
// literal newlines, which encoding/json cannot emit).
type response struct {
	// Error reports problems with the envelope itself (unparseable
	// JSON, empty call); slot problems live on the slots instead.
	Error   string           `json:"error,omitempty"`
	Browser []actionResponse `json:"browser,omitempty"`
	Tabs    []tabResponse    `json:"tabs,omitempty"`
}

// tabResponse is one group's outcome. Title, URL, and Output are filled
// by execution; a validation failure carries only Tab, Results, and
// group-level problems in Error. Output holds what the group's "output"
// field asked for: the snapshot or the extracted page content.
type tabResponse struct {
	Tab     string           `json:"tab"`
	Title   string           `json:"title,omitempty"`
	URL     string           `json:"url,omitempty"`
	Results []actionResponse `json:"results,omitempty"`
	Output  string           `json:"output,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// actionResponse is one result slot, aligned by position with its
// request. Absent Error means the action succeeded. Each handler sets
// Target to its verb's natural identifier (element id, url, key) so
// same-verb runs stay distinguishable. Result carries the output of
// data-bearing actions (today just eval), fenced when page-derived.
type actionResponse struct {
	Action string     `json:"action"`
	Target string     `json:"target,omitempty"`
	Result string     `json:"result,omitempty"`
	Tabs   []tabEntry `json:"tabs,omitempty"`
	Error  string     `json:"error,omitempty"`

	// image holds a base64 JPEG for a screenshot without a path; the
	// renderer appends it as an image content block after the JSON text.
	image string
}

// tabEntry is one roster row in a list result, the same vocabulary that
// describes a tab everywhere else: tab, title, url.
type tabEntry struct {
	Tab   string `json:"tab"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

const groupExample = `{"tab": "abc1234", "output": "snapshot", "actions": [` + clickExample + `]}`

const envelopeExample = `{"tabs": [` + groupExample + `]}`

// bindCall decodes and validates the raw tool input. Exactly one return
// is non-nil: the call ready to execute, or the validation-failure
// response carrying an error on every bad slot so the model can fix the
// whole call in one turn. Invalid calls never partially execute.
func bindCall(input []byte) (*call, *response) {
	var req request
	if err := strictUnmarshal(input, &req); err != nil {
		return nil, &response{Error: fmt.Sprintf(`%s. Input is a JSON object with "browser" and/or "tabs" arrays. Example: %s`, err, envelopeExample)}
	}
	if err := req.Validate(); err != nil {
		return nil, &response{Error: err.Error()}
	}

	invalid := false
	resp := &response{}
	out := &call{}

	for _, raw := range req.Browser {
		head, err := actionHead(raw)
		slot := actionResponse{Action: head.Action}
		var a browserAction
		if err == nil {
			a, err = bindBrowserAction(head, raw)
		}
		if err != nil {
			slot.Error = err.Error()
			invalid = true
		} else {
			out.Browser = append(out.Browser, a)
		}
		resp.Browser = append(resp.Browser, slot)
	}

	seen := make(map[string]bool) // tab ids match case-insensitively
	for _, tr := range req.Tabs {
		// Ids are normalized to lowercase here, once; everything
		// downstream (echo, duplicate check, execution) sees lowercase.
		id := strings.ToLower(tr.Tab)
		tres := tabResponse{Tab: id}

		if err := tr.Validate(); err != nil {
			tres.Error = err.Error()
		} else if id != "new" {
			if seen[id] {
				tres.Error = fmt.Sprintf("tab %q appears twice in this call; merge its actions into one group", id)
			}
			seen[id] = true
		}
		if tres.Error != "" {
			invalid = true
			// Keep the alignment invariant: one result slot per
			// requested action, even when the group itself failed.
			for _, raw := range tr.Actions {
				head, _ := actionHead(raw)
				tres.Results = append(tres.Results, actionResponse{Action: head.Action})
			}
			resp.Tabs = append(resp.Tabs, tres)
			continue
		}

		ta := tabActions{
			Tab:               id,
			Output:            tr.Output,
			IncludeElementIDs: tr.IncludeElementIDs,
			IncludeAXScope:    tr.IncludeAXScope,
			ExcludeAXScope:    tr.ExcludeAXScope,
			Match:             tr.Match,
			Section:           tr.Section,
		}
		for _, raw := range tr.Actions {
			head, err := actionHead(raw)
			slot := actionResponse{Action: head.Action}
			var a tabAction
			if err == nil {
				a, err = bindTabAction(head, raw)
			}
			if err != nil {
				slot.Error = err.Error()
				invalid = true
			} else {
				ta.Actions = append(ta.Actions, a)
			}
			tres.Results = append(tres.Results, slot)
		}
		resp.Tabs = append(resp.Tabs, tres)
		out.Tabs = append(out.Tabs, ta)
	}

	if invalid {
		return nil, resp
	}
	return out, nil
}

// actionHead decodes just the discriminator fields so the binders can
// pick the concrete struct.
func actionHead(raw json.RawMessage) (actionRequest, error) {
	var head actionRequest
	if err := json.Unmarshal(raw, &head); err != nil {
		return head, fmt.Errorf("each action is a JSON object. Example: %s", clickExample)
	}
	if head.Action == "" {
		return head, fmt.Errorf(`missing "action". Example: %s`, clickExample)
	}
	return head, nil
}

// tabActionNames lists every tab verb, for the unknown-action error and
// the wrong-scope check in bindBrowserAction. Keep in sync with the
// bindTabAction switch.
var tabActionNames = []string{
	"open", "close", "back", "forward", "reload", "click", "hover",
	"fill", "select", "type", "scroll", "upload", "eval",
	"screenshot",
}

// bindTabAction decodes one action object into the concrete struct named
// by its "action" field and validates it.
func bindTabAction(head actionRequest, raw json.RawMessage) (tabAction, error) {
	switch head.Action {
	case "open":
		var a openRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, openExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "close":
		var a closeRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, closeExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "back":
		var a backRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, backExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "forward":
		var a forwardRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, forwardExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "reload":
		var a reloadRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, reloadExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "click":
		var a clickRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, clickExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "hover":
		var a hoverRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, hoverExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "fill":
		var a fillRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, fillExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "select":
		var a selectRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, selectExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "type":
		var a typeRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf(`%s. Example: %s. To set an input's value, use "fill"`, err, typeExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "scroll":
		var a scrollRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, scrollExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "upload":
		var a uploadRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, uploadExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "eval":
		var a evalRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, evalExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "screenshot":
		var a screenshotRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, screenshotExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	case "list":
		return nil, fmt.Errorf(`"list" is a browser action; it goes in the top-level "browser" array, not a tab group. Example: %s`, browserExample)
	default:
		if head.Action == "wait" {
			return nil, fmt.Errorf(`there is no "wait" action: every action and read waits for the page to settle automatically. If content is still loading, re-request the snapshot`)
		}
		if head.Action == "key" {
			return nil, fmt.Errorf(`there is no "key" action: "type" presses keys (example: %s); "fill" sets an input's value`, typeExample)
		}
		return nil, fmt.Errorf("unknown action %q. Tab actions: %s", head.Action, strings.Join(tabActionNames, ", "))
	}
}

// bindBrowserAction decodes one action object from the top-level
// "browser" array and validates it.
func bindBrowserAction(head actionRequest, raw json.RawMessage) (browserAction, error) {
	switch head.Action {
	case "list":
		var a listRequest
		if err := strictUnmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s. Example: %s", err, listExample)
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		return a, nil
	default:
		if slices.Contains(tabActionNames, head.Action) {
			return nil, fmt.Errorf(`%q is a tab action; it goes inside a tab group, not "browser". Example: %s`, head.Action, envelopeExample)
		}
		return nil, fmt.Errorf("unknown browser action %q. Browser actions: list", head.Action)
	}
}

// strictUnmarshal decodes data into v, rejecting unknown fields.
func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
