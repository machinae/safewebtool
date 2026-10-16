// Package cdp for application layer communication with browser.
package cdp

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/machinae/safewebtool/internal/rod/lib/defaults"
	"github.com/machinae/safewebtool/internal/rod/lib/utils"
)

// Request to send to browser.
type Request struct {
	ID        int         `json:"id"`
	SessionID string      `json:"sessionId,omitempty"`
	Method    string      `json:"method"`
	Params    interface{} `json:"params,omitempty"`
}

// Response from browser.
type Response struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Event from browser.
type Event struct {
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// WebSocketable enables you to choose the websocket lib you want to use.
// Such as you can easily wrap gorilla/websocket and use it as the transport layer.
type WebSocketable interface {
	// Send text message only
	Send(data []byte) error
	// Read returns text message only
	Read() ([]byte, error)
	// Close the connection. Called to unblock an in-flight Send when the
	// caller's context is cancelled while the Send is still running.
	Close() error
}

// Client is a devtools protocol connection instance.
type Client struct {
	count uint64

	ws WebSocketable

	pending sync.Map    // pending requests
	event   chan *Event // events from browser

	// How long a Send may straddle its caller's cancelled ctx before it's
	// declared stuck and the connection is closed to unblock it. The
	// connection is shared by every session, so closing it must be a last
	// resort, not a side effect of one call's deadline.
	sendGrace time.Duration

	logger utils.Logger
}

// New creates a cdp connection, all messages from Client.Event must be received or they will block the client.
func New() *Client {
	return &Client{
		event:     make(chan *Event),
		sendGrace: time.Second,
		logger:    defaults.CDP,
	}
}

// Logger sets the logger to log all the requests, responses, and events transferred between Rod and the browser.
// The default format for each type is in file format.go.
func (cdp *Client) Logger(l utils.Logger) *Client {
	cdp.logger = l
	return cdp
}

// Start to browser.
func (cdp *Client) Start(ws WebSocketable) *Client {
	cdp.ws = ws

	go cdp.consumeMessages()

	return cdp
}

type result struct {
	msg json.RawMessage
	err error
}

// Call a method and wait for its response.
func (cdp *Client) Call(ctx context.Context, sessionID, method string, params interface{}) ([]byte, error) {
	// Fast path for stale/cancelled callers: don't allocate a request, don't
	// register pending state, don't touch the socket. This matters in queue/
	// worker designs where jobs can become stale before they reach Call, and
	// ensures a cancelled parent never closes a healthy CDP connection.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	req := &Request{
		ID:        int(atomic.AddUint64(&cdp.count, 1)),
		SessionID: sessionID,
		Method:    method,
		Params:    params,
	}

	cdp.logger.Println(req)

	data, err := json.Marshal(req)
	utils.E(err)

	// Buffered so the pump (or the death sweep) never blocks on a caller
	// that already gave up; the once guards against double delivery when a
	// response and the sweep race.
	done := make(chan result, 1)
	once := sync.Once{}
	cdp.pending.Store(req.ID, func(res result) {
		once.Do(func() {
			done <- res
		})
	})
	defer cdp.pending.Delete(req.ID)

	// Run Send in a goroutine so a stuck ws.Write doesn't prevent us from
	// honoring ctx cancellation. Buffered so the goroutine can always exit
	// even if Call already returned.
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- cdp.ws.Send(data)
	}()

	select {
	case err := <-sendErr:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		// ctx fired during the send window. This does NOT prove the Send is
		// stuck: sendErr is buffered, so a completed Send can land here on
		// the select's random pick, and a slow large write can straddle the
		// deadline on a healthy connection. Drain sendErr for a grace period
		// and only close the shared connection (unblocking the Write, which
		// lets the goroutine exit) when the Send is provably wedged.
		timer := time.NewTimer(cdp.sendGrace)
		defer timer.Stop()
		select {
		case err := <-sendErr:
			if err != nil {
				return nil, err
			}
		case <-timer.C:
			_ = cdp.ws.Close()
		}
		return nil, ctx.Err()
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-done:
		return res.msg, res.err
	}
}

// Event returns a channel that will emit browser devtools protocol events. Must be consumed or will block producer.
func (cdp *Client) Event() <-chan *Event {
	return cdp.event
}

// Consume messages coming from the browser via the websocket.
func (cdp *Client) consumeMessages() {
	defer close(cdp.event)

	for {
		data, err := cdp.ws.Read()
		if err != nil {
			cdp.pending.Range(func(_, val interface{}) bool {
				val.(func(result))(result{err: err}) //nolint: forcetypeassert
				return true
			})
			return
		}

		var id struct {
			ID int `json:"id"`
		}
		err = json.Unmarshal(data, &id)
		utils.E(err)

		if id.ID == 0 {
			var evt Event
			err := json.Unmarshal(data, &evt)
			utils.E(err)
			cdp.logger.Println(&evt)
			cdp.event <- &evt
			continue
		}

		var res Response
		err = json.Unmarshal(data, &res)
		utils.E(err)

		cdp.logger.Println(&res)

		val, ok := cdp.pending.Load(id.ID)
		if !ok {
			continue
		}
		if res.Error == nil {
			val.(func(result))(result{res.Result, nil}) //nolint: forcetypeassert
		} else {
			val.(func(result))(result{nil, res.Error}) //nolint: forcetypeassert
		}
	}
}
