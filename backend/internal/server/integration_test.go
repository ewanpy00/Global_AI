package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/config"
	"github.com/ewanpy00/Global_AI/backend/internal/llm"
	"github.com/ewanpy00/Global_AI/backend/internal/plugin"
	"github.com/ewanpy00/Global_AI/backend/internal/search"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

const chatAnswer = "Acme Pro costs $10 per month. Acme was founded in 1999 by two engineers."

// fakeOpenAI is an OpenAI-compatible provider. Bifrost's openai provider is pointed at it,
// so these tests exercise the real Bifrost request path and plugin hooks without API keys.
type fakeOpenAI struct {
	mu       sync.Mutex
	models   []string
	paths    []string
	fail429  int // answer this many chat calls with 429 first, like a free tier under burst
	rejected int
}

func (f *fakeOpenAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	if r.Method == http.MethodGet && (strings.HasSuffix(r.URL.Path, "/models") || strings.HasSuffix(r.URL.Path, "/api/tags")) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"fake-chat","object":"model"},{"id":"fake-extract","object":"model"},{"id":"fake-judge","object":"model"}],"models":[{"name":"fake-chat","model":"fake-chat"},{"name":"fake-extract","model":"fake-extract"},{"name":"fake-judge","model":"fake-judge"}]}`)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") && !strings.HasSuffix(r.URL.Path, "/api/chat") {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	if f.rejected < f.fail429 {
		f.rejected++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limit reached","type":"rate_limit_error"}}`)
		return
	}
	f.mu.Unlock()
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.models = append(f.models, req.Model)
	f.mu.Unlock()

	system := ""
	if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
		system, _ = req.Messages[0].Content.(string)
	}
	var content string
	switch {
	case strings.Contains(system, "You extract time-sensitive"):
		content = `{"claims":[{"text":"Acme Pro costs $10 per month","category":"pricing","entity":"Acme","attribute":"Pro price","claimed_value":"$10 per month","time_sensitivity":"high","search_query":"Acme Pro price","why_time_sensitive":"prices change"}]}`
	case strings.Contains(system, "You check whether a claim"):
		content = "```json\n" + `{"verdict":"outdated","current_value":"$14 per month","confidence":0.9,"explanation":"The pricing page lists $14 [1].","corrected_text":"Acme Pro costs $14 per month","as_of":"2026-10-01","sources":[{"index":1,"stance":"contradicts","official":true}]}` + "\n```"
	default:
		content = chatAnswer
	}
	resp := map[string]any{
		"id": "chatcmpl-test", "object": "chat.completion", "created": time.Now().Unix(), "model": req.Model,
		"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeOpenAI) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.models...)
}

type fakeSearch struct{}

func (fakeSearch) Search(_ context.Context, q search.Query) (*search.Response, error) {
	return &search.Response{Query: q.Text, Results: []search.Result{{
		Title: "Acme pricing", URL: "https://acme.example/pricing", PublishedDate: "2026-10-01",
		Content: "Pro: $14 per month.", Score: 0.9,
	}}}, nil
}

func newLiveTestServer(t *testing.T) (*httptest.Server, *fakeOpenAI) {
	t.Helper()
	ts, provider, _ := newLiveTestServerWith(t, "openai", "test-key", &fakeOpenAI{})
	return ts, provider
}

func newLiveTestServerWith(t *testing.T, providerName, key string, provider *fakeOpenAI) (*httptest.Server, *fakeOpenAI, *llm.Gateway) {
	t.Helper()
	providerSrv := httptest.NewServer(provider)
	t.Cleanup(providerSrv.Close)

	cfg := &config.Config{
		ProviderKeys:     map[string]string{providerName: key},
		ProviderBaseURLs: map[string]string{providerName: providerSrv.URL},
		ExtractModel:     config.ModelRef{Provider: providerName, Model: "fake-extract"},
		JudgeModel:       config.ModelRef{Provider: providerName, Model: "fake-judge"},
		ChatModel:        config.ModelRef{Provider: providerName, Model: "fake-chat"},
		MaxRetries:       3,
		CacheTTLMinutes:  60,
		TavilyKey:        "test",
		MaxClaims:        5,
		Concurrency:      2,
		ConnectorMode:    "annotate",
		VerifyTimeout:    30 * time.Second,
		WebDir:           t.TempDir(),
	}
	fresh := plugin.New(plugin.ModeAnnotate, cfg.VerifyTimeout)
	gw, err := llm.NewGateway(context.Background(), cfg, fresh)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Shutdown)
	engine := &verify.Engine{LLM: gw, Search: fakeSearch{}, MaxClaims: cfg.MaxClaims, Concurrency: cfg.Concurrency}
	fresh.SetEngine(engine)

	srv := &Server{Cfg: cfg, Engine: engine, Gateway: gw}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, provider, gw
}

func postChat(t *testing.T, url, mode string, body map[string]any) (*http.Response, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if mode != "" {
		req.Header.Set("X-Freshgate-Mode", mode)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res, out
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Freshgate struct {
		Mode   string         `json:"mode"`
		Report *verify.Report `json:"report"`
		Error  string         `json:"error"`
	} `json:"freshgate"`
}

func TestConnectorAnnotatesThroughBifrost(t *testing.T) {
	ts, provider := newLiveTestServer(t)
	res, body := postChat(t, ts.URL, "", map[string]any{
		"model":    "freshgate",
		"messages": []map[string]any{{"role": "user", "content": "How much is Acme Pro?"}},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		t.Fatal(err)
	}
	if cr.Freshgate.Error != "" {
		t.Fatalf("plugin error: %s", cr.Freshgate.Error)
	}
	content := cr.Choices[0].Message.Content
	if !strings.HasPrefix(content, chatAnswer) || !strings.Contains(content, "Freshness check:") {
		t.Fatalf("answer was not annotated:\n%s", content)
	}
	rep := cr.Freshgate.Report
	if rep == nil || len(rep.Claims) != 1 || rep.Claims[0].Result.Verdict != verify.VerdictOutdated {
		t.Fatalf("unexpected report: %s", body)
	}
	if res.Header.Get("X-Freshgate-Label") != "stale" {
		t.Fatalf("label header %q", res.Header.Get("X-Freshgate-Label"))
	}
	// The chat call and both pipeline calls all went through Bifrost to the provider.
	seen := strings.Join(provider.seen(), ",")
	for _, m := range []string{"fake-chat", "fake-extract", "fake-judge"} {
		if !strings.Contains(seen, m) {
			t.Errorf("provider never saw model %s (saw %s)", m, seen)
		}
	}
}

func TestConnectorCorrectMode(t *testing.T) {
	ts, _ := newLiveTestServer(t)
	_, body := postChat(t, ts.URL, "correct", map[string]any{
		"model":    "openai/fake-chat",
		"messages": []map[string]any{{"role": "user", "content": "How much is Acme Pro?"}},
	})
	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		t.Fatal(err)
	}
	content := cr.Choices[0].Message.Content
	if !strings.HasPrefix(content, "Acme Pro costs $14 per month.") || !strings.Contains(content, "~~Acme Pro costs $10 per month~~") {
		t.Fatalf("answer was not corrected:\n%s", content)
	}
}

func TestConnectorOffModePassesThrough(t *testing.T) {
	ts, provider := newLiveTestServer(t)
	_, body := postChat(t, ts.URL, "off", map[string]any{
		"model":    "fake-chat",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	var cr chatResponse
	_ = json.Unmarshal(body, &cr)
	if cr.Choices[0].Message.Content != chatAnswer || cr.Freshgate.Report != nil {
		t.Fatalf("off mode changed the answer: %s", body)
	}
	if n := len(provider.seen()); n != 1 {
		t.Fatalf("off mode should make exactly one provider call, made %d", n)
	}
}

func TestConnectorStream(t *testing.T) {
	ts, _ := newLiveTestServer(t)
	res, body := postChat(t, ts.URL, "", map[string]any{
		"model":    "freshgate",
		"stream":   true,
		"messages": []map[string]any{{"role": "user", "content": "How much is Acme Pro?"}},
	})
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}
	s := string(body)
	if !strings.Contains(s, `"chat.completion.chunk"`) || !strings.Contains(s, "Freshness check:") || !strings.HasSuffix(strings.TrimSpace(s), "data: [DONE]") {
		t.Fatalf("bad stream:\n%s", s)
	}
}

func TestVerifySSE(t *testing.T) {
	ts, _ := newLiveTestServer(t)
	b, _ := json.Marshal(map[string]string{"text": chatAnswer})
	res, err := http.Post(ts.URL+"/api/verify", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	events := map[string]int{}
	var report verify.Report
	var traces []verify.TraceEntry
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			events[event]++
			data := []byte(strings.TrimPrefix(line, "data: "))
			switch event {
			case verify.EvReport:
				_ = json.Unmarshal(data, &report)
			case verify.EvTrace:
				var tr verify.TraceEntry
				_ = json.Unmarshal(data, &tr)
				traces = append(traces, tr)
			}
		}
	}
	if events[verify.EvError] > 0 {
		t.Fatalf("got error event: %+v", events)
	}
	if events[verify.EvReport] != 1 || events[verify.EvClaims] != 1 || events[verify.EvClaimResult] != 1 {
		t.Fatalf("events: %+v", events)
	}
	if report.Summary.Total != 1 || report.Claims[0].Result.CorrectedText != "Acme Pro costs $14 per month" {
		t.Fatalf("report: %+v", report.Summary)
	}
	var llmCalls int
	for _, tr := range traces {
		if tr.Kind == "llm" {
			llmCalls++
			if tr.Provider != "openai" || tr.Model == "" {
				t.Errorf("trace lacks Bifrost routing info: %+v", tr)
			}
		}
	}
	if llmCalls != 2 {
		t.Fatalf("expected 2 LLM trace entries, got %d", llmCalls)
	}
}

func TestDemoModeConnectorUnavailable(t *testing.T) {
	srv := &Server{Cfg: &config.Config{Demo: true, WebDir: t.TempDir()}}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	res, _ := postChat(t, ts.URL, "", map[string]any{"messages": []map[string]any{{"role": "user", "content": "hi"}}})
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestRateLimitedProviderIsRetried(t *testing.T) {
	ts, provider, _ := newLiveTestServerWith(t, "openai", "test-key", &fakeOpenAI{fail429: 2})
	b, _ := json.Marshal(map[string]string{"text": chatAnswer})
	res, err := http.Post(ts.URL+"/api/verify", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(body), "event: error") || !strings.Contains(string(body), "event: report") {
		t.Fatalf("run failed despite retries:\n%s", body)
	}
	if strings.Contains(string(body), "rate limit") {
		t.Fatalf("a 429 leaked into the result instead of being retried:\n%s", body)
	}
	if provider.rejected != 2 {
		t.Fatalf("provider rejected %d calls, want 2", provider.rejected)
	}
}

func TestCheckModels(t *testing.T) {
	_, _, gw := newLiveTestServerWith(t, "openai", "test-key", &fakeOpenAI{})
	if w := gw.CheckModels(context.Background()); len(w) != 0 {
		t.Fatalf("unexpected warnings for valid models: %v", w)
	}
	ids, err := gw.ListModels(context.Background(), "openai")
	if err != nil || len(ids) != 3 {
		t.Fatalf("ListModels = %v, %v", ids, err)
	}
}

func TestCheckModelsFlagsUnknownModel(t *testing.T) {
	provider := &fakeOpenAI{}
	providerSrv := httptest.NewServer(provider)
	defer providerSrv.Close()
	cfg := &config.Config{
		ProviderKeys:     map[string]string{"openai": "k"},
		ProviderBaseURLs: map[string]string{"openai": providerSrv.URL},
		ExtractModel:     config.ModelRef{Provider: "openai", Model: "fake-extract"},
		JudgeModel:       config.ModelRef{Provider: "openai", Model: "retired-model"},
	}
	gw, err := llm.NewGateway(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Shutdown()
	w := gw.CheckModels(context.Background())
	if len(w) != 1 || !strings.Contains(w[0], "openai/retired-model") || !strings.Contains(w[0], "fake-judge") {
		t.Fatalf("warnings = %v", w)
	}
}

func TestKeylessOllamaThroughBifrost(t *testing.T) {
	ts, provider, _ := newLiveTestServerWith(t, "ollama", "", &fakeOpenAI{})
	_, body := postChat(t, ts.URL, "", map[string]any{
		"model":    "freshgate",
		"messages": []map[string]any{{"role": "user", "content": "How much is Acme Pro?"}},
	})
	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil || len(cr.Choices) == 0 {
		t.Fatalf("bad response (provider saw %v): %s", provider.paths, body)
	}
	t.Logf("ollama endpoints hit: %v", provider.paths)
	if cr.Freshgate.Report == nil || !strings.Contains(cr.Choices[0].Message.Content, "Freshness check:") {
		t.Fatalf("ollama path did not verify (provider saw %v): %s", provider.paths, body)
	}
}

func runVerify(t *testing.T, url string) verify.Report {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"text": chatAnswer})
	res, err := http.Post(url+"/api/verify", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var rep verify.Report
	for _, e := range readSSE(t, res) {
		if e.Event == verify.EvReport {
			_ = json.Unmarshal(e.Data, &rep)
		}
		if e.Event == verify.EvError {
			t.Fatalf("error: %s", e.Data)
		}
	}
	return rep
}

func TestLedgerPluginCachesRepeatedCalls(t *testing.T) {
	ts, provider := newLiveTestServer(t)
	first := runVerify(t, ts.URL)
	calls := len(provider.seen())
	second := runVerify(t, ts.URL)
	if first.Summary.Usage.CachedLLMCalls != 0 {
		t.Fatalf("first run should not hit the cache: %+v", first.Summary.Usage)
	}
	if got := second.Summary.Usage.CachedLLMCalls; got != 2 {
		t.Fatalf("second run: %d cached calls, want 2 (extract + judge): %+v", got, second.Summary.Usage)
	}
	if len(provider.seen()) != calls {
		t.Fatalf("cached run still reached the provider (%d → %d calls)", calls, len(provider.seen()))
	}
	if second.Claims[0].Result.Verdict != verify.VerdictOutdated {
		t.Fatalf("cached verdict: %+v", second.Claims[0].Result)
	}
}

func TestLedgerPluginEnforcesTokenBudget(t *testing.T) {
	ts, _, gw := newLiveTestServerWith(t, "openai", "k", &fakeOpenAI{})
	_ = ts
	// The fake provider charges 30 tokens per call: the extract call spends the whole budget,
	// so the judge call must be stopped inside Bifrost.
	engine := &verify.Engine{LLM: gw, Search: fakeSearch{}, TokenBudget: 25, MaxClaims: 5}
	rep, err := engine.Run(context.Background(), "Acme Pro costs $10 per month and more words here.", nil)
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Claims[0].Result
	if r.Verdict != verify.VerdictUnverifiable || !strings.Contains(r.Error, "token budget") {
		t.Fatalf("judge call should be blocked by the budget: %+v", r)
	}
}
