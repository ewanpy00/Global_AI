// Package demo provides offline stand-ins for the model and Tavily so the UI can be built and
// shown without API keys. The sample is about a fictional company ("Nimbus Cloud", *.example
// domains); every other text gets an honest "unverifiable — demo mode" result.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/llm"
	"github.com/ewanpy00/Global_AI/backend/internal/search"
)

// SampleAnswer is the bundled demo input: an "AI answer" whose facts went stale.
const SampleAnswer = `Nimbus Cloud is a solid pick for small teams that need shared storage and a simple API.

**Pricing.** Its Pro plan costs $12 per user per month and includes 50 GB of storage per user. A free tier is available for up to 3 users.

**Developer platform.** The current stable version of the Nimbus SDK is 3.2, and the v2 REST API is still supported, so older integrations keep working.

**Company.** The company is led by CEO Maria Lindqvist, who founded it in 2015. Nimbus has around 400 employees and is headquartered in Stockholm.

**Compliance.** Under the Aurora Data Act, providers must store EU customer data in the EU and report breaches within 72 hours.`

type fixture struct {
	claim    map[string]string
	results  []search.Result
	answer   string
	judgment map[string]any
}

var fixtures = []fixture{
	{
		claim:  map[string]string{"text": "Its Pro plan costs $12 per user per month and includes 50 GB of storage per user", "category": "pricing", "entity": "Nimbus Cloud", "attribute": "Pro plan price and storage", "claimed_value": "$12 per user per month, 50 GB", "time_sensitivity": "high", "search_query": "Nimbus Cloud Pro plan price per user", "why_time_sensitive": "SaaS prices change often"},
		answer: "Nimbus Cloud Pro costs $15 per user per month with 100 GB of storage per user.",
		results: []search.Result{
			{Title: "Pricing — Nimbus Cloud", URL: "https://nimbuscloud.example/pricing", PublishedDate: "2026-10-02", Score: 0.94, Content: "Free: up to 3 users, 5 GB each. Pro: $15 per user / month, 100 GB storage per user, priority support. Business: $29 per user / month."},
			{Title: "Nimbus Cloud raises Pro prices, doubles storage", URL: "https://techwire.example/2026/07/nimbus-pro-price", PublishedDate: "2026-07-14", Score: 0.88, Content: "Starting August 1, 2026 the Pro tier moves from $12 to $15 per user per month; included storage grows from 50 GB to 100 GB."},
			{Title: "Nimbus Cloud review (2025)", URL: "https://saasreviews.example/nimbus-cloud", PublishedDate: "2025-03-10", Score: 0.71, Content: "The Pro plan is $12 per user per month and comes with 50 GB of storage."},
		},
		judgment: map[string]any{"verdict": "outdated", "current_value": "$15 per user per month, 100 GB per user (since Aug 1, 2026)", "confidence": 0.93, "explanation": "The official pricing page lists Pro at $15 per user per month with 100 GB [1]; the change from $12 / 50 GB took effect on August 1, 2026 [2]. Only an older review still shows $12 [3].", "corrected_text": "Its Pro plan costs $15 per user per month and includes 100 GB of storage per user", "as_of": "2026-10-02",
			"sources": []map[string]any{{"index": 1, "stance": "contradicts", "official": true}, {"index": 2, "stance": "contradicts", "official": false}, {"index": 3, "stance": "supports", "official": false}}},
	},
	{
		claim:  map[string]string{"text": "A free tier is available for up to 3 users", "category": "pricing", "entity": "Nimbus Cloud", "attribute": "free tier limit", "claimed_value": "up to 3 users", "time_sensitivity": "high", "search_query": "Nimbus Cloud free tier users limit", "why_time_sensitive": "free tiers change"},
		answer: "The free plan supports up to 3 users.",
		results: []search.Result{
			{Title: "Pricing — Nimbus Cloud", URL: "https://nimbuscloud.example/pricing", PublishedDate: "2026-10-02", Score: 0.9, Content: "Free: up to 3 users, 5 GB each. Pro: $15 per user / month."},
		},
		judgment: map[string]any{"verdict": "current", "current_value": "up to 3 users", "confidence": 0.9, "explanation": "The official pricing page still offers a free plan for up to 3 users [1].", "as_of": "2026-10-02",
			"sources": []map[string]any{{"index": 1, "stance": "supports", "official": true}}},
	},
	{
		claim:  map[string]string{"text": "The current stable version of the Nimbus SDK is 3.2", "category": "api_version", "entity": "Nimbus SDK", "attribute": "latest stable version", "claimed_value": "3.2", "time_sensitivity": "high", "search_query": "Nimbus SDK latest stable release", "why_time_sensitive": "SDKs ship new versions"},
		answer: "The latest Nimbus SDK release is 4.1.0.",
		results: []search.Result{
			{Title: "Changelog — Nimbus Docs", URL: "https://docs.nimbuscloud.example/changelog", PublishedDate: "2026-09-22", Score: 0.95, Content: "4.1.0 (2026-09-22): streaming uploads, Python 3.14 support. 4.0.0 (2026-04-08): new async client, v3 API by default. 3.2.4 (2025-11-02): final 3.x maintenance release."},
		},
		judgment: map[string]any{"verdict": "outdated", "current_value": "4.1 (released Sep 22, 2026)", "confidence": 0.95, "explanation": "The official changelog shows 4.1.0 released on Sep 22, 2026; the 3.x line ended with 3.2.4 in Nov 2025 [1].", "corrected_text": "The current stable version of the Nimbus SDK is 4.1", "as_of": "2026-09-22",
			"sources": []map[string]any{{"index": 1, "stance": "contradicts", "official": true}}},
	},
	{
		claim:  map[string]string{"text": "the v2 REST API is still supported", "category": "api_version", "entity": "Nimbus REST API", "attribute": "v2 support status", "claimed_value": "supported", "time_sensitivity": "high", "search_query": "Nimbus Cloud v2 REST API deprecation", "why_time_sensitive": "API versions get retired"},
		answer: "Nimbus retired the v2 REST API on March 31, 2026.",
		results: []search.Result{
			{Title: "API v2 end of life", URL: "https://docs.nimbuscloud.example/api/v2-sunset", PublishedDate: "2026-04-01", Score: 0.93, Content: "API v2 was shut down on March 31, 2026. Requests to /v2 now return 410 Gone. Migrate to v3."},
			{Title: "Forum: is v2 still working?", URL: "https://community.nimbuscloud.example/t/v2-still-working", PublishedDate: "2025-12-05", Score: 0.61, Content: "Yes, v2 still works for now but it is deprecated."},
		},
		judgment: map[string]any{"verdict": "outdated", "current_value": "Retired on March 31, 2026; v3 is required", "confidence": 0.94, "explanation": "The official docs say API v2 was shut down on March 31, 2026 and now returns 410 Gone [1]. A December 2025 forum post predates the shutdown [2].", "corrected_text": "the v2 REST API was retired on March 31, 2026 (use v3)", "as_of": "2026-04-01",
			"sources": []map[string]any{{"index": 1, "stance": "contradicts", "official": true}, {"index": 2, "stance": "supports", "official": false}}},
	},
	{
		claim:  map[string]string{"text": "The company is led by CEO Maria Lindqvist", "category": "leadership", "entity": "Nimbus Cloud", "attribute": "CEO", "claimed_value": "Maria Lindqvist", "time_sensitivity": "medium", "search_query": "Nimbus Cloud CEO", "why_time_sensitive": "executives change"},
		answer: "Daniel Okafor is CEO of Nimbus Cloud; Maria Lindqvist is executive chair.",
		results: []search.Result{
			{Title: "Leadership — Nimbus Cloud", URL: "https://nimbuscloud.example/about/leadership", PublishedDate: "2026-08-19", Score: 0.92, Content: "Daniel Okafor, Chief Executive Officer. Maria Lindqvist, Founder & Executive Chair."},
			{Title: "Nimbus founder hands CEO role to Okafor", URL: "https://nordicbusiness.example/2026/05/nimbus-ceo", PublishedDate: "2026-05-12", Score: 0.86, Content: "Founder Maria Lindqvist moves to executive chair; former COO Daniel Okafor becomes CEO effective May 12."},
		},
		judgment: map[string]any{"verdict": "outdated", "current_value": "Daniel Okafor (CEO since May 2026)", "confidence": 0.92, "explanation": "The company's leadership page names Daniel Okafor as CEO [1]; Lindqvist became executive chair in May 2026 [2].", "corrected_text": "The company is led by CEO Daniel Okafor", "as_of": "2026-08-19",
			"sources": []map[string]any{{"index": 1, "stance": "contradicts", "official": true}, {"index": 2, "stance": "contradicts", "official": false}}},
	},
	{
		claim:  map[string]string{"text": "Nimbus has around 400 employees", "category": "company", "entity": "Nimbus Cloud", "attribute": "headcount", "claimed_value": "about 400", "time_sensitivity": "medium", "search_query": "Nimbus Cloud number of employees", "why_time_sensitive": "headcount changes"},
		answer: "Estimates vary.",
		results: []search.Result{
			{Title: "Nimbus Cloud — company profile", URL: "https://companydb.example/nimbus-cloud", Score: 0.66, Content: "Employees: 250-500. Founded 2015. Stockholm, Sweden."},
			{Title: "Nimbus Cloud hiring push (2024)", URL: "https://nordicbusiness.example/2024/02/nimbus-hiring", PublishedDate: "2024-02-20", Score: 0.58, Content: "The company plans to grow from 320 to about 400 people by the end of the year."},
		},
		judgment: map[string]any{"verdict": "unverifiable", "confidence": 0.4, "explanation": "Only an undated range (250–500) [1] and a 2024 hiring plan [2] mention headcount; there is no recent authoritative figure.",
			"sources": []map[string]any{{"index": 1, "stance": "neutral", "official": false}, {"index": 2, "stance": "neutral", "official": false}}},
	},
	{
		claim:  map[string]string{"text": "is headquartered in Stockholm", "category": "company", "entity": "Nimbus Cloud", "attribute": "headquarters", "claimed_value": "Stockholm", "time_sensitivity": "low", "search_query": "Nimbus Cloud headquarters", "why_time_sensitive": "companies relocate"},
		answer: "Nimbus Cloud is headquartered in Stockholm, Sweden.",
		results: []search.Result{
			{Title: "About — Nimbus Cloud", URL: "https://nimbuscloud.example/about", PublishedDate: "2026-08-19", Score: 0.9, Content: "Headquartered in Stockholm with offices in Berlin and Toronto."},
		},
		judgment: map[string]any{"verdict": "current", "current_value": "Stockholm", "confidence": 0.9, "explanation": "The company's About page says it is headquartered in Stockholm [1].", "as_of": "2026-08-19",
			"sources": []map[string]any{{"index": 1, "stance": "supports", "official": true}}},
	},
	{
		claim:  map[string]string{"text": "providers must store EU customer data in the EU and report breaches within 72 hours", "category": "regulation", "entity": "Aurora Data Act", "attribute": "data residency and breach reporting", "claimed_value": "EU storage, 72-hour breach notice", "time_sensitivity": "medium", "search_query": "Aurora Data Act breach notification deadline data residency", "why_time_sensitive": "regulations get amended"},
		answer: "The Aurora Data Act requires EU data residency and breach notification within 72 hours.",
		results: []search.Result{
			{Title: "Aurora Data Act — obligations for providers", URL: "https://eu-data-authority.example/aurora-act/obligations", PublishedDate: "2026-06-30", Score: 0.91, Content: "Article 12: customer data of EU residents must be stored within the Union. Article 19: personal data breaches must be reported to the authority within 72 hours."},
			{Title: "Aurora Act explained", URL: "https://lawblog.example/aurora-act-explained", PublishedDate: "2026-02-11", Score: 0.74, Content: "Providers have 72 hours to report a breach, and EU data has to stay in the EU."},
		},
		judgment: map[string]any{"verdict": "current", "current_value": "EU storage required; breaches reported within 72 hours", "confidence": 0.88, "explanation": "The regulator's guidance (June 2026) confirms both the EU residency rule and the 72-hour breach notice [1], consistent with [2].", "as_of": "2026-06-30",
			"sources": []map[string]any{{"index": 1, "stance": "supports", "official": true}, {"index": 2, "stance": "supports", "official": false}}},
	},
}

// LLM fakes the extractor and judge.
type LLM struct {
	Delay time.Duration
	Now   func() time.Time
}

func (d *LLM) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := sleep(ctx, d.Delay); err != nil {
		return llm.Response{}, err
	}
	meta := llm.CallMeta{Purpose: req.Purpose, Provider: "demo", Model: "fixture-" + string(req.Purpose), LatencyMs: d.Delay.Milliseconds()}
	input := ""
	if len(req.Messages) > 0 {
		input = req.Messages[len(req.Messages)-1].Content
	}
	var out any
	switch req.Purpose {
	case llm.PurposeExtract:
		out = map[string]any{"claims": extractClaims(input)}
	case llm.PurposeJudge:
		out = judge(input)
	default:
		return llm.Response{}, fmt.Errorf("demo: unsupported purpose %q", req.Purpose)
	}
	b, _ := json.Marshal(out)
	meta.PromptTokens, meta.CompletionTokens = len(input)/4, len(b)/4
	return llm.Response{Text: string(b), Meta: meta}, nil
}

func extractClaims(input string) []map[string]string {
	var claims []map[string]string
	if strings.Contains(input, "Nimbus") {
		flat := squash(input)
		for _, f := range fixtures {
			if strings.Contains(flat, squash(f.claim["text"])) {
				claims = append(claims, f.claim)
			}
		}
		if len(claims) > 0 {
			return claims
		}
	}
	return heuristicClaims(input)
}

var (
	sentenceRe  = regexp.MustCompile(`[^.!?\n]+[.!?]?`)
	softBreakRe = regexp.MustCompile(`([^\n])\n([^\n])`)
	signalRe    = regexp.MustCompile(`(?i)(\$|€|£|\d|\bv\d|\bversion\b|\bceo\b|\bprice|\bcost|\bplan\b|\blaw\b|\bact\b|\bregulat|\blatest\b|\bcurrent|\breleased?\b|\bper month\b)`)
)

// heuristicClaims picks sentences that look time-sensitive, for texts without fixtures.
func heuristicClaims(input string) []map[string]string {
	if i := strings.Index(input, "<answer>"); i >= 0 {
		input = input[i+len("<answer>"):]
	}
	input = strings.TrimSuffix(strings.TrimSpace(input), "</answer>")
	// Text from PDFs breaks lines mid-sentence; join single line breaks.
	input = softBreakRe.ReplaceAllString(softBreakRe.ReplaceAllString(input, "$1 $2"), "$1 $2")
	var out []map[string]string
	for _, s := range sentenceRe.FindAllString(input, -1) {
		s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "*#-"))
		if len(s) < 12 || !signalRe.MatchString(s) {
			continue
		}
		out = append(out, map[string]string{
			"text": s, "category": guessCategory(s), "time_sensitivity": "medium",
			"search_query": s, "why_time_sensitive": "contains a current-state detail",
		})
		if len(out) == 8 {
			break
		}
	}
	return out
}

func guessCategory(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.ContainsAny(l, "$€£") || strings.Contains(l, "price") || strings.Contains(l, "cost"):
		return "pricing"
	case strings.Contains(l, "version") || strings.Contains(l, "api") || strings.Contains(l, "sdk"):
		return "api_version"
	case strings.Contains(l, "ceo") || strings.Contains(l, "founder") || strings.Contains(l, "president"):
		return "leadership"
	case strings.Contains(l, "law") || strings.Contains(l, " act") || strings.Contains(l, "regulat"):
		return "regulation"
	}
	return "other"
}

func judge(input string) map[string]any {
	flat := squash(input)
	for _, f := range fixtures {
		if strings.Contains(flat, squash(fmt.Sprintf("Claim: %q", f.claim["text"]))) {
			return f.judgment
		}
	}
	return map[string]any{
		"verdict":     "unverifiable",
		"confidence":  0,
		"explanation": "Demo mode: live verification is off. Add TAVILY_API_KEY and a model provider key to check this claim against the web.",
	}
}

// Search fakes Tavily with the fixture results.
type Search struct {
	Delay time.Duration
}

func (d *Search) Search(ctx context.Context, query string) (*search.Response, error) {
	if err := sleep(ctx, d.Delay); err != nil {
		return nil, err
	}
	for _, f := range fixtures {
		if f.claim["search_query"] == query {
			return &search.Response{Query: query, Answer: f.answer, Results: f.results, ResponseTimeMs: d.Delay.Milliseconds()}, nil
		}
	}
	return &search.Response{Query: query, ResponseTimeMs: d.Delay.Milliseconds()}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// squash makes fixture matching ignore line breaks and spacing, including the escaped "\n"
// that %q writes into prompts, so the sample also matches after a trip through a PDF.
func squash(s string) string {
	s = strings.NewReplacer(`\n`, " ", `\t`, " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
