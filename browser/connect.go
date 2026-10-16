package browser

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/machinae/safewebtool/config"
	"github.com/machinae/safewebtool/internal/rod"
	"github.com/machinae/safewebtool/internal/rod/lib/cdp"
)

// Connect establishes a connection to Chrome.
// If no URL was set, it discovers Chrome via DevToolsActivePort in the user data directory.
// Safe to call multiple times — returns immediately if already connected.
func (b *Browser) Connect(ctx context.Context) error {
	if b.rod.Load() != nil {
		return nil
	}

	wsURL, err := b.resolveWSUrl()
	if err != nil {
		return err
	}

	wsURL, err = normalizeURL(wsURL)
	if err != nil {
		return err
	}

	ws, err := dialCDP(ctx, wsURL, b.Headers)
	if err != nil {
		return newConnError(err)
	}
	client := cdp.New().Start(ws)
	rb := rod.New().Client(client).NoDefaultDevice()
	// In demo mode, draw a visible gliding cursor on the page so the agent's
	// actions are watchable in screen recordings — CDP-driven input moves no
	// real OS cursor. Off unless webtool mcp runs with --demo.
	if config.AppConfig.DemoMode {
		rb.ShowCursor(true)
	}
	if err = rb.Connect(); err != nil {
		ws.Close()
		return newConnError(err)
	}
	b.ws = ws
	b.rod.Store(rb)

	return nil
}

// resolveWSUrl returns the CDP websocket URL to dial: the explicitly
// configured WSUrl if set, otherwise one discovered fresh from
// DevToolsActivePort. The discovered URL is deliberately not cached — Chrome
// regenerates the browser path (and, with an ephemeral port, the port) on
// every launch, so each Connect must re-resolve to reach the live instance.
func (b *Browser) resolveWSUrl() (string, error) {
	if b.WSUrl != "" {
		return b.WSUrl, nil
	}

	if b.ChromeDataDir == "" {
		b.ChromeDataDir = config.AppConfig.ChromeDataDir
	}
	if b.ChromeDataDir == "" {
		return "", fmt.Errorf("chrome data directory not found, your OS may be unsupported")
	}

	return discoverWSURLFromDir(b.ChromeDataDir)
}

// Close disconnects from Chrome without closing it. Cancels every tab
// worker, clears coordinator state, and tears down the CDP websocket.
// Safe to Connect() again afterwards. Rod's per-router event loops
// exit on their own once the ws dies.
func (b *Browser) Close() error {
	b.mu.Lock()
	for _, t := range b.known {
		t.cancel()
	}
	b.known = make(map[string]*Tab)
	b.rod.Store(nil)
	ws := b.ws
	b.ws = nil
	b.mu.Unlock()

	if ws != nil {
		return ws.Close()
	}
	return nil
}

// IsConnected returns true if the browser has an active connection.
func (b *Browser) IsConnected() bool {
	return b.rod.Load() != nil
}

// getRod returns the Rod client if connected, or an error if the Browser
// is not connected (pre-Connect or post-shutdown). Callers should capture
// the returned pointer once at the top of a method and use it for the
// remainder; do not re-read b.rod within the same call.
func (b *Browser) getRod() (*rod.Browser, error) {
	rb := b.rod.Load()
	if rb == nil {
		return nil, fmt.Errorf("browser not connected")
	}
	return rb, nil
}

// discoverWSURLFromDir reads DevToolsActivePort from a given directory.
func discoverWSURLFromDir(dataDir string) (string, error) {
	portFile := filepath.Join(dataDir, "DevToolsActivePort")
	data, err := os.ReadFile(portFile)
	if err != nil {
		return "", fmt.Errorf("could not find Chrome data directory")
	}

	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) < 2 {
		return "", fmt.Errorf("unexpected DevToolsActivePort format")
	}

	port := strings.TrimSpace(lines[0])
	path := strings.TrimSpace(lines[1])
	return fmt.Sprintf("ws://127.0.0.1:%s%s", port, path), nil
}

// Copied from go-rod/rod/lib/launcher (ResolveURL normalization, without the HTTP request).
var (
	regPort     = regexp.MustCompile(`^\:?(\d+)$`)
	regProtocol = regexp.MustCompile(`^\w+://`)
)

// normalizeURL normalizes a CDP URL into a ws:// URL that rod can connect to.
// Accepts formats: "9222", ":9222", "host:9222", "ws://host:9222/path",
// "http://host:9222/path".
func normalizeURL(u string) (string, error) {
	if u == "" {
		u = "9222"
	}

	u = strings.TrimSpace(u)
	u = regPort.ReplaceAllString(u, "127.0.0.1:$1")

	if !regProtocol.MatchString(u) {
		u = "ws://" + u
	}

	parsed, err := url.Parse(u)
	if err != nil {
		return "", err
	}

	// Ensure ws scheme
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	}

	return parsed.String(), nil
}
