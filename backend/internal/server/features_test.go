package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/config"
	"github.com/ewanpy00/Global_AI/backend/internal/demo"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

func newDemoServer(t *testing.T) *httptest.Server {
	t.Helper()
	ds := &demo.Search{}
	f := verify.AllFeatures()
	f.Jury = true
	engine := &verify.Engine{LLM: &demo.LLM{}, Search: ds, Fetch: ds, MaxClaims: 12, Concurrency: 4, Features: f}
	srv := &Server{
		Cfg:         &config.Config{Demo: true, WebDir: t.TempDir(), VerifyTimeout: 30 * time.Second},
		Engine:      engine,
		Crawler:     ds,
		ArenaModels: demo.ArenaModels,
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// readSSE returns every event name with its raw data.
func readSSE(t *testing.T, res *http.Response) []struct {
	Event string
	Data  []byte
} {
	t.Helper()
	var out []struct {
		Event string
		Data  []byte
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	ev := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			ev = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			out = append(out, struct {
				Event string
				Data  []byte
			}{ev, []byte(strings.TrimPrefix(line, "data: "))})
		}
	}
	return out
}

func TestArenaRanksFreshestModelFirst(t *testing.T) {
	ts := newDemoServer(t)
	b, _ := json.Marshal(map[string]any{"question": "Is Nimbus Cloud a good choice?"})
	res, err := http.Post(ts.URL+"/api/arena", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var board []ArenaEntry
	answers := 0
	for _, e := range readSSE(t, res) {
		switch e.Event {
		case "arena_answer":
			answers++
		case "arena_done":
			var d struct {
				Leaderboard []ArenaEntry `json:"leaderboard"`
			}
			_ = json.Unmarshal(e.Data, &d)
			board = d.Leaderboard
		case "error":
			t.Fatalf("error event: %s", e.Data)
		}
	}
	if answers != 3 || len(board) != 3 {
		t.Fatalf("answers %d, leaderboard %+v", answers, board)
	}
	if board[0].Model != "demo/nova-2026" || board[2].Model != "demo/atlas-2024" {
		t.Fatalf("ranking: %s, %s, %s", board[0].Model, board[1].Model, board[2].Model)
	}
	if board[0].Stale != 0 || board[2].Stale < 4 || board[0].Score <= board[2].Score {
		t.Fatalf("leaderboard: %+v", board)
	}
}

func TestAuditRanksStalePagesFirst(t *testing.T) {
	ts := newDemoServer(t)
	b, _ := json.Marshal(map[string]any{"url": "nimbuscloud.example", "limit": 5})
	res, err := http.Post(ts.URL+"/api/audit", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var done AuditResult
	pageEvents := 0
	for _, e := range readSSE(t, res) {
		switch e.Event {
		case "audit_page":
			pageEvents++
		case "audit_done":
			_ = json.Unmarshal(e.Data, &done)
		case "error":
			t.Fatalf("error event: %s", e.Data)
		}
	}
	if pageEvents != 5 || len(done.Pages) != 5 {
		t.Fatalf("page events %d, pages %d", pageEvents, len(done.Pages))
	}
	if done.Stale < 4 || done.Pages[0].Stale < done.Pages[len(done.Pages)-1].Stale {
		t.Fatalf("audit: stale=%d pages=%+v", done.Stale, done.Pages)
	}
	if !strings.Contains(done.Pages[0].URL, "docs") && !strings.Contains(done.Pages[0].URL, "blog") {
		t.Errorf("expected the docs or blog page first, got %s", done.Pages[0].URL)
	}
}

func rpc(t *testing.T, url string, id int, method string, params any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	res, err := http.Post(url+"/mcp", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	if out["error"] != nil {
		t.Fatalf("%s: %v", method, out["error"])
	}
	return out["result"].(map[string]any)
}

func TestMCPOverHTTP(t *testing.T) {
	ts := newDemoServer(t)
	init := rpc(t, ts.URL, 1, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test"}})
	if init["protocolVersion"] != "2025-03-26" {
		t.Fatalf("initialize: %v", init)
	}
	// notification → 202, no body
	res, _ := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("notification status %d", res.StatusCode)
	}
	tools := rpc(t, ts.URL, 2, "tools/list", nil)["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("tools: %v", tools)
	}
	out := rpc(t, ts.URL, 3, "tools/call", map[string]any{"name": "check_freshness", "arguments": map[string]any{"text": demo.SampleAnswer}})
	text := out["content"].([]any)[0].(map[string]any)["text"].(string)
	if out["isError"] != false || !strings.Contains(text, "Freshness") || !strings.Contains(text, "Now: $15") {
		t.Fatalf("check_freshness: %v", out)
	}
	fix := rpc(t, ts.URL, 4, "tools/call", map[string]any{"name": "correct_text", "arguments": map[string]any{"text": demo.SampleAnswer}})
	fixed := fix["structuredContent"].(map[string]any)["corrected_text"].(string)
	if !strings.Contains(fixed, "$15 per user per month") || strings.Contains(fixed, "$12 per user") {
		t.Fatalf("correct_text: %s", fixed)
	}
	bad := rpc(t, ts.URL, 5, "tools/call", map[string]any{"name": "check_freshness", "arguments": map[string]any{}})
	if bad["isError"] != true {
		t.Fatalf("missing argument should be a tool error: %v", bad)
	}
}

func TestMCPOverStdio(t *testing.T) {
	ts := newDemoServer(t)
	_ = ts
	srv := &Server{Cfg: &config.Config{Demo: true, VerifyTimeout: 10 * time.Second}, Engine: &verify.Engine{LLM: &demo.LLM{}, Search: &demo.Search{}}}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
`)
	var out bytes.Buffer
	if err := srv.MCP().ServeStdio(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(out.String(), `"check_freshness"`) {
		t.Fatalf("stdio output:\n%s", out.String())
	}
}
