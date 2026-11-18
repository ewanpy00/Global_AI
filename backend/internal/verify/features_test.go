package verify

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/demo"
	"github.com/ewanpy00/Global_AI/backend/internal/llm"
	"github.com/ewanpy00/Global_AI/backend/internal/search"
)

func newFullDemoEngine() *Engine {
	e := newDemoEngine()
	ds := &demo.Search{}
	e.Search, e.Fetch = ds, ds
	e.Features = AllFeatures()
	e.Features.Jury = true
	return e
}

func TestAllFeaturesOnDemoSample(t *testing.T) {
	report, err := newFullDemoEngine().Run(context.Background(), demo.SampleAnswer, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := report.Summary
	// The follow-up search turns the headcount claim from "unverifiable" into "outdated".
	if s.ByVerdict[VerdictOutdated] != 5 || s.ByVerdict[VerdictCurrent] != 3 || s.ByVerdict[VerdictUnverifiable] != 0 {
		t.Fatalf("verdict counts: %+v", s.ByVerdict)
	}
	var head *ClaimReport
	for i := range report.Claims {
		c := &report.Claims[i]
		if c.Attribute == "headcount" {
			head = c
		}
		r := c.Result
		if r.Grounding == nil || r.Grounding.Cited == 0 || r.Grounding.Verified != r.Grounding.Cited {
			t.Errorf("%s: every demo quote is verbatim, grounding = %+v", c.ID, r.Grounding)
		}
		if r.Jury == nil || r.Jury.Agreement != "agree" {
			t.Errorf("%s: jury = %+v", c.ID, r.Jury)
		}
	}
	if head == nil || head.Result.Hops != 2 || head.Result.CurrentValue == "" || !strings.Contains(head.Result.FollowUpQuery, "annual report") {
		t.Fatalf("headcount follow-up: %+v", head.Result)
	}
	u := s.Usage
	if u.FollowUps != 1 || u.PageFetches < 2 || u.QuotesVerified != u.QuotesCited {
		t.Fatalf("usage: %+v", u)
	}
	var official int
	for _, tr := range report.Trace {
		if tr.Step == "official" {
			official++
		}
	}
	if official != 8 {
		t.Fatalf("expected an official-domain search per claim, got %d", official)
	}
}

func TestQuoteFound(t *testing.T) {
	src := "Pro: $15 per user / month, 100 GB storage per user,\n priority support. Business: $29."
	cases := []struct {
		quote string
		want  bool
	}{
		{"Pro: $15 per user / month, 100 GB storage per user, priority support.", true},
		{"pro $15 per user/month", true},
		{"Pro: $15 per user … priority support", true},
		{"Pro: $12 per user / month", false},
		{"$15", false}, // too short to prove anything
		{"", false},
	}
	for _, c := range cases {
		if got := quoteFound(c.quote, src); got != c.want {
			t.Errorf("quoteFound(%q) = %v, want %v", c.quote, got, c.want)
		}
	}
}

// scriptedLLM returns fixed replies per purpose.
type scriptedLLM struct {
	extract, judge, jury string
	calls                atomic.Int32
}

func (s *scriptedLLM) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	s.calls.Add(1)
	meta := llm.CallMeta{Provider: "test", Model: string(req.Purpose)}
	switch req.Purpose {
	case llm.PurposeExtract:
		return llm.Response{Text: s.extract, Meta: meta}, nil
	case llm.PurposeJury:
		return llm.Response{Text: s.jury, Meta: meta}, nil
	}
	return llm.Response{Text: s.judge, Meta: meta}, nil
}

type countingSearch struct{ n atomic.Int32 }

func (c *countingSearch) Search(_ context.Context, q search.Query) (*search.Response, error) {
	c.n.Add(1)
	return &search.Response{Query: q.Text, Results: []search.Result{{
		Title: "Acme pricing", URL: "https://acme.example/pricing", PublishedDate: "2026-10-01", Content: "Acme Pro: $14 per month, billed yearly.",
	}}}, nil
}

const acmeExtract = `{"claims":[{"text":"Acme Pro costs $10 per month","category":"pricing","entity":"Acme","attribute":"Pro price","claimed_value":"$10","time_sensitivity":"high","search_query":"Acme Pro price"}]}`

func TestGroundingDiscardsInventedQuote(t *testing.T) {
	l := &scriptedLLM{
		extract: acmeExtract,
		judge:   `{"verdict":"outdated","current_value":"$14","confidence":0.9,"explanation":"[1]","corrected_text":"Acme Pro costs $14 per month","sources":[{"index":1,"stance":"contradicts","official":true,"quote":"Acme Pro now costs $14 a month for everyone"}]}`,
	}
	e := &Engine{LLM: l, Search: &countingSearch{}, Features: Features{Grounding: true}, Now: func() time.Time { return testNow }}
	rep, err := e.Run(context.Background(), "Acme Pro costs $10 per month.", nil)
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Claims[0].Result
	if r.Grounding == nil || r.Grounding.Cited != 1 || r.Grounding.Verified != 0 {
		t.Fatalf("grounding = %+v", r.Grounding)
	}
	if r.Sources[0].Stance != StanceNeutral || r.Sources[0].Grounded == nil || *r.Sources[0].Grounded {
		t.Fatalf("an invented quote must not count as evidence: %+v", r.Sources[0])
	}
	if r.Confidence != 0.45 {
		t.Fatalf("confidence %v, want halved to 0.45", r.Confidence)
	}
}

func TestJuryDisagreementIsContested(t *testing.T) {
	l := &scriptedLLM{
		extract: acmeExtract,
		judge:   `{"verdict":"outdated","current_value":"$14","confidence":0.9,"explanation":"Price is $14 [1].","corrected_text":"Acme Pro costs $14 per month","sources":[{"index":1,"stance":"contradicts","official":true}]}`,
		jury:    `{"verdict":"current","confidence":0.7,"explanation":"Still $10.","sources":[{"index":1,"stance":"supports"}]}`,
	}
	e := &Engine{LLM: l, Search: &countingSearch{}, Features: Features{Jury: true}, Now: func() time.Time { return testNow }}
	rep, err := e.Run(context.Background(), "Acme Pro costs $10 per month.", nil)
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Claims[0].Result
	if r.Verdict != VerdictUnverifiable || r.Jury == nil || r.Jury.Agreement != "contested" || r.CorrectedText != "" {
		t.Fatalf("contested claim: %+v", r)
	}
	if !strings.Contains(r.Explanation, "Judges disagree") || rep.Summary.Usage.Contested != 1 {
		t.Fatalf("explanation %q, usage %+v", r.Explanation, rep.Summary.Usage)
	}
}

func TestShareSearchDedupesSameFact(t *testing.T) {
	l := &scriptedLLM{
		extract: `{"claims":[
			{"text":"Acme Pro costs $10 per month","category":"pricing","entity":"Acme","attribute":"Pro price","search_query":"Acme Pro price"},
			{"text":"the Pro tier is $10","category":"pricing","entity":"Acme","attribute":"Pro price","search_query":"acme pro plan monthly cost"}]}`,
		judge: `{"verdict":"outdated","confidence":0.9,"explanation":"x","sources":[]}`,
	}
	s := &countingSearch{}
	e := &Engine{LLM: l, Search: s, Concurrency: 2, Features: Features{ShareSearch: true}, Now: func() time.Time { return testNow }}
	rep, err := e.Run(context.Background(), "Acme Pro costs $10 per month. In short, the Pro tier is $10.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Claims) != 2 || s.n.Load() != 1 {
		t.Fatalf("claims %d, Tavily calls %d (want 1)", len(rep.Claims), s.n.Load())
	}
	if rep.Summary.Usage.SharedSearches != 1 {
		t.Fatalf("usage %+v", rep.Summary.Usage)
	}
}

func TestFocusWindowKeepsRelevantParagraphs(t *testing.T) {
	page := strings.Repeat("Unrelated marketing text about our mission.\n", 200) + "Pro: $15 per user / month.\n" + strings.Repeat("Footer links.\n", 100)
	out := focusWindow(page, []string{"pro", "15", "user"}, 500)
	if !strings.Contains(out, "Pro: $15 per user / month.") || len(out) > 500 {
		t.Fatalf("focus window: %q", out)
	}
}

func TestCleanDomain(t *testing.T) {
	for in, want := range map[string]string{"https://www.Python.org/downloads": "python.org", "stripe.com": "stripe.com", "": "", "not a domain": "", "localhost": ""} {
		if got := cleanDomain(in); got != want {
			t.Errorf("cleanDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
