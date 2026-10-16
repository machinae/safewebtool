package mcp

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/machinae/safewebtool/browser"
	"github.com/machinae/safewebtool/logger"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

//go:embed tool_description.md
var toolDescription string

//go:embed tool_schema.json
var toolSchema []byte

// serverName and serverVersion identify this MCP server to clients.
const (
	serverName          = "webtool"
	serverVersion       = "0.5.0"
	totalMCPCallTimeout = 300 * time.Second
)

// Server wraps a browser.Browser and exposes it as an MCP tool. The
// Browser owns all per-tab concurrency; the MCP server is a thin
// adapter: bind, authorize, run, render. No job queue here; actions
// flow directly to the browser coordinator and its per-tab workers.
type Server struct {
	browser *browser.Browser
	logger  logger.Logger
	// connMu serializes browser Connect and Close so concurrent calls
	// dial Chrome once. Held only around those two calls, never while
	// actions run.
	connMu sync.Mutex
}

// Option configures a Server.
type Option func(*Server)

// WithLogger sets the structured action logger. Nil by default (no logging).
func WithLogger(l logger.Logger) Option {
	return func(s *Server) { s.logger = l }
}

// NewServer creates an MCP server wrapping the given browser.
func NewServer(b *browser.Browser, opts ...Option) *Server {
	s := &Server{browser: b}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// newWebtoolTool returns the tool definition. The schema is the embedded
// tool_schema.json: a flat action object with the verb enum and every
// per-action field documented in place. Clients truncate tool
// descriptions (Claude Code cuts them at 2K) but pass the input schema
// through whole, so the action inventory lives in the schema. Clients do
// not validate against it; bindCall stays the authority and teaches by
// corrective example. Boring primitives only (no $ref, anyOf, or
// schema-valued additionalProperties) because client schema converters
// mangle anything fancier. TestToolSchemaMatchesCall keeps the schema
// and bindCall in sync.
func newWebtoolTool() mcp.Tool {
	return mcp.NewToolWithRawSchema("webtool", toolDescription, toolSchema)
}

// mcpServer creates the underlying mcp-go server with the webtool tool registered.
func (s *Server) mcpServer() *server.MCPServer {
	srv := server.NewMCPServer(serverName, serverVersion)
	srv.AddTool(newWebtoolTool(), s.handleToolCall)
	return srv
}

// handleToolCall is the MCP tool handler for the "webtool" tool:
// bind, authorize, connect, run, render. The browser connects on the
// first call, so starting the server does not trigger Chrome's
// remote-debugging prompt; after a dropped connection the next call
// reconnects. IsError is true exactly when nothing executed: validation
// failure, policy rejection, failed connect, or a fatal error that
// aborted the run. The per-tab worker queue inside browser handles
// serialization; there is no MCP-level queue.
func (s *Server) handleToolCall(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := json.Marshal(request.GetRawArguments())
	if err != nil {
		return mcp.NewToolResultError("arguments are not valid JSON: " + err.Error()), nil
	}

	c, failure := bindCall(raw)
	if failure != nil {
		return mcp.NewToolResultError("invalid call, nothing was executed:\n" + s.render(failure)), nil
	}

	// Bound the full MCP call independently from the per-command budget.
	ctx, cancel := context.WithTimeout(ctx, totalMCPCallTimeout)
	defer cancel()

	if err := s.authorizeCall(c); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	// Connect returns at once when already connected. ctx bounds only
	// the handshake, which includes waiting on Chrome's approval prompt;
	// the connection outlives this call.
	s.connMu.Lock()
	err = s.browser.Connect(ctx)
	s.connMu.Unlock()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	resp, err := s.run(ctx, c)
	if err != nil {
		// A dead connection means our view of "connected" is stale.
		// Tear down so the next call reconnects.
		var cerr browser.ConnectionError
		if errors.As(err, &cerr) {
			s.connMu.Lock()
			_ = s.browser.Close()
			s.connMu.Unlock()
			return mcp.NewToolResultError(err.Error() + ". The next call reconnects"), nil
		}
		// Context death (timeout or cancel): the connection is fine.
		return mcp.NewToolResultError(err.Error()), nil
	}

	result := mcp.NewToolResultText(s.render(resp))
	// Pathless screenshots ride along as image blocks after the JSON
	// text; their slots say "inline".
	for _, tr := range resp.Tabs {
		for _, slot := range tr.Results {
			if slot.image != "" {
				result.Content = append(result.Content, mcp.NewImageContent(slot.image, "image/jpeg"))
			}
		}
	}
	return result, nil
}

// ServeStdio serves MCP over stdin and stdout until stdin closes or the
// process gets SIGINT or SIGTERM. Nothing else may write to stdout while
// it runs.
func (s *Server) ServeStdio() error {
	err := server.ServeStdio(s.mcpServer())
	if s.logger != nil {
		err = errors.Join(err, s.logger.Close())
	}
	return err
}
