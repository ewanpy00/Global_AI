// Package search retrieves live web evidence. Tavily is the production backend.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Result is one web source.
type Result struct {
	Title         string  `json:"title"`
	URL           string  `json:"url"`
	Content       string  `json:"content"`
	Score         float64 `json:"score"`
	PublishedDate string  `json:"published_date,omitempty"` // Tavily's best estimate; may be empty
}

// Response is what one search returns.
type Response struct {
	Query          string   `json:"query"`
	Answer         string   `json:"answer,omitempty"`
	Results        []Result `json:"results"`
	ResponseTimeMs int64    `json:"response_time_ms"`
	Cached         bool     `json:"cached"`
}

// Searcher is the interface the pipeline depends on.
type Searcher interface {
	Search(ctx context.Context, query string) (*Response, error)
}

// Tavily calls the Tavily Search API.
type Tavily struct {
	APIKey     string
	Depth      string // basic | advanced
	MaxResults int
	BaseURL    string
	HTTP       *http.Client

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	resp *Response
	at   time.Time
}

const cacheTTL = time.Hour

func NewTavily(apiKey, depth string, maxResults int) *Tavily {
	return &Tavily{
		APIKey:     apiKey,
		Depth:      depth,
		MaxResults: maxResults,
		BaseURL:    "https://api.tavily.com",
		HTTP:       &http.Client{Timeout: 45 * time.Second},
		cache:      map[string]cacheEntry{},
	}
}

type tavilyRequest struct {
	Query                string `json:"query"`
	SearchDepth          string `json:"search_depth"`
	Topic                string `json:"topic"`
	MaxResults           int    `json:"max_results"`
	IncludeAnswer        string `json:"include_answer"`
	IncludePublishedDate bool   `json:"include_published_date"`
}

type tavilyResponse struct {
	Query        string   `json:"query"`
	Answer       string   `json:"answer"`
	Results      []Result `json:"results"`
	ResponseTime float64  `json:"response_time"`
}

// Search runs one query. Identical queries within an hour are served from memory:
// repeated demo runs stay fast and do not burn credits.
func (t *Tavily) Search(ctx context.Context, query string) (*Response, error) {
	key := strings.ToLower(strings.TrimSpace(query))
	t.mu.Lock()
	if e, ok := t.cache[key]; ok && time.Since(e.at) < cacheTTL {
		t.mu.Unlock()
		cp := *e.resp
		cp.Cached = true
		return &cp, nil
	}
	t.mu.Unlock()

	body, _ := json.Marshal(tavilyRequest{
		Query:                query,
		SearchDepth:          t.Depth,
		Topic:                "general",
		MaxResults:           t.MaxResults,
		IncludeAnswer:        "basic",
		IncludePublishedDate: true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.BaseURL+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.APIKey)

	started := time.Now()
	res, err := t.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("tavily: read body: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tavily: status %d: %s", res.StatusCode, truncate(string(raw), 300))
	}
	var tr tavilyResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("tavily: decode: %w", err)
	}
	out := &Response{
		Query:          query,
		Answer:         tr.Answer,
		Results:        tr.Results,
		ResponseTimeMs: time.Since(started).Milliseconds(),
	}
	t.mu.Lock()
	t.cache[key] = cacheEntry{resp: out, at: time.Now()}
	t.mu.Unlock()
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
