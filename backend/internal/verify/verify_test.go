package verify

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/demo"
	"github.com/ewanpy00/Global_AI/backend/internal/llm"
)

var testNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func TestLocate(t *testing.T) {
	text := "**Pricing.** Its “Pro” plan costs $12  per month. Ещё раз: the Pro plan costs $12."
	cases := []struct {
		name, quote string
		want        string
	}{
		{"exact", "Its “Pro” plan costs $12  per month", "Its “Pro” plan costs $12  per month"},
		{"quotes and spaces normalized", `its "Pro" plan costs $12 per month`, "Its “Pro” plan costs $12  per month"},
		{"markdown dropped", "Pricing. Its", "Pricing.** Its"},
		{"cyrillic", "Ещё раз", "Ещё раз"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, e, ok := locate(text, c.quote, 0)
			if !ok {
				t.Fatalf("not found")
			}
			if got := text[s:e]; got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
	if _, _, ok := locate(text, "not in the text", 0); ok {
		t.Fatal("expected no match")
	}
	// `from` makes repeated phrases resolve in reading order.
	rep := "The plan costs $12. Later: the plan costs $12."
	s1, e1, _ := locate(rep, "plan costs $12", 0)
	s2, _, _ := locate(rep, "plan costs $12", e1)
	if s2 <= s1 {
		t.Fatalf("second match should come after the first: %d <= %d", s2, s1)
	}
}

func TestUTF16Offset(t *testing.T) {
	s := "a😀б"
	// 'a' = 1 unit, emoji = 2 units (surrogate pair), so 'б' starts at 3.
	if got := utf16Offset(s, strings.Index(s, "б")); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestDecodeJSON(t *testing.T) {
	var v struct {
		A int `json:"a"`
	}
	for _, in := range []string{
		`{"a":1}`,
		"```json\n{\"a\":1}\n```",
		"Sure! Here it is: {\"a\":1} hope that helps",
		`prefix {"a":1, "s":"has } brace"} suffix`,
		"<think>maybe {\"a\":2}? no.</think>\n{\"a\":1}",
	} {
		v.A = 0
		if err := llm.DecodeJSON(in, &v); err != nil || v.A != 1 {
			t.Fatalf("DecodeJSON(%q) = %v, a=%d", in, err, v.A)
		}
	}
	if err := llm.DecodeJSON("no json here", &v); err == nil {
		t.Fatal("expected error")
	}
}

func TestScoreBands(t *testing.T) {
	strongCurrent := &Verification{Verdict: VerdictCurrent, Confidence: 0.95, Sources: []Source{
		{Stance: StanceSupports, Official: true, PublishedDate: "2026-11-01"},
		{Stance: StanceSupports, PublishedDate: "2026-10-01"},
	}}
	weakCurrent := &Verification{Verdict: VerdictCurrent, Confidence: 0.4, Sources: []Source{
		{Stance: StanceSupports, PublishedDate: "2025-01-01"},
	}}
	strongOutdated := &Verification{Verdict: VerdictOutdated, Confidence: 0.95, Sources: []Source{
		{Stance: StanceContradicts, Official: true, PublishedDate: "2026-11-01"},
		{Stance: StanceContradicts, PublishedDate: "2026-09-01"},
	}}
	unverifiedPrice := &Verification{Verdict: VerdictUnverifiable}
	unverifiedOther := &Verification{Verdict: VerdictUnverifiable}

	check := func(name string, cat Category, v *Verification, lo, hi int, label Label) {
		t.Helper()
		got, b := Score(cat, v, testNow)
		if got < lo || got > hi {
			t.Errorf("%s: score %d not in [%d,%d] (breakdown %+v)", name, got, lo, hi, b)
		}
		if l := labelFor(got); l != label {
			t.Errorf("%s: label %s, want %s", name, l, label)
		}
	}
	check("strong current", CatPricing, strongCurrent, 90, 100, LabelFresh)
	check("weak current", CatPricing, weakCurrent, 50, 74, LabelReview)
	check("strong outdated", CatPricing, strongOutdated, 0, 10, LabelStale)
	check("unverified price", CatPricing, unverifiedPrice, 45, 45, LabelReview)
	check("unverified other", CatOther, unverifiedOther, 55, 56, LabelReview)
}

func TestSummarizeWeightsAndCaps(t *testing.T) {
	claims := []ClaimReport{
		{Claim: Claim{Sensitivity: SensLow}, Result: &Verification{Verdict: VerdictCurrent, Score: 100, Label: LabelFresh}},
		{Claim: Claim{Sensitivity: SensLow}, Result: &Verification{Verdict: VerdictCurrent, Score: 100, Label: LabelFresh}},
		{Claim: Claim{Sensitivity: SensLow}, Result: &Verification{Verdict: VerdictCurrent, Score: 100, Label: LabelFresh}},
		{Claim: Claim{Sensitivity: SensLow}, Result: &Verification{Verdict: VerdictCurrent, Score: 100, Label: LabelFresh}},
		{Claim: Claim{Sensitivity: SensLow}, Result: &Verification{Verdict: VerdictOutdated, Score: 20, Label: LabelStale}},
	}
	s := Summarize(claims)
	if s.Score != 84 {
		t.Fatalf("score %d, want 84", s.Score)
	}
	// A stale claim keeps the answer out of the "fresh" band even when the mean is high.
	if s.Label != LabelReview {
		t.Fatalf("label %s, want review", s.Label)
	}
	if empty := Summarize(nil); empty.Score != 100 || empty.Total != 0 {
		t.Fatalf("empty summary: %+v", empty)
	}
}

func newDemoEngine() *Engine {
	return &Engine{
		LLM:         &demo.LLM{},
		Search:      &demo.Search{},
		MaxClaims:   12,
		Concurrency: 3,
		Now:         func() time.Time { return testNow },
	}
}

func TestPipelineOnDemoSample(t *testing.T) {
	var mu sync.Mutex
	events := map[string]int{}
	report, err := newDemoEngine().Run(context.Background(), demo.SampleAnswer, func(ev Event) {
		mu.Lock()
		events[ev.Type]++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Claims) != 8 {
		t.Fatalf("got %d claims, want 8", len(report.Claims))
	}
	for i, c := range report.Claims {
		if !c.Located {
			t.Errorf("claim %s not located: %q", c.ID, c.Text)
		}
		if i > 0 && c.Start < report.Claims[i-1].End {
			t.Errorf("claims out of order or overlapping at %s", c.ID)
		}
		if c.Result == nil {
			t.Fatalf("claim %s has no result", c.ID)
		}
	}
	s := report.Summary
	if s.ByVerdict[VerdictOutdated] != 4 || s.ByVerdict[VerdictCurrent] != 3 || s.ByVerdict[VerdictUnverifiable] != 1 {
		t.Fatalf("verdict counts: %+v", s.ByVerdict)
	}
	if s.Label == LabelFresh {
		t.Fatalf("an answer with 4 outdated claims must not be fresh: %+v", s)
	}
	if events[EvClaimResult] != 8 || events[EvClaims] != 1 || events[EvSummary] != 1 {
		t.Fatalf("events: %+v", events)
	}
	// 1 extract call + 8 searches + 8 judge calls.
	if len(report.Trace) != 17 {
		t.Fatalf("trace has %d entries, want 17", len(report.Trace))
	}
}

func TestApplyFixesAndFootnote(t *testing.T) {
	report, err := newDemoEngine().Run(context.Background(), demo.SampleAnswer, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixed, edits := ApplyFixes(report)
	if len(edits) != 4 {
		t.Fatalf("got %d edits, want 4", len(edits))
	}
	for _, want := range []string{"$15 per user per month", "SDK is 4.1", "retired on March 31, 2026", "CEO Daniel Okafor"} {
		if !strings.Contains(fixed, want) {
			t.Errorf("corrected text lacks %q", want)
		}
	}
	for _, gone := range []string{"$12 per user", "SDK is 3.2", "still supported", "CEO Maria Lindqvist"} {
		if strings.Contains(fixed, gone) {
			t.Errorf("corrected text still contains %q", gone)
		}
	}
	// Untouched text survives.
	if !strings.Contains(fixed, "who founded it in 2015") {
		t.Error("unrelated text was changed")
	}
	note := Footnote(report, edits)
	if !strings.Contains(note, "Freshness check:") || !strings.Contains(note, "~~") {
		t.Errorf("footnote missing parts:\n%s", note)
	}
	if strings.Contains(Footnote(report, nil), "~~") {
		t.Error("annotate-mode footnote should not list corrections")
	}
}

func TestUnknownTextIsUnverifiableInDemo(t *testing.T) {
	report, err := newDemoEngine().Run(context.Background(), "The latest version of FooLib is 2.0 and it costs $5 per month.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Claims) == 0 {
		t.Fatal("heuristic extractor found nothing")
	}
	for _, c := range report.Claims {
		if c.Result.Verdict != VerdictUnverifiable {
			t.Errorf("demo mode must not invent verdicts for unknown text, got %s", c.Result.Verdict)
		}
	}
}

func TestEmptyInput(t *testing.T) {
	if _, err := newDemoEngine().Run(context.Background(), "   ", nil); err == nil {
		t.Fatal("expected error for empty input")
	}
}
