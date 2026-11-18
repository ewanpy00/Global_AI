package verify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ewanpy00/Global_AI/backend/internal/llm"
	"github.com/ewanpy00/Global_AI/backend/internal/search"
)

// Engine runs the verification pipeline. LLM calls go through Bifrost, searches through Tavily.
type Engine struct {
	LLM         llm.Client
	Search      search.Searcher
	MaxClaims   int
	Concurrency int
	Now         func() time.Time
}

// MaxAnswerChars bounds the input so one request cannot run up an unbounded bill.
const MaxAnswerChars = 60_000

// Run verifies an answer. emit (optional) receives progress events as they happen;
// it is called from several goroutines but never concurrently.
func (e *Engine) Run(ctx context.Context, text string, emit func(Event)) (*Report, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("answer text is empty")
	}
	if utf8.RuneCountInString(text) > MaxAnswerChars {
		return nil, fmt.Errorf("answer is longer than %d characters", MaxAnswerChars)
	}
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	started := now()
	r := &run{engine: e, started: started, now: now, text: text}
	var mu sync.Mutex
	r.emit = func(ev Event) {
		if emit == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		emit(ev)
	}

	report := &Report{ID: newID(), Text: text, CheckedAt: started}

	r.emit(Event{EvStage, map[string]string{"stage": "extracting"}})
	claims, err := r.extract(ctx)
	if err != nil {
		return nil, fmt.Errorf("extract claims: %w", err)
	}
	r.emit(Event{EvClaims, map[string]any{"claims": claims}})

	r.emit(Event{EvStage, map[string]string{"stage": "verifying"}})
	report.Claims = make([]ClaimReport, len(claims))
	conc := e.Concurrency
	if conc <= 0 {
		conc = 4
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, c := range claims {
		report.Claims[i].Claim = c
		wg.Add(1)
		go func(i int, c Claim) {
			defer wg.Done()
			var res *Verification
			select {
			case sem <- struct{}{}:
				res = r.verifyClaim(ctx, c)
				<-sem
			case <-ctx.Done():
				res = failed(c, ctx.Err(), now())
			}
			report.Claims[i].Result = res
			r.emit(Event{EvClaimResult, map[string]any{"claim_id": c.ID, "result": res}})
		}(i, c)
	}
	wg.Wait()

	report.Summary = Summarize(report.Claims)
	report.Summary.DurationMs = now().Sub(started).Milliseconds()
	report.Trace = r.trace
	r.emit(Event{EvSummary, report.Summary})
	r.emit(Event{EvStage, map[string]string{"stage": "done"}})
	return report, nil
}

type run struct {
	engine  *Engine
	started time.Time
	now     func() time.Time
	text    string
	emit    func(Event)

	traceMu sync.Mutex
	trace   []TraceEntry
}

func (r *run) record(t TraceEntry) {
	t.AtMs = r.now().Sub(r.started).Milliseconds()
	r.traceMu.Lock()
	r.trace = append(r.trace, t)
	r.traceMu.Unlock()
	r.emit(Event{EvTrace, t})
}

type extractedClaim struct {
	Text         string `json:"text"`
	Category     string `json:"category"`
	Entity       string `json:"entity"`
	Attribute    string `json:"attribute"`
	ClaimedValue string `json:"claimed_value"`
	Sensitivity  string `json:"time_sensitivity"`
	SearchQuery  string `json:"search_query"`
	Why          string `json:"why_time_sensitive"`
}

func (r *run) extract(ctx context.Context) ([]Claim, error) {
	maxClaims := r.engine.MaxClaims
	if maxClaims <= 0 {
		maxClaims = 12
	}
	resp, err := r.engine.LLM.Complete(ctx, llm.Request{
		Purpose:   llm.PurposeExtract,
		System:    extractSystemPrompt(r.now(), maxClaims),
		Messages:  []llm.Message{{Role: "user", Content: extractUserPrompt(r.text)}},
		MaxTokens: 4096,
	})
	r.record(llmTrace("extract", "", resp.Meta, err))
	if err != nil {
		return nil, err
	}
	var out struct {
		Claims []extractedClaim `json:"claims"`
	}
	if err := llm.DecodeJSON(resp.Text, &out); err != nil {
		return nil, fmt.Errorf("decode extractor reply: %w", err)
	}

	var claims []Claim
	seen := map[string]bool{}
	cursor := 0
	for _, x := range out.Claims {
		key := strings.ToLower(strings.TrimSpace(x.Text))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		c := Claim{
			Text:         strings.TrimSpace(x.Text),
			Category:     normalizeCategory(x.Category),
			Entity:       strings.TrimSpace(x.Entity),
			Attribute:    strings.TrimSpace(x.Attribute),
			ClaimedValue: strings.TrimSpace(x.ClaimedValue),
			Sensitivity:  normalizeSensitivity(x.Sensitivity),
			SearchQuery:  strings.TrimSpace(x.SearchQuery),
			Why:          strings.TrimSpace(x.Why),
		}
		if c.SearchQuery == "" {
			c.SearchQuery = strings.TrimSpace(c.Entity + " " + c.Attribute)
			if c.SearchQuery == "" {
				c.SearchQuery = c.Text
			}
		}
		if s, e, ok := locate(r.text, c.Text, cursor); ok {
			c.byteStart, c.byteEnd, c.Located = s, e, true
			c.Text = r.text[s:e]
			c.Start, c.End = utf16Offset(r.text, s), utf16Offset(r.text, e)
			cursor = e
		}
		claims = append(claims, c)
		if len(claims) == maxClaims {
			break
		}
	}
	// Keep reading order so highlights and cards line up; unlocated claims go last.
	sort.SliceStable(claims, func(i, j int) bool {
		if claims[i].Located != claims[j].Located {
			return claims[i].Located
		}
		return claims[i].byteStart < claims[j].byteStart
	})
	claims = dropOverlaps(claims)
	for i := range claims {
		claims[i].ID = fmt.Sprintf("c%d", i+1)
	}
	return claims, nil
}

// dropOverlaps removes claims whose span overlaps an earlier, already accepted span,
// so the answer can be highlighted without nested marks.
func dropOverlaps(claims []Claim) []Claim {
	out := claims[:0]
	lastEnd := -1
	for _, c := range claims {
		if c.Located {
			if c.byteStart < lastEnd {
				continue
			}
			lastEnd = c.byteEnd
		}
		out = append(out, c)
	}
	return out
}

type judgement struct {
	Verdict       string  `json:"verdict"`
	CurrentValue  string  `json:"current_value"`
	Confidence    float64 `json:"confidence"`
	Explanation   string  `json:"explanation"`
	CorrectedText string  `json:"corrected_text"`
	AsOf          string  `json:"as_of"`
	Sources       []struct {
		Index    int    `json:"index"`
		Stance   string `json:"stance"`
		Official bool   `json:"official"`
	} `json:"sources"`
}

func (r *run) verifyClaim(ctx context.Context, c Claim) *Verification {
	r.emit(Event{EvClaimStatus, map[string]string{"claim_id": c.ID, "status": "searching"}})
	started := r.now()
	res, err := r.engine.Search.Search(ctx, c.SearchQuery)
	st := TraceEntry{Kind: "search", Step: "search", ClaimID: c.ID, Query: c.SearchQuery, LatencyMs: r.now().Sub(started).Milliseconds()}
	if err != nil {
		st.Error = err.Error()
		r.record(st)
		return failed(c, fmt.Errorf("search: %w", err), r.now())
	}
	st.Results, st.Cached = len(res.Results), res.Cached
	if !res.Cached && res.ResponseTimeMs > 0 {
		st.LatencyMs = res.ResponseTimeMs
	}
	r.record(st)

	v := &Verification{SearchQuery: c.SearchQuery, SearchSummary: res.Answer}
	for i, sr := range res.Results {
		v.Sources = append(v.Sources, Source{
			Index:         i + 1,
			Title:         sr.Title,
			URL:           sr.URL,
			Domain:        domainOf(sr.URL),
			Snippet:       truncateRunes(sr.Content, 360),
			PublishedDate: sr.PublishedDate,
			Relevance:     sr.Score,
			Stance:        StanceNeutral,
		})
	}

	r.emit(Event{EvClaimStatus, map[string]string{"claim_id": c.ID, "status": "judging"}})
	resp, err := r.engine.LLM.Complete(ctx, llm.Request{
		Purpose:   llm.PurposeJudge,
		System:    judgeSystemPrompt(r.now()),
		Messages:  []llm.Message{{Role: "user", Content: judgeUserPrompt(c, res)}},
		MaxTokens: 2048,
	})
	r.record(llmTrace("judge", c.ID, resp.Meta, err))
	if err != nil {
		return failedWith(c, v, fmt.Errorf("judge: %w", err), r.now())
	}
	var j judgement
	if err := llm.DecodeJSON(resp.Text, &j); err != nil {
		return failedWith(c, v, fmt.Errorf("decode judge reply: %w", err), r.now())
	}

	v.Verdict = normalizeVerdict(j.Verdict)
	v.CurrentValue = strings.TrimSpace(j.CurrentValue)
	v.Confidence = clamp01(j.Confidence)
	v.Explanation = strings.TrimSpace(j.Explanation)
	v.AsOf = strings.TrimSpace(j.AsOf)
	if v.Verdict == VerdictOutdated || v.Verdict == VerdictIncorrect {
		v.CorrectedText = strings.TrimSpace(j.CorrectedText)
	}
	for _, js := range j.Sources {
		if js.Index < 1 || js.Index > len(v.Sources) {
			continue
		}
		s := &v.Sources[js.Index-1]
		switch Stance(js.Stance) {
		case StanceSupports, StanceContradicts:
			s.Stance = Stance(js.Stance)
		}
		s.Official = js.Official
	}
	v.Score, v.Breakdown = Score(c.Category, v, r.now())
	v.Label = labelFor(v.Score)
	return v
}

func failed(c Claim, err error, now time.Time) *Verification {
	return failedWith(c, &Verification{SearchQuery: c.SearchQuery}, err, now)
}

// failedWith turns an infrastructure error into an "unverifiable" result instead of failing
// the whole run: one flaky search should not hide the other claims.
func failedWith(c Claim, v *Verification, err error, now time.Time) *Verification {
	v.Verdict = VerdictUnverifiable
	v.Error = err.Error()
	v.Explanation = "Could not verify this claim: " + err.Error()
	v.Score, v.Breakdown = Score(c.Category, v, now)
	v.Label = labelFor(v.Score)
	return v
}

func llmTrace(step, claimID string, m llm.CallMeta, err error) TraceEntry {
	t := TraceEntry{
		Kind: "llm", Step: step, ClaimID: claimID,
		Provider: m.Provider, Model: m.Model, Fallback: m.Fallback,
		PromptTokens: m.PromptTokens, CompletionTokens: m.CompletionTokens, LatencyMs: m.LatencyMs,
	}
	if err != nil {
		t.Error = err.Error()
	}
	return t
}

func domainOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

func truncateRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
