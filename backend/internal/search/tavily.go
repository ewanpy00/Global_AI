// Package search retrieves live web evidence. Tavily is the production backend.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
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
	Credits        float64  `json:"credits"` // Tavily API credits this call used (0 when cached)
}

// Query is one search. Only Text is required; the rest tunes Tavily per claim category.
type Query struct {
	Text           string
	Topic          string   // general (default) | news
	TimeRange      string   // "" | day | week | month | year
	IncludeDomains []string // restrict to these domains (e.g. the entity's official site)
	Depth          string   // "" uses the client default; basic | advanced
	MaxResults     int      // 0 uses the client default
}

func (q Query) key() string {
	return strings.ToLower(strings.TrimSpace(q.Text)) + "|" + q.Topic + "|" + q.TimeRange + "|" +
		strings.Join(q.IncludeDomains, ",") + "|" + q.Depth + fmt.Sprintf("|%d", q.MaxResults)
}

// Page is the full text of one web page (Tavily Extract / Crawl).
type Page struct {
	URL     string `json:"url"`
	Content string `json:"content"`
}

// PagesResponse is what Extract and Crawl return.
type PagesResponse struct {
	Pages          []Page  `json:"pages"`
	Failed         int     `json:"failed"`
	ResponseTimeMs int64   `json:"response_time_ms"`
	Cached         bool    `json:"cached"`
	Credits        float64 `json:"credits"`
}

// Searcher is the interface the pipeline depends on.
type Searcher interface {
	Search(ctx context.Context, q Query) (*Response, error)
}

// Extractor fetches the full text of pages (used when a search snippet is too thin to judge).
type Extractor interface {
	Extract(ctx context.Context, urls []string) (*PagesResponse, error)
}

// Crawler walks a site from a start URL (used by the site audit).
type Crawler interface {
	Crawl(ctx context.Context, url string, limit int) (*PagesResponse, error)
}

// Tavily calls the Tavily Search, Extract and Crawl APIs.
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
	val any
	at  time.Time
}

const cacheTTL = time.Hour

func NewTavily(apiKey, depth string, maxResults int) *Tavily {
	return &Tavily{
		APIKey:     apiKey,
		Depth:      depth,
		MaxResults: maxResults,
		BaseURL:    "https://api.tavily.com",
		HTTP:       &http.Client{Timeout: 90 * time.Second},
		cache:      map[string]cacheEntry{},
	}
}

type tavilyRequest struct {
	Query                string   `json:"query"`
	SearchDepth          string   `json:"search_depth"`
	Topic                string   `json:"topic"`
	MaxResults           int      `json:"max_results"`
	IncludeAnswer        string   `json:"include_answer"`
	IncludePublishedDate bool     `json:"include_published_date"`
	TimeRange            string   `json:"time_range,omitempty"`
	IncludeDomains       []string `json:"include_domains,omitempty"`
	ChunksPerSource      int      `json:"chunks_per_source,omitempty"`
}

type tavilyResponse struct {
	Query        string   `json:"query"`
	Answer       string   `json:"answer"`
	Results      []Result `json:"results"`
	ResponseTime float64  `json:"response_time"`
}

func (t *Tavily) cached(key string) (any, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.cache[key]; ok && time.Since(e.at) < cacheTTL {
		return e.val, true
	}
	return nil, false
}

func (t *Tavily) store(key string, v any) {
	t.mu.Lock()
	t.cache[key] = cacheEntry{val: v, at: time.Now()}
	t.mu.Unlock()
}

// Search runs one query. Identical queries within an hour are served from memory:
// repeated demo runs stay fast and do not burn credits.
func (t *Tavily) Search(ctx context.Context, q Query) (*Response, error) {
	key := "s|" + q.key()
	if v, ok := t.cached(key); ok {
		cp := *v.(*Response)
		cp.Cached, cp.Credits = true, 0
		return &cp, nil
	}
	depth := q.Depth
	if depth == "" {
		depth = t.Depth
	}
	maxResults := q.MaxResults
	if maxResults <= 0 {
		maxResults = t.MaxResults
	}
	topic := q.Topic
	if topic == "" {
		topic = "general"
	}
	req := tavilyRequest{
		Query:                q.Text,
		SearchDepth:          depth,
		Topic:                topic,
		MaxResults:           maxResults,
		IncludeAnswer:        "basic",
		IncludePublishedDate: true,
		TimeRange:            q.TimeRange,
		IncludeDomains:       q.IncludeDomains,
	}
	if depth == "advanced" {
		req.ChunksPerSource = 3 // the most relevant passages per page, not just the opening
	}
	started := time.Now()
	var tr tavilyResponse
	if err := t.post(ctx, "/search", req, &tr); err != nil {
		return nil, err
	}
	out := &Response{
		Query:          q.Text,
		Answer:         tr.Answer,
		Results:        tr.Results,
		ResponseTimeMs: time.Since(started).Milliseconds(),
		Credits:        SearchCredits(depth),
	}
	t.store(key, out)
	return out, nil
}

// SearchCredits is what one search costs on Tavily's pricing: basic 1, advanced 2.
func SearchCredits(depth string) float64 {
	if depth == "advanced" {
		return 2
	}
	return 1
}

// extractCredits: basic extraction costs 1 credit per 5 successful URLs.
func extractCredits(n int) float64 { return math.Ceil(float64(n) / 5) }

type extractResponse struct {
	Results []struct {
		URL        string `json:"url"`
		RawContent string `json:"raw_content"`
	} `json:"results"`
	FailedResults []json.RawMessage `json:"failed_results"`
}

// Extract fetches the readable text of up to 20 pages.
func (t *Tavily) Extract(ctx context.Context, urls []string) (*PagesResponse, error) {
	if len(urls) == 0 {
		return &PagesResponse{}, nil
	}
	key := "e|" + strings.Join(urls, "|")
	if v, ok := t.cached(key); ok {
		cp := *v.(*PagesResponse)
		cp.Cached, cp.Credits = true, 0
		return &cp, nil
	}
	started := time.Now()
	var er extractResponse
	err := t.post(ctx, "/extract", map[string]any{"urls": urls, "extract_depth": "basic", "format": "text"}, &er)
	if err != nil {
		return nil, err
	}
	out := &PagesResponse{Failed: len(er.FailedResults), ResponseTimeMs: time.Since(started).Milliseconds()}
	for _, r := range er.Results {
		out.Pages = append(out.Pages, Page{URL: r.URL, Content: r.RawContent})
	}
	out.Credits = extractCredits(len(out.Pages))
	t.store(key, out)
	return out, nil
}

type crawlResponse struct {
	BaseURL string `json:"base_url"`
	Results []struct {
		URL        string `json:"url"`
		RawContent string `json:"raw_content"`
	} `json:"results"`
}

// Crawl walks a site from url and returns the text of up to limit pages on the same domain.
func (t *Tavily) Crawl(ctx context.Context, url string, limit int) (*PagesResponse, error) {
	if limit <= 0 {
		limit = 10
	}
	key := fmt.Sprintf("c|%s|%d", url, limit)
	if v, ok := t.cached(key); ok {
		cp := *v.(*PagesResponse)
		cp.Cached, cp.Credits = true, 0
		return &cp, nil
	}
	started := time.Now()
	var cr crawlResponse
	err := t.post(ctx, "/crawl", map[string]any{
		"url": url, "max_depth": 2, "max_breadth": 20, "limit": limit,
		"extract_depth": "basic", "format": "text", "allow_external": false,
	}, &cr)
	if err != nil {
		return nil, err
	}
	out := &PagesResponse{ResponseTimeMs: time.Since(started).Milliseconds()}
	for _, r := range cr.Results {
		out.Pages = append(out.Pages, Page{URL: r.URL, Content: r.RawContent})
	}
	// Crawl = mapping (1 credit per 10 pages) + extraction (1 credit per 5 pages).
	out.Credits = math.Ceil(float64(len(out.Pages))/10) + extractCredits(len(out.Pages))
	t.store(key, out)
	return out, nil
}

func (t *Tavily) post(ctx context.Context, path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.APIKey)
	res, err := t.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("tavily%s: %w", path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("tavily%s: read body: %w", path, err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("tavily%s: status %d: %s", path, res.StatusCode, truncate(string(raw), 300))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("tavily%s: decode: %w", path, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
