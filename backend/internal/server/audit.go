package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

// Site audit: Tavily Crawl walks a website, every page goes through the pipeline, and the pages
// are ranked by how stale their facts are. "Which pages of our help centre lie to customers?"

const (
	defaultAuditPages = 6
	maxAuditPages     = 15
	auditPageChars    = 12_000
	auditClaimsPage   = 6
	auditParallel     = 2
)

type auditRequest struct {
	URL   string `json:"url"`
	Limit int    `json:"limit"`
}

// AuditPage is one crawled page and what was found on it.
type AuditPage struct {
	ID      string          `json:"id"`
	URL     string          `json:"url"`
	Title   string          `json:"title"`
	Chars   int             `json:"chars"`
	Score   *int            `json:"score,omitempty"`
	Label   string          `json:"label,omitempty"`
	Claims  int             `json:"claims"`
	Stale   int             `json:"stale"`
	Skipped string          `json:"skipped,omitempty"`
	Error   string          `json:"error,omitempty"`
	Items   []compactClaim  `json:"items,omitempty"`
	Summary *verify.Summary `json:"summary,omitempty"`
}

// AuditResult is the whole site.
type AuditResult struct {
	URL        string      `json:"url"`
	Score      int         `json:"score"`
	Label      string      `json:"label"`
	Pages      []AuditPage `json:"pages"` // stalest first
	Claims     int         `json:"claims"`
	Stale      int         `json:"stale"`
	Credits    float64     `json:"credits"`
	DurationMs int64       `json:"duration_ms"`
}

type compactClaim struct {
	ID            string `json:"id"`
	Text          string `json:"text"`
	Category      string `json:"category"`
	Verdict       string `json:"verdict"`
	Label         string `json:"label"`
	Score         int    `json:"score"`
	CurrentValue  string `json:"current_value,omitempty"`
	CorrectedText string `json:"corrected_text,omitempty"`
	Source        string `json:"source,omitempty"`
	Explanation   string `json:"explanation,omitempty"`
}

func compact(r *verify.Report) []compactClaim {
	var out []compactClaim
	for _, c := range r.Claims {
		if c.Result == nil {
			continue
		}
		cc := compactClaim{
			ID: c.ID, Text: c.Text, Category: string(c.Category), Verdict: string(c.Result.Verdict),
			Label: string(c.Result.Label), Score: c.Result.Score, CurrentValue: c.Result.CurrentValue,
			CorrectedText: c.Result.CorrectedText, Explanation: c.Result.Explanation,
		}
		for _, s := range c.Result.Sources {
			if s.Stance != verify.StanceNeutral {
				cc.Source = s.URL
				break
			}
		}
		out = append(out, cc)
	}
	return out
}

func normalizeSiteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.Contains(u.Hostname(), ".") {
		return "", fmt.Errorf("%q is not a web address", raw)
	}
	return u.String(), nil
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if s.Crawler == nil {
		writeError(w, http.StatusServiceUnavailable, "site audit needs Tavily: set TAVILY_API_KEY")
		return
	}
	var req auditRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	site, err := normalizeSiteURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sse, err := newSSE(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*s.Cfg.VerifyTimeout)
	defer cancel()
	var mu sync.Mutex
	emit := func(event string, data any) {
		mu.Lock()
		defer mu.Unlock()
		sse.send(event, data)
	}
	res, err := s.RunAudit(ctx, site, req.Limit, emit)
	if err != nil {
		emit("error", map[string]string{"message": err.Error()})
		return
	}
	emit("audit_done", res)
}

// RunAudit crawls site and checks each page. emit (optional) receives audit_pages,
// page_event and audit_page events while it runs.
func (s *Server) RunAudit(ctx context.Context, site string, limit int, emit func(string, any)) (*AuditResult, error) {
	if s.Crawler == nil {
		return nil, errors.New("site audit needs Tavily: set TAVILY_API_KEY")
	}
	if emit == nil {
		emit = func(string, any) {}
	}
	if limit <= 0 {
		limit = defaultAuditPages
	}
	if limit > maxAuditPages {
		limit = maxAuditPages
	}
	started := time.Now()
	emit("stage", map[string]string{"stage": "crawling"})
	crawl, err := s.Crawler.Crawl(ctx, site, limit)
	if err != nil {
		return nil, fmt.Errorf("crawl: %w", err)
	}
	res := &AuditResult{URL: site, Credits: crawl.Credits}
	pages := make([]AuditPage, len(crawl.Pages))
	texts := make([]string, len(crawl.Pages))
	for i, p := range crawl.Pages {
		text := strings.TrimSpace(p.Content)
		if utf8.RuneCountInString(text) > auditPageChars {
			text = string([]rune(text)[:auditPageChars])
		}
		texts[i] = text
		pages[i] = AuditPage{ID: fmt.Sprintf("p%d", i+1), URL: p.URL, Title: pageTitle(p.URL, text), Chars: len([]rune(text))}
	}
	emit("audit_pages", map[string]any{"pages": pages, "credits": crawl.Credits, "crawl_ms": crawl.ResponseTimeMs})
	emit("stage", map[string]string{"stage": "checking"})

	sem := make(chan struct{}, auditParallel)
	var wg sync.WaitGroup
	for i := range pages {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p := &pages[i]
			if len([]rune(texts[i])) < 80 {
				p.Skipped = "too little text"
				emit("audit_page", p)
				return
			}
			rep, err := s.Engine.RunWith(ctx, texts[i], verify.RunOptions{MaxClaims: auditClaimsPage}, func(ev verify.Event) {
				switch ev.Type {
				case verify.EvClaims, verify.EvClaimStatus, verify.EvClaimResult:
					emit("page_event", map[string]any{"page_id": p.ID, "event": ev})
				}
			})
			if err != nil {
				p.Error = err.Error()
				emit("audit_page", p)
				return
			}
			sum := rep.Summary
			p.Summary = &sum
			p.Claims = sum.Total
			p.Stale = sum.ByVerdict[verify.VerdictOutdated] + sum.ByVerdict[verify.VerdictIncorrect]
			if sum.Total > 0 {
				sc := sum.Score
				p.Score, p.Label = &sc, string(sum.Label)
			}
			p.Items = compact(rep)
			emit("audit_page", p)
		}(i)
	}
	wg.Wait()

	var weighted, weight float64
	for _, p := range pages {
		res.Claims += p.Claims
		res.Stale += p.Stale
		if p.Summary != nil {
			res.Credits += p.Summary.Usage.TavilyCredits
		}
		if p.Score != nil {
			weighted += float64(*p.Score) * float64(p.Claims)
			weight += float64(p.Claims)
		}
	}
	res.Score = 100
	if weight > 0 {
		res.Score = int(math.Round(weighted / weight))
	}
	switch {
	case res.Score >= 75 && res.Stale == 0:
		res.Label = string(verify.LabelFresh)
	case res.Score < 45:
		res.Label = string(verify.LabelStale)
	default:
		res.Label = string(verify.LabelReview)
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].Stale != pages[j].Stale {
			return pages[i].Stale > pages[j].Stale
		}
		return scoreOr(pages[i].Score, 101) < scoreOr(pages[j].Score, 101)
	})
	for i := range pages {
		pages[i].Summary = nil // the items carry what the UI needs; keep the final event small
	}
	res.Pages = pages
	res.Credits = math.Round(res.Credits*10) / 10
	res.DurationMs = time.Since(started).Milliseconds()
	return res, nil
}

func scoreOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// pageTitle uses the page's first short line, or its URL path.
func pageTitle(raw, text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.Trim(line, "#*= "))
		if n := utf8.RuneCountInString(line); n >= 3 && n <= 90 {
			return line
		}
		if line != "" {
			break
		}
	}
	if u, err := url.Parse(raw); err == nil && u.Path != "" && u.Path != "/" {
		return u.Path
	}
	return raw
}
