package verify

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/ewanpy00/Global_AI/backend/internal/search"
)

// evidence is everything the judge sees for one claim: the sources of every search, in order.
type evidence struct {
	answer  string // Tavily's own summary of the first search (a hint for the judge)
	items   []evItem
	queries []string
	seen    map[string]bool
}

type evItem struct {
	res     search.Result
	origin  string // search | official | news | followup
	shown   string // the text shown to the judge (grounding checks quotes against exactly this)
	fetched bool
}

// maxEvidence caps the sources per claim, so the judge prompt stays small.
const maxEvidence = 10

func newEvidence() *evidence { return &evidence{seen: map[string]bool{}} }

// add appends results not seen yet and returns how many were new.
func (ev *evidence) add(results []search.Result, origin string) int {
	n := 0
	for _, res := range results {
		key := normURL(res.URL)
		if key == "" || ev.seen[key] || len(ev.items) >= maxEvidence {
			continue
		}
		ev.seen[key] = true
		ev.items = append(ev.items, evItem{res: res, origin: origin, shown: truncateRunes(res.Content, maxSnippet)})
		n++
	}
	return n
}

func normURL(u string) string {
	u = strings.TrimSpace(strings.ToLower(u))
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	u = strings.TrimPrefix(u, "www.")
	return strings.TrimRight(u, "/")
}

// gather runs the claim's searches. The main search always runs. With Features.OfficialSearch a
// second, cheaper search runs in parallel: restricted to the official domain when the extractor
// named one, otherwise (for leadership, company and statistics) a news search over the past year.
// Official results go first so the judge reads the authoritative source before aggregators.
func (r *run) gather(ctx context.Context, c Claim) (*evidence, error) {
	ev := newEvidence()
	type job struct {
		q      search.Query
		origin string
		resp   *search.Response
		err    error
	}
	jobs := []*job{{q: search.Query{Text: c.SearchQuery}, origin: "search"}}
	if r.engine.Features.OfficialSearch {
		switch {
		case c.OfficialDomain != "":
			jobs = append(jobs, &job{q: search.Query{Text: c.SearchQuery, IncludeDomains: []string{c.OfficialDomain}, Depth: "basic", MaxResults: 3}, origin: "official"})
		case c.Category == CatLeadership || c.Category == CatCompany || c.Category == CatStatistic:
			jobs = append(jobs, &job{q: search.Query{Text: c.SearchQuery, Topic: "news", TimeRange: "year", Depth: "basic", MaxResults: 3}, origin: "news"})
		}
	}
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j *job) {
			defer wg.Done()
			j.resp, j.err = r.search(ctx, c, j.q, j.origin)
		}(j)
	}
	wg.Wait()

	for _, j := range jobs {
		ev.queries = append(ev.queries, describeQuery(j.q))
	}
	// official first, then the main search, then news
	order := map[string]int{"official": 0, "search": 1, "news": 2}
	sort.SliceStable(jobs, func(a, b int) bool { return order[jobs[a].origin] < order[jobs[b].origin] })
	var errs []error
	ok := false
	for _, j := range jobs {
		if j.err != nil {
			errs = append(errs, j.err)
			continue
		}
		ok = true
		if ev.answer == "" && j.origin == "search" {
			ev.answer = j.resp.Answer
		}
		ev.add(j.resp.Results, j.origin)
	}
	if !ok {
		return ev, errors.Join(errs...)
	}
	return ev, nil
}

func describeQuery(q search.Query) string {
	s := q.Text
	if len(q.IncludeDomains) > 0 {
		s += " site:" + strings.Join(q.IncludeDomains, ",")
	}
	if q.Topic == "news" {
		s += " [news"
		if q.TimeRange != "" {
			s += ", past " + q.TimeRange
		}
		s += "]"
	}
	return s
}

// search runs one Tavily query and records it in the trace. With Features.ShareSearch, identical
// queries in the same run (claims about the same thing) share one call.
func (r *run) search(ctx context.Context, c Claim, q search.Query, step string) (*search.Response, error) {
	started := r.now()
	key := ""
	if r.engine.Features.ShareSearch {
		key = strings.ToLower(describeQuery(q)) + "|" + q.Depth
		r.flightMu.Lock()
		if f, ok := r.flights[key]; ok {
			r.flightMu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			t := TraceEntry{Kind: "search", Step: step, ClaimID: c.ID, Query: describeQuery(q), Shared: true, Cached: true,
				LatencyMs: r.now().Sub(started).Milliseconds()}
			if f.err != nil {
				t.Error = f.err.Error()
			} else {
				t.Results = len(f.resp.Results)
			}
			r.record(t)
			return f.resp, f.err
		}
		f := &flight{done: make(chan struct{})}
		r.flights[key] = f
		r.flightMu.Unlock()
		defer close(f.done)
		resp, err := r.doSearch(ctx, c, q, step)
		f.resp, f.err = resp, err
		return resp, err
	}
	return r.doSearch(ctx, c, q, step)
}

func (r *run) doSearch(ctx context.Context, c Claim, q search.Query, step string) (*search.Response, error) {
	t0 := r.now()
	res, err := r.engine.Search.Search(ctx, q)
	t := TraceEntry{Kind: "search", Step: step, ClaimID: c.ID, Query: describeQuery(q), LatencyMs: r.now().Sub(t0).Milliseconds()}
	if err != nil {
		t.Error = err.Error()
		r.record(t)
		return nil, err
	}
	t.Results, t.Cached, t.Credits = len(res.Results), res.Cached, res.Credits
	if !res.Cached && res.ResponseTimeMs > 0 {
		t.LatencyMs = res.ResponseTimeMs
	}
	r.record(t)
	return res, nil
}

// followUp runs the judge's follow-up query and adds the new sources. Returns how many were new.
func (r *run) followUp(ctx context.Context, c Claim, ev *evidence, q string) int {
	ev.queries = append(ev.queries, q)
	res, err := r.search(ctx, c, search.Query{Text: q}, "followup")
	if err != nil {
		return 0
	}
	if ev.answer == "" {
		ev.answer = res.Answer
	}
	return ev.add(res.Results, "followup")
}

// fetchCategories are the facts that usually live in a table or changelog on an official page,
// where the search snippet often misses the decisive row.
var fetchCategories = map[Category]bool{CatPricing: true, CatAPIVersion: true, CatRegulation: true}

// fetchThreshold: a snippet shorter than this from an official page is replaced by the page itself.
const fetchThreshold = 1500

// fetchOfficial pulls the full text of the best official source with Tavily Extract when its
// snippet is thin, and shows the judge the passages that mention the claim's keywords.
func (r *run) fetchOfficial(ctx context.Context, c Claim, ev *evidence) {
	if r.engine.Fetch == nil || !fetchCategories[c.Category] {
		return
	}
	idx := -1
	for i, it := range ev.items {
		official := it.origin == "official" || (c.OfficialDomain != "" && domainMatches(domainOf(it.res.URL), c.OfficialDomain))
		if official {
			idx = i
			break
		}
	}
	if idx < 0 || len([]rune(ev.items[idx].res.Content)) >= fetchThreshold {
		return
	}
	r.status(c, "fetching")
	u := ev.items[idx].res.URL
	t0 := r.now()
	res, err := r.engine.Fetch.Extract(ctx, []string{u})
	t := TraceEntry{Kind: "search", Step: "fetch", ClaimID: c.ID, Query: u, LatencyMs: r.now().Sub(t0).Milliseconds()}
	if err != nil {
		t.Error = err.Error()
		r.record(t)
		return
	}
	t.Results, t.Cached, t.Credits = len(res.Pages), res.Cached, res.Credits
	r.record(t)
	if len(res.Pages) == 0 || strings.TrimSpace(res.Pages[0].Content) == "" {
		return
	}
	ev.items[idx].shown = focusWindow(res.Pages[0].Content, claimKeywords(c), maxFetchedPage)
	ev.items[idx].fetched = true
}

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "per": true, "with": true, "from": true, "that": true, "this": true,
	"are": true, "was": true, "its": true, "has": true, "have": true, "about": true, "current": true, "latest": true,
}

func claimKeywords(c Claim) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range []string{c.ClaimedValue, c.Attribute, c.Entity} {
		for _, w := range tokens(f) {
			if (len([]rune(w)) >= 3 || hasDigit(w)) && !stopwords[w] && !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	return out
}

// focusWindow keeps the paragraphs of a page that mention the most keywords, in page order, up to
// max characters. A pricing page of 30 KB becomes the 3 KB that talk about the claimed plan.
func focusWindow(page string, keywords []string, max int) string {
	paras := strings.FieldsFunc(page, func(r rune) bool { return r == '\n' })
	type scored struct {
		i, score int
		text     string
	}
	var ps []scored
	for i, p := range paras {
		p = strings.Join(strings.Fields(p), " ")
		if p == "" {
			continue
		}
		low := strings.ToLower(p)
		s := 0
		for _, k := range keywords {
			if strings.Contains(low, k) {
				s++
			}
		}
		ps = append(ps, scored{i: i, score: s, text: p})
	}
	ranked := append([]scored(nil), ps...)
	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].score > ranked[b].score })
	keep := map[int]bool{}
	total := 0
	for _, p := range ranked {
		if p.score == 0 && total > 0 {
			break
		}
		if total+len(p.text) > max {
			continue
		}
		keep[p.i] = true
		total += len(p.text) + 1
	}
	var b strings.Builder
	for _, p := range ps {
		if keep[p.i] {
			b.WriteString(p.text)
			b.WriteByte('\n')
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return truncateRunes(page, max)
	}
	return out
}

// quoteFound reports whether the judge's quote is really in the source text. Matching ignores
// case, punctuation and spacing, and accepts an elided quote ("a … b") if every part is present.
// Very short quotes (fewer than 3 words) are rejected: they prove nothing.
func quoteFound(quote, source string) bool {
	src := " " + strings.Join(tokens(source), " ") + " "
	parts := strings.FieldsFunc(quote, func(r rune) bool { return r == '…' })
	var expanded []string
	for _, p := range parts {
		expanded = append(expanded, strings.Split(p, "...")...)
	}
	words := 0
	for _, p := range expanded {
		tk := tokens(p)
		if len(tk) == 0 {
			continue
		}
		words += len(tk)
		if !strings.Contains(src, " "+strings.Join(tk, " ")+" ") {
			return false
		}
	}
	return words >= 3
}

// tokens lowercases and splits on anything that is not a letter or digit. "$15/mo" → ["15", "mo"],
// "4.1.0" → ["4", "1", "0"].
func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func hasDigit(s string) bool {
	return strings.IndexFunc(s, unicode.IsDigit) >= 0
}
