package browser

import (
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/machinae/safewebtool/internal/rod"
	"github.com/machinae/safewebtool/policy"
)

// Browser holds a connection to a running Chrome instance and coordinates
// access to every tab. It owns the known-tab map (entries added on first
// reference, removed on close or reconciliation) and the hijack router
// lifecycle. The Browser itself is a coordinator, not a serializer — all
// per-tab work flows through per-tab worker queues.
type Browser struct {
	// mu guards known and policy. Held briefly and NEVER across a
	// Rod/CDP round-trip. Router lifecycle runs under t.routerMu
	// (per-tab), outside b.mu, so a slow Chrome on one tab cannot stall
	// every other coordinator op.
	mu sync.Mutex

	// rod is the underlying Rod browser client. nil until Connect, nil
	// after shutdown. Read via getRod(); written via rod.Store() in
	// Connect and shutdown.
	rod atomic.Pointer[rod.Browser]
	// ws is the underlying CDP WebSocket, kept for a clean Close().
	ws *cdpWebSocket

	// WSUrl is the Chrome DevTools WebSocket URL.
	WSUrl string
	// Headers are sent during the WebSocket handshake.
	Headers http.Header
	// ChromeDataDir is used for DevToolsActivePort discovery.
	ChromeDataDir string

	// known maps full CDP target ID → *Tab for every tab we have adopted.
	// Entries are added on first reference by Tab() or OpenTab() and
	// removed by CloseTab() on clean close or by reconciliation when
	// Chrome no longer lists the target.
	known map[string]*Tab

	// policy is the security policy for request interception. nil means
	// no policy is configured.
	policy *policy.Policy

	// maxTabs caps how many tabs may be attached at once. 0 means no
	// limit. Only OpenTab enforces the cap; attaching to a tab that
	// already exists in Chrome always succeeds.
	maxTabs int
}

// New creates a new Browser ready to connect.
func New() *Browser {
	return &Browser{known: make(map[string]*Tab)}
}

// WithChromeDataDir sets the Chrome user data directory for DevToolsActivePort discovery.
func (b *Browser) WithChromeDataDir(dir string) *Browser {
	b.ChromeDataDir = dir
	return b
}

// WithPolicy sets the security policy for request interception.
func (b *Browser) WithPolicy(p *policy.Policy) *Browser {
	b.mu.Lock()
	b.policy = p
	b.mu.Unlock()
	return b
}

// WithMaxTabs caps how many tabs may be attached at once (0 = no limit).
func (b *Browser) WithMaxTabs(n int) *Browser {
	b.maxTabs = n
	return b
}

// Policy returns the security policy, or nil if none is configured.
func (b *Browser) Policy() *policy.Policy {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.policy
}

// ReloadPolicy swaps the security policy and rebuilds hijack routers on
// every known tab. The policy swap and tab snapshot happen under b.mu;
// the per-tab CDP work (Fetch.disable, Fetch.enable, N × Add) runs
// OUTSIDE b.mu so one slow tab does not block reloads on the others.
func (b *Browser) ReloadPolicy(p *policy.Policy) error {
	b.mu.Lock()
	b.policy = p
	tabs := make([]*Tab, 0, len(b.known))
	for _, t := range b.known {
		tabs = append(tabs, t)
	}
	b.mu.Unlock()

	for _, t := range tabs {
		if err := b.resetHijack(t); err != nil {
			return err
		}
	}
	return nil
}

// WithURL sets an explicit debugging WebSocket URL, skipping DevToolsActivePort discovery.
func (b *Browser) WithURL(wsURL string) *Browser {
	b.WSUrl = wsURL
	return b
}

// WithHeaders sets HTTP headers sent during the WebSocket handshake.
func (b *Browser) WithHeaders(h http.Header) *Browser {
	b.Headers = h
	return b
}
