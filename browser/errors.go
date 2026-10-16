package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/coder/websocket"
)

// ErrTabClosed indicates an enqueue raced with or followed a Tab.Close().
// Callers should re-resolve the tab prefix through the coordinator; the
// coordinator will surface "tab not found" or "tab closed" as appropriate.
type ErrTabClosed struct{}

func (*ErrTabClosed) Error() string { return "tab closed" }
func (*ErrTabClosed) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"kind":    "tab_closed",
		"message": "tab closed",
	})
}

// ConnectionError is the marker interface implemented by every error
// indicating the CDP websocket to Chrome is dead. Upstream callers (the
// MCP server) use a single errors.As against this interface to decide
// whether to tear down browser state.
type ConnectionError interface {
	error
	connectionError()
}

// ErrChromeUnreachable means the CDP endpoint could not be reached —
// Chrome is not running, remote debugging is disabled, or the TCP
// connection was refused.
type ErrChromeUnreachable struct {
	Cause error
}

func (e *ErrChromeUnreachable) Error() string {
	return "Chrome is not running or remote debugging is not enabled"
}
func (e *ErrChromeUnreachable) Unwrap() error    { return e.Cause }
func (e *ErrChromeUnreachable) connectionError() {}
func (e *ErrChromeUnreachable) MarshalJSON() ([]byte, error) {
	m := map[string]string{"kind": "chrome_unreachable", "message": e.Error()}
	if e.Cause != nil {
		m["cause"] = e.Cause.Error()
	}
	return json.Marshal(m)
}

// ErrChromeRejected means the CDP endpoint responded but refused the
// handshake (HTTP 403). Usually a stale auth token or a mismatched
// remote-debugging setup.
type ErrChromeRejected struct {
	Cause error
}

func (e *ErrChromeRejected) Error() string {
	return "Chrome rejected the connection"
}
func (e *ErrChromeRejected) Unwrap() error    { return e.Cause }
func (e *ErrChromeRejected) connectionError() {}
func (e *ErrChromeRejected) MarshalJSON() ([]byte, error) {
	m := map[string]string{"kind": "chrome_rejected", "message": e.Error()}
	if e.Cause != nil {
		m["cause"] = e.Cause.Error()
	}
	return json.Marshal(m)
}

// newConnError classifies err into a ConnectionError, or returns it
// unchanged if it does not match a known connection-failure signature.
// nil in → nil out.
func newConnError(err error) error {
	if err == nil {
		return nil
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return &ErrChromeUnreachable{Cause: err}
	}

	// Mid-session drops: established websocket goes away while idle.
	// rod/cdp surfaces these as io.EOF, net.ErrClosed, or a websocket
	// close frame — none of which match the initial-connect signatures
	// above.
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return &ErrChromeUnreachable{Cause: err}
	}

	// coder/websocket surfaces a peer-initiated close frame as a typed
	// CloseError; CloseStatus returns the status code (>= 0) for those and
	// -1 otherwise. A close frame from Chrome means the CDP session ended.
	if websocket.CloseStatus(err) != -1 {
		return &ErrChromeUnreachable{Cause: err}
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "403"):
		return &ErrChromeRejected{Cause: err}
	case strings.Contains(msg, "connection refused"):
		return &ErrChromeUnreachable{Cause: err}
	case strings.Contains(msg, "use of closed network connection"):
		return &ErrChromeUnreachable{Cause: err}
	case strings.Contains(msg, "websocket: close"):
		return &ErrChromeUnreachable{Cause: err}
	default:
		return err
	}
}

// ErrNoElement indicates that an id has no element on the page: its
// content has changed, left the page, or the page has navigated. Shared
// by action resolution and scoped snapshots so the model sees one
// consistent failure shape. Not a BrowserError: resolution never
// succeeded, so there is no resolved selector to report, only the id.
type ErrNoElement struct {
	ID string
}

func (e *ErrNoElement) Error() string {
	return fmt.Sprintf("no element with id %q on this page; its content has changed, left the page, or the page has navigated", e.ID)
}
func (e *ErrNoElement) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"kind":    "no_element",
		"message": e.Error(),
		"id":      e.ID,
	})
}

// BrowserError is the interface for all actionability errors. It follows
// the net.Error convention: callers can type-assert or use errors.As to
// distinguish actionability failures from system errors.
type BrowserError interface {
	error
	Selector() string
}

// ErrNotVisible indicates that the element has no visible shape (zero bounding
// rect) or is outside the viewport.
type ErrNotVisible struct {
	Sel string
}

func (e *ErrNotVisible) Error() string {
	return fmt.Sprintf("element not visible: %s; no visible shape or outside the viewport", e.Sel)
}
func (e *ErrNotVisible) Selector() string { return e.Sel }
func (e *ErrNotVisible) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"kind":     "not_visible",
		"message":  e.Error(),
		"selector": e.Sel,
	})
}

// ErrObscured indicates that the element is covered by another element (e.g.
// a modal, cookie banner, or overlay). Blocker is the covering element's
// role and name, e.g. `dialog "Cookie consent"`, or empty if it could not
// be determined.
type ErrObscured struct {
	Sel     string
	Blocker string
}

func (e *ErrObscured) Error() string {
	if e.Blocker != "" {
		return fmt.Sprintf("element obscured: %s is covered by %s; dismiss the blocker first", e.Sel, e.Blocker)
	}
	return fmt.Sprintf("element obscured: %s is covered by another element; dismiss the blocker first", e.Sel)
}
func (e *ErrObscured) Selector() string { return e.Sel }
func (e *ErrObscured) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"kind":     "obscured",
		"message":  e.Error(),
		"selector": e.Sel,
		"blocker":  e.Blocker,
	})
}

// ErrNoPointerEvents indicates that the element has pointer-events: none set
// in CSS, preventing mouse interaction.
type ErrNoPointerEvents struct {
	Sel string
}

func (e *ErrNoPointerEvents) Error() string {
	return fmt.Sprintf("element not clickable: %s; pointer-events is none", e.Sel)
}
func (e *ErrNoPointerEvents) Selector() string { return e.Sel }
func (e *ErrNoPointerEvents) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"kind":     "no_pointer_events",
		"message":  e.Error(),
		"selector": e.Sel,
	})
}

// ErrNotStable indicates that the element's position or size is still changing
// (animation, layout shift, or transition in progress).
type ErrNotStable struct {
	Sel string
}

func (e *ErrNotStable) Error() string {
	return fmt.Sprintf("element not stable: %s; position or size still changing", e.Sel)
}
func (e *ErrNotStable) Selector() string { return e.Sel }
func (e *ErrNotStable) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"kind":     "not_stable",
		"message":  e.Error(),
		"selector": e.Sel,
	})
}

// ErrOptionNotFound indicates that no option matching the given text exists
// in the select element. The agent should extract the element to see available options.
type ErrOptionNotFound struct {
	Sel   string
	Value string
}

func (e *ErrOptionNotFound) Error() string {
	return fmt.Sprintf("option not found: %q in select element %s", e.Value, e.Sel)
}
func (e *ErrOptionNotFound) Selector() string { return e.Sel }
func (e *ErrOptionNotFound) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"kind":     "option_not_found",
		"message":  e.Error(),
		"selector": e.Sel,
		"value":    e.Value,
	})
}

// ErrNotEnabled indicates that the element is disabled.
type ErrNotEnabled struct {
	Sel string
}

func (e *ErrNotEnabled) Error() string {
	return fmt.Sprintf("element disabled: %s", e.Sel)
}
func (e *ErrNotEnabled) Selector() string { return e.Sel }
func (e *ErrNotEnabled) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"kind":     "not_enabled",
		"message":  e.Error(),
		"selector": e.Sel,
	})
}

// ErrBlocked indicates that a network request was blocked by the security policy.
type ErrBlocked struct {
	Method string
	URL    string
	Rule   string
}

func (e *ErrBlocked) Error() string {
	return fmt.Sprintf("request blocked by policy: %s %s (rule: %s)", e.Method, e.URL, e.Rule)
}
func (e *ErrBlocked) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"kind":    "blocked",
		"message": e.Error(),
		"method":  e.Method,
		"url":     e.URL,
		"rule":    e.Rule,
	})
}
