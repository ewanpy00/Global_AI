package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestTavilyRequestsAndCache(t *testing.T) {
	var calls atomic.Int32
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &last)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			_, _ = io.WriteString(w, `{"answer":"a","results":[{"title":"t","url":"https://x.example/p","content":"c","score":0.9}]}`)
		case "/extract":
			_, _ = io.WriteString(w, `{"results":[{"url":"https://x.example/p","raw_content":"full page"}],"failed_results":[]}`)
		case "/crawl":
			_, _ = io.WriteString(w, `{"base_url":"https://x.example","results":[{"url":"https://x.example/a","raw_content":"A"},{"url":"https://x.example/b","raw_content":"B"}]}`)
		}
	}))
	defer srv.Close()
	tv := NewTavily("k", "advanced", 5)
	tv.BaseURL = srv.URL
	ctx := context.Background()

	res, err := tv.Search(ctx, Query{Text: "q", IncludeDomains: []string{"x.example"}, Depth: "basic", MaxResults: 3})
	if err != nil || len(res.Results) != 1 || res.Credits != 1 {
		t.Fatalf("search: %+v %v", res, err)
	}
	if last["search_depth"] != "basic" || last["max_results"].(float64) != 3 || last["include_domains"].([]any)[0] != "x.example" {
		t.Fatalf("request: %v", last)
	}
	res, _ = tv.Search(ctx, Query{Text: "Q ", IncludeDomains: []string{"x.example"}, Depth: "basic", MaxResults: 3})
	if !res.Cached || res.Credits != 0 || calls.Load() != 1 {
		t.Fatalf("identical query should be cached: %+v (calls %d)", res, calls.Load())
	}
	res, _ = tv.Search(ctx, Query{Text: "q", Topic: "news", TimeRange: "year"})
	if res.Cached || last["topic"] != "news" || last["time_range"] != "year" || last["chunks_per_source"].(float64) != 3 || res.Credits != 2 {
		t.Fatalf("news search: %+v %v", res, last)
	}
	pages, err := tv.Extract(ctx, []string{"https://x.example/p"})
	if err != nil || len(pages.Pages) != 1 || pages.Pages[0].Content != "full page" || pages.Credits != 1 {
		t.Fatalf("extract: %+v %v", pages, err)
	}
	crawl, err := tv.Crawl(ctx, "https://x.example", 5)
	if err != nil || len(crawl.Pages) != 2 || last["limit"].(float64) != 5 || crawl.Credits != 2 {
		t.Fatalf("crawl: %+v %v %v", crawl, err, last)
	}
}
