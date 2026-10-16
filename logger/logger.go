package logger

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"time"
)

// Entry is a single audit log record.
type Entry struct {
	// Time is the UTC timestamp of the action.
	Time time.Time `json:"ts"`
	// Action is the action name (click, type, open, etc.).
	Action string `json:"action_name"`
	// Target is what was acted on: element description, URL, key name, etc.
	Target string `json:"target,omitempty"`
	// URL is the page URL at the time of the action.
	URL string `json:"url,omitempty"`
	// Data is action-specific payload (e.g. eval result).
	Data string `json:"data,omitempty"`
	// Reason is the model's short explanation for why it chose this
	// action. Captured at the tool boundary and stamped on every entry.
	Reason string `json:"reason,omitempty"`
	// Error is the typed error, nil on success. Callers pass the raw
	// error so MarshalJSON can emit its structured form — see below.
	Error error `json:"-"`
}

// MarshalJSON emits Entry as JSON with the error as a structured object
// rather than a flat string, so downstream consumers can discriminate on
// error kind and access typed fields (e.g. ErrBlocked.Rule) without
// substring-matching messages.
//
// Each concrete error type in browser/errors.go is expected to implement
// json.Marshaler and produce {"kind": "...", "message": "...", ...fields}.
// Errors that don't implement it (wrapped errors, stdlib errors.New)
// would otherwise serialize to "{}", so we fall back to a synthetic
// {"kind":"other","message":err.Error()} to preserve the message.
//
// Implementation note: `type alias Entry` is the standard Go idiom for
// defining custom JSON marshaling without infinite recursion. It creates
// a new named type with Entry's fields but none of Entry's methods
// (method sets do not transfer across type definitions), so embedding it
// lets json.Marshal walk the fields normally instead of calling back
// into this method.
func (e Entry) MarshalJSON() ([]byte, error) {
	type alias Entry

	var errVal any
	if e.Error != nil {
		if _, ok := e.Error.(json.Marshaler); ok {
			errVal = e.Error
		} else {
			errVal = map[string]string{
				"kind":    errorKind(e.Error),
				"message": e.Error.Error(),
			}
		}
	}

	return json.Marshal(struct {
		alias
		Error any `json:"error,omitempty"`
	}{alias(e), errVal})
}

// errorKind returns a machine-readable label for an error that doesn't
// implement json.Marshaler. Used only by the fallback branch of
// MarshalJSON; typed errors in browser/errors.go carry their own kind
// via their MarshalJSON methods.
//
// Uses errors.Is / errors.As (no reflection) so wrapped sentinels and
// typed errors still match through fmt.Errorf("...: %w", err) wrappers.
func errorKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	// net.Error covers *net.OpError, *net.DNSError, *url.Error, etc.
	// Timeout() distinguishes deadline/i-o timeouts from other failures.
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "network"
	}
	return "other"
}

// Logger writes structured audit log entries.
type Logger interface {
	Log(ctx context.Context, entry Entry)
	Close() error
}

// FileLogger writes JSONL entries to a file. Write-only.
type FileLogger struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

// NewFileLogger opens the given path for append-only JSONL writing.
func NewFileLogger(path string) (*FileLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	return &FileLogger{
		file: f,
		enc:  json.NewEncoder(f),
	}, nil
}

// Log writes an entry to the file. The timestamp is set automatically.
func (l *FileLogger) Log(_ context.Context, entry Entry) {
	entry.Time = time.Now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.enc.Encode(entry)
}

// Close closes the underlying file.
func (l *FileLogger) Close() error {
	return l.file.Close()
}
