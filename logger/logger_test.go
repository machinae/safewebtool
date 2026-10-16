package logger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

// structuredErr implements json.Marshaler to exercise the
// pass-through branch of Entry.MarshalJSON. No stdlib error type
// implements json.Marshaler, so this one has to be custom.
type structuredErr struct {
	kind string
	msg  string
}

func (e *structuredErr) Error() string { return e.msg }
func (e *structuredErr) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"kind":    e.kind,
		"message": e.msg,
		"extra":   "payload",
	})
}

func TestErrorKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"context deadline exceeded", context.DeadlineExceeded, "timeout"},
		{"wrapped context deadline", fmt.Errorf("fetch: %w", context.DeadlineExceeded), "timeout"},
		{"context canceled", context.Canceled, "canceled"},
		{"net.DNSError timeout", &net.DNSError{IsTimeout: true}, "timeout"},
		{"net.DNSError non-timeout", &net.DNSError{}, "network"},
		{"wrapped net.DNSError", fmt.Errorf("dial: %w", &net.DNSError{}), "network"},
		{"plain error falls through", errors.New("boom"), "other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorKind(tc.err); got != tc.want {
				t.Errorf("errorKind(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestEntryMarshalJSON(t *testing.T) {
	ts := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		entry Entry
		want  string
	}{
		{
			name:  "no error omits the field",
			entry: Entry{Time: ts, Action: "click", Target: "button", URL: "https://example.com"},
			want:  `{"ts":"2026-04-24T12:00:00Z","action_name":"click","target":"button","url":"https://example.com"}`,
		},
		{
			name:  "json.Marshaler error passes through verbatim",
			entry: Entry{Time: ts, Action: "open", Error: &structuredErr{kind: "blocked", msg: "denied"}},
			want:  `{"ts":"2026-04-24T12:00:00Z","action_name":"open","error":{"extra":"payload","kind":"blocked","message":"denied"}}`,
		},
		{
			name:  "plain error hits fallback with kind=other",
			entry: Entry{Time: ts, Action: "eval", Error: errors.New("boom")},
			want:  `{"ts":"2026-04-24T12:00:00Z","action_name":"eval","error":{"kind":"other","message":"boom"}}`,
		},
		{
			name:  "context timeout hits fallback with kind=timeout",
			entry: Entry{Time: ts, Action: "click", Error: context.DeadlineExceeded},
			want:  `{"ts":"2026-04-24T12:00:00Z","action_name":"click","error":{"kind":"timeout","message":"context deadline exceeded"}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.entry)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got:\n  %s\nwant:\n  %s", string(got), tc.want)
			}
		})
	}
}
