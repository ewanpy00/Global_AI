// Package mcp is a minimal Model Context Protocol server (JSON-RPC 2.0) with two transports:
// Streamable HTTP (one POST per message, JSON response) and stdio (newline-delimited JSON).
// It implements what agents need to call tools: initialize, ping, tools/list and tools/call.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
)

// LatestProtocol is the newest MCP revision this server speaks; older clients get theirs echoed.
const LatestProtocol = "2025-06-18"

var supported = map[string]bool{"2024-11-05": true, "2025-03-26": true, "2025-06-18": true}

// Tool is one callable tool.
type Tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	// Handler returns a short human-readable text plus structured data for the agent.
	Handler func(ctx context.Context, args json.RawMessage) (text string, structured any, err error) `json:"-"`
}

// Server dispatches JSON-RPC messages to tools.
type Server struct {
	Name         string
	Version      string
	Instructions string
	tools        []Tool
}

func NewServer(name, version, instructions string) *Server {
	return &Server{Name: name, Version: version, Instructions: instructions}
}

func (s *Server) AddTool(t Tool) { s.tools = append(s.tools, t) }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Handle processes one message. It returns nil for notifications (no reply expected).
func (s *Server) Handle(ctx context.Context, raw []byte) *response {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return &response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}}
	}
	if len(req.ID) == 0 {
		return nil // notification (e.g. notifications/initialized)
	}
	res := &response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		v := LatestProtocol
		if supported[p.ProtocolVersion] {
			v = p.ProtocolVersion
		}
		res.Result = map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
			"instructions":    s.Instructions,
		}
	case "ping":
		res.Result = map[string]any{}
	case "tools/list":
		res.Result = map[string]any{"tools": s.tools}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			res.Error = &rpcError{-32602, "invalid params"}
			return res
		}
		var tool *Tool
		for i := range s.tools {
			if s.tools[i].Name == p.Name {
				tool = &s.tools[i]
			}
		}
		if tool == nil {
			res.Error = &rpcError{-32602, fmt.Sprintf("unknown tool %q", p.Name)}
			return res
		}
		if len(p.Arguments) == 0 {
			p.Arguments = json.RawMessage("{}")
		}
		text, structured, err := tool.Handler(ctx, p.Arguments)
		if err != nil {
			// Tool errors are results with isError, so the model can see and react to them.
			res.Result = map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}
			return res
		}
		out := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false}
		if structured != nil {
			out["structuredContent"] = structured
		}
		res.Result = out
	default:
		res.Error = &rpcError{-32601, "method not found: " + req.Method}
	}
	return res
}

// ServeHTTP implements the Streamable HTTP transport without server-initiated streams:
// POST a JSON-RPC message, get the JSON response (202 for notifications).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP endpoint accepts POST only (no server-initiated stream)", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	res := s.Handle(r.Context(), body)
	if res == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// ServeStdio reads newline-delimited JSON-RPC messages from in and writes replies to out.
// Requests run concurrently (a long freshness check must not block a ping).
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	var mu sync.Mutex
	var wg sync.WaitGroup
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		if len(line) == 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := s.Handle(ctx, line)
			if res == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if err := enc.Encode(res); err != nil {
				log.Printf("mcp: write: %v", err)
			}
		}()
	}
	wg.Wait()
	return sc.Err()
}
