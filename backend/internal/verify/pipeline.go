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

// Features switches the accuracy upgrades of the pipeline on and off. The zero value is the
// plain pipeline (one search, one judge); the benchmark compares the two (`make eval`).
type Features struct {
	// OfficialSearch runs a second, domain-restricted search on the claim's official site
	// (or, for leadership/company news, a news search over the past year).
	OfficialSearch bool `json:"official_search"`
	// FetchPages pulls the full official page with Tavily Extract when its snippet is too thin
	// (pricing tables, changelogs).
	FetchPages bool `json:"fetch_pages"`
	// FollowUp lets the judge ask for one more targeted search when the evidence is inconclusive.
	FollowUp bool `json:"follow_up"`
	// Grounding discards any stance whose quoted passage is not found in the source.
	Grounding bool `json:"grounding"`
	// ShareSearch runs identical searches once per run (claims about the same entity and attribute).
	ShareSearch bool `json:"share_search"`
	// Jury asks an independent second judge (another provider via Bifrost) and flags disagreement.
	Jury bool `json:"jury"`
}

// AllFeatures turns every upgrade on except the jury, which needs a second model route.
func AllFeatures() Features {
	return Features{OfficialSearch: true, FetchPages: true, FollowUp: true, Grounding: true, ShareSearch: true}
}

// Engine runs the verification pipeline. LLM calls go through Bifrost, searches through Tavily.
type Engine struct {
	LLM    llm.Client
	Search search.Searcher
	// Fetch (optional) pulls full pages; needed for Features.FetchPages.
	Fetch       search.Extractor
	MaxClaims   int
	Concurrency int
	Features    Features
	// TokenBudget caps the tokens of one run (enforced by the Bifrost ledger plugin; 0 = none).
	TokenBudget int
	Now         func() time.Time
}

// RunOptions tunes a single run.
type RunOptions struct {
	MaxClaims int // overrides Engine.MaxClaims (the site audit uses fewer claims per page)
}

// MaxAnswerChars bounds the input so one request cannot run up an unbounded bill.
const MaxAnswerChars = 60_000

// Run verifies an answer. emit (optional) receives progress events as they happen;
// it is called from several goroutines but never concurrently.
func (e *Engine) Run(ctx context.Context, text string, emit func(Event)) (*Report, error) {
	return e.RunWith(ctx, text, RunOptions{}, emit)
}

// RunWith is Run with per-run options.
func (e *Engine) RunWith(ctx context.Context, text string, opts RunOptions, emit func(Event)) (*Report, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("answer text is empty")
	}
	if utf8.RuneCountInString(text) > MaxAnswerChars {
		return nil, fmt.Errorf("answer is longer than %d characters", MaxAnswerChars)
	}
	r := e.newRun(text, emit)
	ctx = r.withBudget(ctx)

	r.emit(Event{EvStage, map[string]string{"stage": "extracting"}})
	maxClaims := opts.MaxClaims
	if maxClaims <= 0 {
		maxClaims = e.MaxClaims
	}
	claims, err := r.extract(ctx, maxClaims)
	if err != nil {
		return nil, fmt.Errorf("extract claims: %w", err)
	}
	return r.verifyAll(ctx, claims), nil
}

// VerifyClaims skips extraction and verifies the given claims (spans are located in text).
// The benchmark uses it to score the verifier on labelled claims.
func (e *Engine) VerifyClaims(ctx context.Context, text string, claims []Claim, emit func(Event)) *Report {
	r := e.newRun(strings.TrimSpace(text), emit)
	ctx = r.withBudget(ctx)
	out := make([]Claim, 0, len(claims))
	for i, c := range claims {
		c.Category = normalizeCategory(string(c.Category))
		c.Sensitivity = normalizeSensitivity(string(c.Sensitivity))
		if c.SearchQuery == "" {
			c.SearchQuery = strings.TrimSpace(c.Entity + " " + c.Attribute)
		}
		if s, en, ok := locate(r.text, c.Text, 0); ok {
			c.byteStart, c.byteEnd, c.Located = s, en, true
			c.Start, c.End = utf16Offset(r.text, s), utf16Offset(r.text, en)
		}
		c.ID = fmt.Sprintf("c%d", i+1)
		out = append(out, c)
	}
	return r.verifyAll(ctx, out)
}

// ExtractClaims runs only the extraction step (benchmark: claim recall and precision).
func (e *Engine) ExtractClaims(ctx context.Context, text string) ([]Claim, []TraceEntry, error) {
	r := e.newRun(strings.TrimSpace(text), nil)
	claims, err := r.extract(r.withBudget(ctx), e.MaxClaims)
	return claims, r.trace, err
}

func (e *Engine) newRun(text string, emit func(Event)) *run {
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	r := &run{engine: e, started: now(), now: now, text: text, flights: map[string]*flight{}}
	var mu sync.Mutex
	r.emit = func(ev Event) {
		if emit == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		emit(ev)
	}
	return r
}

func (r *run) withBudget(ctx context.Context) context.Context {
	if r.engine.TokenBudget > 0 {
		return llm.WithBudget(ctx, &llm.Budget{Limit: int64(r.engine.TokenBudget)})
	}
	return ctx
}

func (r *run) verifyAll(ctx context.Context, claims []Claim) *Report {
	e := r.engine
	if e.Features.ShareSearch {
		shareQueries(claims)
	}
	r.emit(Event{EvClaims, map[string]any{"claims": claims}})
	report := &Report{ID: newID(), Text: r.text, CheckedAt: r.started}

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
				res = failed(c, ctx.Err(), r.now())
			}
			report.Claims[i].Result = res
			r.emit(Event{EvClaimResult, map[string]any{"claim_id": c.ID, "result": res}})
		}(i, c)
	}
	wg.Wait()

	report.Trace = r.trace
	report.Summary = Summarize(report.Claims)
	report.Summary.DurationMs = r.now().Sub(r.started).Milliseconds()
	report.Summary.Usage = usageOf(report.Trace, report.Claims)
	r.emit(Event{EvSummary, report.Summary})
	r.emit(Event{EvStage, map[string]string{"stage": "done"}})
	return report
}

// shareQueries gives claims about the same entity and attribute the same search query, so the
// run searches once for all of them (the in-flight dedupe in run.search does the rest).
func shareQueries(claims []Claim) {
	first := map[string]string{}
	for i := range claims {
		k := strings.ToLower(strings.TrimSpace(claims[i].Entity)) + "|" + strings.ToLower(strings.TrimSpace(claims[i].Attribute))
		if k == "|" {
			continue
		}
		if q, ok := first[k]; ok {
			claims[i].SearchQuery = q
		} else {
			first[k] = claims[i].SearchQuery
		}
	}
}

type run struct {
	engine  *Engine
	started time.Time
	now     func() time.Time
	text    string
	emit    func(Event)

	traceMu sync.Mutex
	trace   []TraceEntry

	flightMu sync.Mutex
	flights  map[string]*flight
}

type flight struct {
	done chan struct{}
	resp *search.Response
	err  error
}

func (r *run) record(t TraceEntry) {
	t.AtMs = r.now().Sub(r.started).Milliseconds()
	r.traceMu.Lock()
	r.trace = append(r.trace, t)
	r.traceMu.Unlock()
	r.emit(Event{EvTrace, t})
}

type extractedClaim struct {
	Text           string `json:"text"`
	Category       string `json:"category"`
	Entity         string `json:"entity"`
	Attribute      string `json:"attribute"`
	ClaimedValue   string `json:"claimed_value"`
	Sensitivity    string `json:"time_sensitivity"`
	SearchQuery    string `json:"search_query"`
	Why            string `json:"why_time_sensitive"`
	OfficialDomain string `json:"official_domain"`
}

func (r *run) extract(ctx context.Context, maxClaims int) ([]Claim, error) {
	if maxClaims <= 0 {
		maxClaims = 12
	}
	resp, err := r.engine.LLM.Complete(ctx, llm.Request{
		Purpose:   llm.PurposeExtract,
		System:    extractSystemPrompt(r.now(), maxClaims),
		Messages:  []llm.Message{{Role: "user", Content: extractUserPrompt(r.text)}},
		MaxTokens: 4096,
	})
	r.record(llmTrace("extract", "", 0, resp.Meta, err))
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
			Text:           strings.TrimSpace(x.Text),
			Category:       normalizeCategory(x.Category),
			Entity:         strings.TrimSpace(x.Entity),
			Attribute:      strings.TrimSpace(x.Attribute),
			ClaimedValue:   strings.TrimSpace(x.ClaimedValue),
			Sensitivity:    normalizeSensitivity(x.Sensitivity),
			SearchQuery:    strings.TrimSpace(x.SearchQuery),
			Why:            strings.TrimSpace(x.Why),
			OfficialDomain: cleanDomain(x.OfficialDomain),
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

// cleanDomain normalises the extractor's official_domain guess ("https://www.python.org/x" → "python.org").
func cleanDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || strings.ContainsAny(s, " \t") {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || !strings.Contains(u.Hostname(), ".") {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
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
	FollowUpQuery string  `json:"follow_up_query"`
	Sources       []struct {
		Index    int    `json:"index"`
		Stance   string `json:"stance"`
		Official bool   `json:"official"`
		Quote    string `json:"quote"`
	} `json:"sources"`

	model string // provider/model that produced it (from Bifrost routing info)
}

// verifyClaim gathers evidence, judges it (with an optional jury), follows up once if the
// evidence is inconclusive, then applies the grounding check and scores the claim.
func (r *run) verifyClaim(ctx context.Context, c Claim) *Verification {
	f := r.engine.Features
	r.status(c, "searching")
	ev, err := r.gather(ctx, c)
	if err != nil {
		return failedWith(c, &Verification{SearchQuery: c.SearchQuery, SearchQueries: ev.queries}, fmt.Errorf("search: %w", err), r.now())
	}
	if f.FetchPages {
		r.fetchOfficial(ctx, c, ev)
	}

	r.status(c, "judging")
	j, jury, err := r.judgeAll(ctx, c, ev, 1)
	if err != nil {
		v := &Verification{SearchQuery: c.SearchQuery, SearchQueries: ev.queries}
		r.applySources(v, c, ev, nil)
		return failedWith(c, v, fmt.Errorf("judge: %w", err), r.now())
	}

	hops := 1
	followUp := ""
	if f.FollowUp && needsFollowUp(j) {
		if q := followUpQuery(c, j, ev); q != "" {
			followUp = q
			r.status(c, "following_up")
			if added := r.followUp(ctx, c, ev, q); added > 0 {
				r.status(c, "judging")
				if j2, jury2, err := r.judgeAll(ctx, c, ev, 2); err == nil {
					j, jury = j2, jury2
					hops = 2
				}
			}
		}
	}

	v := &Verification{
		SearchQuery:   c.SearchQuery,
		SearchQueries: ev.queries,
		SearchSummary: ev.answer,
		Hops:          hops,
		FollowUpQuery: followUp,
	}
	v.Verdict = normalizeVerdict(j.Verdict)
	v.CurrentValue = strings.TrimSpace(j.CurrentValue)
	v.Confidence = clamp01(j.Confidence)
	v.Explanation = strings.TrimSpace(j.Explanation)
	v.AsOf = strings.TrimSpace(j.AsOf)
	if v.Verdict == VerdictOutdated || v.Verdict == VerdictIncorrect {
		v.CorrectedText = strings.TrimSpace(j.CorrectedText)
	}
	r.applySources(v, c, ev, &j)
	if jury != nil {
		combineJury(v, j.model, jury)
	}
	v.Score, v.Breakdown = Score(c.Category, v, r.now())
	v.Label = labelFor(v.Score)
	return v
}

func (r *run) status(c Claim, s string) {
	r.emit(Event{EvClaimStatus, map[string]string{"claim_id": c.ID, "status": s}})
}

// judgeAll runs the main judge and, when enabled, the jury in parallel on the same evidence.
// A jury failure is not fatal: the claim keeps the main verdict.
func (r *run) judgeAll(ctx context.Context, c Claim, ev *evidence, hop int) (judgement, *judgement, error) {
	if !r.engine.Features.Jury {
		j, err := r.judge(ctx, llm.PurposeJudge, c, ev, hop)
		return j, nil, err
	}
	var (
		wg      sync.WaitGroup
		jj      judgement
		jerr    error
		jury    judgement
		juryErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); jj, jerr = r.judge(ctx, llm.PurposeJudge, c, ev, hop) }()
	go func() { defer wg.Done(); jury, juryErr = r.judge(ctx, llm.PurposeJury, c, ev, hop) }()
	wg.Wait()
	if jerr != nil {
		return jj, nil, jerr
	}
	if juryErr != nil {
		return jj, nil, nil
	}
	return jj, &jury, nil
}

func (r *run) judge(ctx context.Context, purpose llm.Purpose, c Claim, ev *evidence, hop int) (judgement, error) {
	resp, err := r.engine.LLM.Complete(ctx, llm.Request{
		Purpose:   purpose,
		System:    judgeSystemPrompt(r.now(), r.engine.Features.Grounding),
		Messages:  []llm.Message{{Role: "user", Content: judgeUserPrompt(c, ev)}},
		MaxTokens: 2048,
	})
	r.record(llmTrace(string(purpose), c.ID, hop, resp.Meta, err))
	var j judgement
	if err != nil {
		return j, err
	}
	if err := llm.DecodeJSON(resp.Text, &j); err != nil {
		return j, fmt.Errorf("decode %s reply: %w", purpose, err)
	}
	j.model = resp.Meta.Provider + "/" + resp.Meta.Model
	return j, nil
}

// applySources turns the evidence and the judge's per-source stances into v.Sources, running
// the grounding check: with Features.Grounding, a stance only counts if its quote is in the source.
func (r *run) applySources(v *Verification, c Claim, ev *evidence, j *judgement) {
	for i, it := range ev.items {
		v.Sources = append(v.Sources, Source{
			Index:         i + 1,
			Title:         it.res.Title,
			URL:           it.res.URL,
			Domain:        domainOf(it.res.URL),
			Snippet:       truncateRunes(it.shown, 360),
			PublishedDate: it.res.PublishedDate,
			Relevance:     it.res.Score,
			Stance:        StanceNeutral,
			Origin:        it.origin,
			Fetched:       it.fetched,
		})
	}
	if j == nil {
		return
	}
	grounding := r.engine.Features.Grounding
	var g Grounding
	for _, js := range j.Sources {
		if js.Index < 1 || js.Index > len(v.Sources) {
			continue
		}
		s := &v.Sources[js.Index-1]
		s.Official = js.Official || (c.OfficialDomain != "" && domainMatches(s.Domain, c.OfficialDomain))
		st := Stance(js.Stance)
		if st != StanceSupports && st != StanceContradicts {
			continue
		}
		s.Stance = st
		s.Quote = truncateRunes(js.Quote, 240)
		if !grounding {
			continue
		}
		g.Cited++
		ok := quoteFound(js.Quote, ev.items[js.Index-1].shown)
		s.Grounded = &ok
		if ok {
			g.Verified++
		} else {
			s.Stance = StanceNeutral // an unsupported citation is not evidence
		}
	}
	if !grounding {
		return
	}
	v.Grounding = &g
	definite := v.Verdict == VerdictCurrent || v.Verdict == VerdictOutdated || v.Verdict == VerdictIncorrect
	if definite && g.Verified == 0 {
		v.Confidence = round2(v.Confidence * 0.5)
		v.Explanation = strings.TrimSpace(v.Explanation + " (None of the cited passages could be found in the sources, so confidence was halved.)")
	}
}

func needsFollowUp(j judgement) bool {
	return normalizeVerdict(j.Verdict) == VerdictUnverifiable || j.Confidence < 0.6
}

// followUpQuery picks the judge's suggested query, or a fallback aimed at the official source.
func followUpQuery(c Claim, j judgement, ev *evidence) string {
	q := strings.TrimSpace(j.FollowUpQuery)
	if q == "" {
		q = strings.TrimSpace(c.Entity + " " + c.Attribute)
		if q == "" {
			return ""
		}
		q += " official"
	}
	for _, prev := range ev.queries {
		if strings.EqualFold(prev, q) {
			return ""
		}
	}
	return q
}

// combineJury merges the jury's vote into the verification. Agreement raises nothing but keeps the
// verdict; a definite disagreement (one says current, the other outdated) makes the claim
// "unverifiable" and contested, so a human looks at it.
func combineJury(v *Verification, judgeModel string, jury *judgement) {
	jv := normalizeVerdict(jury.Verdict)
	vote := &JuryVote{
		Model:        jury.model,
		Verdict:      jv,
		Confidence:   clamp01(jury.Confidence),
		CurrentValue: strings.TrimSpace(jury.CurrentValue),
		Explanation:  strings.TrimSpace(jury.Explanation),
	}
	p1, p2 := polarity(v.Verdict), polarity(jv)
	switch {
	case p1 == p2:
		vote.Agreement = "agree"
		v.Confidence = round2((v.Confidence + vote.Confidence) / 2)
	case p1 == 0 || p2 == 0:
		vote.Agreement = "partial"
		v.Confidence = round2(v.Confidence * 0.8)
	default:
		vote.Agreement = "contested"
		v.Explanation = fmt.Sprintf("Judges disagree. %s: %s (%s) %s: %s (%s)",
			judgeModel, v.Verdict, strings.TrimSuffix(v.Explanation, "."), vote.Model, jv, strings.TrimSuffix(vote.Explanation, "."))
		v.Verdict = VerdictUnverifiable
		v.Confidence = round2(minF(v.Confidence, vote.Confidence) * 0.5)
		v.CorrectedText = ""
	}
	v.Jury = vote
}

func polarity(v Verdict) int {
	switch v {
	case VerdictCurrent:
		return 1
	case VerdictOutdated, VerdictIncorrect:
		return -1
	}
	return 0
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
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

func llmTrace(step, claimID string, hop int, m llm.CallMeta, err error) TraceEntry {
	t := TraceEntry{
		Kind: "llm", Step: step, ClaimID: claimID,
		Provider: m.Provider, Model: m.Model, Fallback: m.Fallback, Cached: m.Cached,
		PromptTokens: m.PromptTokens, CompletionTokens: m.CompletionTokens, LatencyMs: m.LatencyMs,
	}
	if hop > 1 {
		t.Hop = hop
	}
	if err != nil {
		t.Error = err.Error()
	}
	return t
}

// usageOf totals what the run spent, from its trace.
func usageOf(trace []TraceEntry, claims []ClaimReport) Usage {
	var u Usage
	for _, t := range trace {
		if t.Error != "" {
			continue
		}
		switch t.Kind {
		case "llm":
			u.LLMCalls++
			if t.Cached {
				u.CachedLLMCalls++
			} else {
				u.Tokens += t.PromptTokens + t.CompletionTokens
			}
		case "search":
			if t.Step == "fetch" {
				u.PageFetches++
			} else {
				u.Searches++
				if t.Cached {
					u.CachedSearches++
				}
				if t.Shared {
					u.SharedSearches++
				}
			}
			u.TavilyCredits += t.Credits
		}
	}
	for _, c := range claims {
		if c.Result == nil {
			continue
		}
		if c.Result.Hops > 1 {
			u.FollowUps++
		}
		if c.Result.Jury != nil && c.Result.Jury.Agreement == "contested" {
			u.Contested++
		}
		if g := c.Result.Grounding; g != nil {
			u.QuotesCited += g.Cited
			u.QuotesVerified += g.Verified
		}
	}
	return u
}

func domainOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// domainMatches is true for the domain itself and its subdomains (docs.python.org ~ python.org).
func domainMatches(host, domain string) bool {
	host, domain = strings.ToLower(host), strings.ToLower(domain)
	return host == domain || strings.HasSuffix(host, "."+domain)
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
