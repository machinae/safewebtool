package browser

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/machinae/safewebtool/internal/rod/lib/cdp"
)

// Compile-time assertion that cdpWebSocket satisfies rod's transport
// interface, so a signature drift fails the build instead of at runtime.
var _ cdp.WebSocketable = (*cdpWebSocket)(nil)

// cdpReadLimit caps the size of a single CDP message read from Chrome.
// CDP delivers each command response as one websocket message, so this
// must exceed the largest payload we expect. Full-page screenshots are
// the driver: CDP returns each as a single base64 message that can run to
// several MB. coder/websocket closes the connection when a message exceeds
// the limit, so this also backstops a runaway response from a trusted
// loopback Chrome.
const cdpReadLimit = 32 << 20 // 32 MiB

const (
	// cdpWriteTimeout bounds a single Send so a wedged write cannot block
	// a CDP call forever. Generous because loopback writes are instant.
	cdpWriteTimeout = 10 * time.Second
	// cdpPingInterval is how often we ping Chrome to keep the connection
	// observably alive and to detect a silently dead socket.
	cdpPingInterval = 20 * time.Second
	// cdpPingTimeout bounds the wait for a pong. A miss means the socket
	// is dead even though TCP has not noticed yet (a half-open connection).
	cdpPingTimeout = 10 * time.Second
)

// cdpWebSocket adapts coder/websocket to rod's cdp.WebSocketable
// (Send/Read/Close). Unlike rod's built-in minimal websocket client it
// understands control frames (ping/pong/close) and lets us run an
// application-level keepalive, so a connection Chrome has dropped
// surfaces as a clean read error instead of a frame-parse panic or an
// indefinite hang.
//
// rod's cdp.Client is the context-aware layer above this; the transport
// itself is intentionally context-free (like net.Conn), interrupted by
// Close(). We hold a connection-lifetime context internally and cancel
// it in Close() to honour that contract: rod calls Close() to unblock a
// Send that is stuck on a dead socket.
type cdpWebSocket struct {
	conn *websocket.Conn
	// ctx/cancel bound the keepAlive goroutine and are cancelled exactly
	// once in Close(). This models the connection's own lifetime, not a
	// request's — the idiomatic exception to "no contexts in structs"
	// (same pattern as rod.Browser and Tab). Send/Read deliberately do
	// NOT read with it; see their docs.
	ctx    context.Context
	cancel context.CancelFunc
}

// dialCDP opens a CDP websocket to Chrome at wsURL and returns a
// transport ready to hand to cdp.New().Start. headers are sent on the
// handshake. ctx bounds only the handshake, not the connection lifetime.
func dialCDP(ctx context.Context, wsURL string, headers http.Header) (*cdpWebSocket, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(cdpReadLimit)

	connCtx, cancel := context.WithCancel(context.Background())
	ws := &cdpWebSocket{conn: conn, ctx: connCtx, cancel: cancel}
	go ws.keepAlive()
	return ws, nil
}

// Send writes one CDP message as a text frame. coder/websocket serializes
// concurrent writes internally, so no extra locking is needed here.
//
// The write deadline derives from context.Background(), not ws.ctx: coder
// prefers ctx.Err() over net.ErrClosed on a failed write (write.go), so a
// Close()-cancelled ws.ctx would rewrite the error to context.Canceled,
// which does not classify as a ConnectionError. CloseNow() still unblocks
// an in-flight write, which then correctly reports net.ErrClosed.
func (ws *cdpWebSocket) Send(data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), cdpWriteTimeout)
	defer cancel()
	return ws.conn.Write(ctx, websocket.MessageText, data)
}

// Read returns the next CDP message. It reads with context.Background(),
// not ws.ctx, and blocks until a message arrives or the connection closes;
// CloseNow() in Close() unblocks it. Using Background means a Close()-driven
// unblock reports net.ErrClosed (a ConnectionError) instead of
// context.Canceled, which coder would otherwise prefer (read.go finishRead).
// This matches rod's own context-free Read, which blocks purely on the conn.
func (ws *cdpWebSocket) Read() ([]byte, error) {
	_, data, err := ws.conn.Read(context.Background())
	return data, err
}

// Close tears down the connection immediately and stops keepAlive.
// CloseNow() unblocks any in-flight Send/Read — which read/write with
// context.Background(), so they report net.ErrClosed (a ConnectionError) —
// and cancel() stops the keepAlive goroutine. Idempotent: CloseNow is
// CAS-guarded (returns net.ErrClosed on later calls) and cancel is a no-op
// after the first. rod calls Close() both for normal shutdown and to
// interrupt a Send stuck on a dead socket.
func (ws *cdpWebSocket) Close() error {
	err := ws.conn.CloseNow()
	ws.cancel()
	return err
}

// keepAlive pings Chrome on an interval. A failed ping (no pong before
// cdpPingTimeout, or a write error) means the connection is dead, so we
// close it; rod's read loop then surfaces a connection error to callers
// and IsConnected flips to false. Exits when the connection is closed.
func (ws *cdpWebSocket) keepAlive() {
	ticker := time.NewTicker(cdpPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ws.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(ws.ctx, cdpPingTimeout)
			err := ws.conn.Ping(ctx)
			cancel()
			if err != nil {
				_ = ws.Close()
				return
			}
		}
	}
}
