// Package mcp implements a minimal Model Context Protocol (MCP) server.
//
// Two transports are supported on top of the same JSON-RPC 2.0 dispatch core:
//   - stdio: newline-delimited JSON via Server.Serve(io.Reader).
//   - HTTP: a single endpoint POST handler via Server.ServeHTTP that accepts
//     a JSON-RPC request body and returns the JSON-RPC response.
//
// Implemented methods:
//   - initialize / notifications/initialized
//   - ping
//   - tools/list
//   - tools/call
//
// Currently a single tool is exposed:
//   - get_telegram_feed: fetches a public Telegram channel feed as JSON,
//     reusing the same logic as the HTTP /feed/{channel} endpoint.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/Goalt/tg-channel-to-rss/internal/app"
	"github.com/Goalt/tg-channel-to-rss/internal/polymarket"
)

const (
	ProtocolVersion = "2024-11-05"
	ServerName      = "llm-signals-mcp"
	ServerVersion   = "0.1.0"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// Server is a stateless MCP server. Construct with New and drive with Serve.
type Server struct {
	app        *app.Service
	polymarket *polymarket.Service
	tools      []toolDef
	writeMu    sync.Mutex
	out        io.Writer
	errOut     io.Writer
}

// New returns a Server that writes JSON-RPC responses to out and unrecoverable
// transport errors to errOut. errOut may be nil.
func New(out, errOut io.Writer) *Server {
	s := &Server{
		app:        app.NewService(http.DefaultClient),
		polymarket: polymarket.NewService("", nil),
		out:        out,
		errOut:     errOut,
	}
	s.tools = []toolDef{
		{
			Name:        "get_telegram_feed",
			Description: "Fetch a public Telegram channel's posts as JSON via t.me/s/{channel}.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"channel": map[string]any{
						"type":        "string",
						"description": "Public Telegram channel username (without @).",
					},
				},
				"required":             []string{"channel"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "get_polymarket_events",
			Description: "Fetch Polymarket events/markets with optional filtering. Returns JSON feed format.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"endpoint": map[string]any{
						"type":        "string",
						"description": "API endpoint path: 'sampling-markets' (default), 'markets', or custom path like '/sampling-markets?limit=10&closed=false'.",
						"default":     "sampling-markets",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of events to return (applied as query param if endpoint doesn't already include it).",
					},
					"closed": map[string]any{
						"type":        "boolean",
						"description": "Include closed markets (true) or only active (false). Applied as query param if endpoint supports it.",
					},
					"active": map[string]any{
						"type":        "boolean",
						"description": "Filter to only active markets. Applied as query param if endpoint supports it.",
					},
				},
				"additionalProperties": false,
			},
		},
	}
	return s
}

// Serve reads newline-delimited JSON-RPC requests from in until EOF or an
// unrecoverable read error.
func (s *Server) Serve(in io.Reader) error {
	scanner := bufio.NewScanner(in)
	// Allow large messages (tool outputs, etc.).
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		s.handleLine(line)
	}
	return scanner.Err()
}

func (s *Server) handleLine(line []byte) {
	resp, ok := s.process(line)
	if !ok {
		return
	}
	s.writeResponse(resp)
}

// process parses a single JSON-RPC request payload, dispatches it, and
// returns the response together with a boolean indicating whether a response
// should be sent back to the caller (notifications return ok=false).
func (s *Server) process(payload []byte) (rpcResponse, bool) {
	var req rpcRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return rpcResponse{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
		}, true
	}

	// Notifications have no id; do not respond.
	isNotification := len(req.ID) == 0

	result, rpcErr := s.dispatch(req)

	if isNotification {
		return rpcResponse{}, false
	}

	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	return resp, true
}

func (s *Server) dispatch(req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    ServerName,
				"version": ServerVersion,
			},
		}, nil
	case "notifications/initialized", "initialized":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools}, nil
	case "tools/call":
		return s.callTool(req.Params)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func (s *Server) callTool(raw json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
		}
	}
	switch p.Name {
	case "get_telegram_feed":
		channel, _ := p.Arguments["channel"].(string)
		if channel == "" {
			return toolErr("channel argument is required"), nil
		}
		status, body, _ := s.app.HandleFeedRequest(channel)
		if status < 200 || status >= 300 {
			return toolErr(fmt.Sprintf("feed fetch failed (status %d): %s", status, body)), nil
		}
		return toolResult{Content: []contentItem{{Type: "text", Text: body}}}, nil
	case "get_polymarket_events":
		endpoint, _ := p.Arguments["endpoint"].(string)
		if endpoint == "" {
			endpoint = "sampling-markets"
		}

		// Build query params if not already in endpoint
		if !strings.Contains(endpoint, "?") {
			params := url.Values{}
			if limit, ok := p.Arguments["limit"].(float64); ok && limit > 0 {
				params.Set("limit", fmt.Sprintf("%.0f", limit))
			}
			if closed, ok := p.Arguments["closed"].(bool); ok {
				params.Set("closed", fmt.Sprintf("%t", closed))
			}
			if active, ok := p.Arguments["active"].(bool); ok {
				params.Set("active", fmt.Sprintf("%t", active))
			}
			if len(params) > 0 {
				endpoint = endpoint + "?" + params.Encode()
			}
		}

		body, err := s.polymarket.GetJSONFeed(endpoint)
		if err != nil {
			return toolErr(fmt.Sprintf("polymarket fetch failed: %s", err.Error())), nil
		}
		return toolResult{Content: []contentItem{{Type: "text", Text: body}}}, nil
	default:
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
	}
}

func toolErr(msg string) toolResult {
	return toolResult{
		IsError: true,
		Content: []contentItem{{Type: "text", Text: msg}},
	}
}

func (s *Server) writeResponse(resp rpcResponse) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	enc := json.NewEncoder(s.out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resp); err != nil && s.errOut != nil {
		fmt.Fprintf(s.errOut, "mcp: write response: %v\n", err)
	}
}

// ServeHTTP implements http.Handler. It accepts a single JSON-RPC request in
// the request body (POST) and writes the JSON-RPC response. Notifications
// (requests without an id) receive HTTP 204 with no body.
//
// Only application/json is accepted. The endpoint is intentionally simple:
// it does not implement Server-Sent Events, sessions, or batched requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Some clients probe the endpoint with GET; advertise basic info.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":            ServerName,
			"version":         ServerVersion,
			"protocolVersion": ProtocolVersion,
			"transport":       "http",
		})
		return
	case http.MethodPost:
		// handled below
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8*1024*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	resp, ok := s.process(body)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resp); err != nil && s.errOut != nil {
		fmt.Fprintf(s.errOut, "mcp: write http response: %v\n", err)
	}
}
