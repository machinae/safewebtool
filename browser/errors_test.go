package browser

import (
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
)

// newConnError must classify by error structure (net.OpError) AND by
// string content ("403", "connection refused"). Both paths feed the
// upstream MCP handler's errors.As check, so any miss leaves the app
// stuck showing "connected" after Chrome dies.
func TestNewConnError(t *testing.T) {
	rawOpErr := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}

	tests := []struct {
		name    string
		in      error
		wantNil bool
		wantAs  any // pointer to the typed error we expect; nil means passthrough
	}{
		{
			name:    "nil passes through",
			in:      nil,
			wantNil: true,
		},
		{
			name:   "net.OpError → Unreachable",
			in:     rawOpErr,
			wantAs: new(*ErrChromeUnreachable),
		},
		{
			name:   "wrapped net.OpError → Unreachable (errors.As unwraps)",
			in:     fmt.Errorf("dialing cdp: %w", rawOpErr),
			wantAs: new(*ErrChromeUnreachable),
		},
		{
			name:   "connection refused string → Unreachable",
			in:     errors.New("dial tcp 127.0.0.1:9222: connection refused"),
			wantAs: new(*ErrChromeUnreachable),
		},
		{
			name:   "403 string → Rejected",
			in:     errors.New("websocket bad handshake: 403 Forbidden."),
			wantAs: new(*ErrChromeRejected),
		},
		{
			name:   "io.EOF → Unreachable (ws dropped mid-session)",
			in:     io.EOF,
			wantAs: new(*ErrChromeUnreachable),
		},
		{
			name:   "wrapped io.EOF → Unreachable",
			in:     fmt.Errorf("reading cdp frame: %w", io.EOF),
			wantAs: new(*ErrChromeUnreachable),
		},
		{
			name:   "net.ErrClosed → Unreachable",
			in:     net.ErrClosed,
			wantAs: new(*ErrChromeUnreachable),
		},
		{
			name:   "use of closed network connection string → Unreachable",
			in:     errors.New("write tcp 127.0.0.1:9222: use of closed network connection"),
			wantAs: new(*ErrChromeUnreachable),
		},
		{
			name:   "websocket close frame → Unreachable",
			in:     errors.New("websocket: close 1006 (abnormal closure): unexpected EOF"),
			wantAs: new(*ErrChromeUnreachable),
		},
		{
			name: "unrelated error passes through unchanged",
			in:   errors.New("tab not found: abc1234"),
			// wantAs nil: check identity instead
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := newConnError(tc.in)

			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}

			if tc.wantAs == nil {
				if got != tc.in {
					t.Fatalf("expected passthrough, got %T: %v", got, got)
				}
				return
			}

			if !errors.As(got, tc.wantAs) {
				t.Fatalf("errors.As failed: got %T (%v), want %T", got, got, tc.wantAs)
			}

			// Every typed connection error must also satisfy the
			// ConnectionError marker — that's what the MCP layer
			// uses as the catch-all. If a new type ever skips the
			// marker, this guards against a silent regression.
			var cerr ConnectionError
			if !errors.As(got, &cerr) {
				t.Fatalf("%T does not satisfy ConnectionError marker", got)
			}

			// Cause must round-trip via Unwrap so callers can drill
			// down to the raw rod/net error if they need to.
			if unwrapped := errors.Unwrap(got); unwrapped != tc.in {
				t.Fatalf("Unwrap() = %v, want %v", unwrapped, tc.in)
			}
		})
	}
}
