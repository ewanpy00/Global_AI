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
		claim:  map[string]string{"text": "Its Pro plan costs $12 per user per month and includes 50 GB of storage per user", "category": "pricing", "entity": "Nimbus Cloud", "attribute": "Pro plan price and storage", "claimed_value": "$12 per user per month, 50 GB", "time_sensitivity": "high", "search_query": "Nimbus Cloud Pro plan price per user", "why_time_sensitive": "SaaS prices change often", "official_domain": "nimbuscloud.example"},
		answer: "Nimbus Cloud Pro costs $15 per user per month with 100 GB of storage per user.",
		results: []search.Result{
			{Title: "Pricing — Nimbus Cloud", URL: "https://nimbuscloud.example/pricing", PublishedDate: "2026-10-02", Score: 0.94, Content: "Free: up to 3 users, 5 GB each. Pro: $15 per user / month, 100 GB storage per user, priority support. Business: $29 per user / month."},
			{Title: "Nimbus Cloud raises Pro prices, doubles storage", URL: "https://techwire.example/2026/07/nimbus-pro-price", PublishedDate: "2026-07-14", Score: 0.88, Content: "Starting August 1, 2026 the Pro tier moves from $12 to $15 per user per month; included storage grows from 50 GB to 100 GB."},
			{Title: "Nimbus Cloud review (2025)", URL: "https://saasreviews.example/nimbus-cloud", PublishedDate: "2025-03-10", Score: 0.71, Content: "The Pro plan is $12 per user per month and comes with 50 GB of storage."},
		},
		judgment: map[string]any{"verdict": "outdated", "current_value": "$15 per user per month, 100 GB per user (since Aug 1, 2026)", "confidence": 0.93, "explanation": "The official pricing page lists Pro at $15 per user per month with 100 GB [1]; the change from $12 / 50 GB took effect on August 1, 2026 [2]. Only an older review still shows $12 [3].", "corrected_text": "Its Pro plan costs $15 per user per month and includes 100 GB of storage per user", "as_of": "2026-10-02",
			"sources": []map[string]any{{"index": 1, "stance": "contradicts", "official": true, "quote": "Pro: $15 per user / month, 100 GB storage per user, priority support."}, {"index": 2, "stance": "contradicts", "official": false, "quote": "Starting August 1, 2026 the Pro tier moves from $12 to $15 per user per month"}, {"index": 3, "stance": "supports", "official": false, "quote": "The Pro plan is $12 per user per month and comes with 50 GB of storage."}}},
	},
	{
		claim:  map[string]string{"text": "A free tier is available for up to 3 users", "category": "pricing", "entity": "Nimbus Cloud", "attribute": "free tier limit", "claimed_value": "up to 3 users", "time_sensitivity": "high", "search_query": "Nimbus Cloud free tier users limit", "why_time_sensitive": "free tiers change", "official_domain": "nimbuscloud.example"},
		answer: "The free plan supports up to 3 users.",
		results: []search.Result{
			{Title: "Pricing — Nimbus Cloud", URL: "https://nimbuscloud.example/pricing", PublishedDate: "2026-10-02", Score: 0.9, Content: "Free: up to 3 users, 5 GB each. Pro: $15 per user / month."},
		},
		judgment: map[string]any{"verdict": "current", "current_value": "up to 3 users", "confidence": 0.9, "explanation": "The official pricing page still offers a free plan for up to 3 users [1].", "as_of": "2026-10-02",
			"sources": []map[string]any{{"index": 1, "stance": "supports", "official": true, "quote": "Free: up to 3 users, 5 GB each."}}},
	},
	{
		claim:  map[string]string{"text": "The current stable version of the Nimbus SDK is 3.2", "category": "api_version", "entity": "Nimbus SDK", "attribute": "latest stable version", "claimed_value": "3.2", "time_sensitivity": "high", "search_query": "Nimbus SDK latest stable release", "why_time_sensitive": "SDKs ship new versions", "official_domain": "nimbuscloud.example"},
		answer: "The latest Nimbus SDK release is 4.1.0.",
		results: []search.Result{
			{Title: "Changelog — Nimbus Docs", URL: "https://docs.nimbuscloud.example/changelog", PublishedDate: "2026-09-22", Score: 0.95, Content: "4.1.0 (2026-09-22): streaming uploads, Python 3.14 support. 4.0.0 (2026-04-08): new async client, v3 API by default. 3.2.4 (2025-11-02): final 3.x maintenance release."},
		},
		judgment: map[string]any{"verdict": "outdated", "current_value": "4.1 (released Sep 22, 2026)", "confidence": 0.95, "explanation": "The official changelog shows 4.1.0 released on Sep 22, 2026; the 3.x line ended with 3.2.4 in Nov 2025 [1].", "corrected_text": "The current stable version of the Nimbus SDK is 4.1", "as_of": "2026-09-22",
			"sources": []map[string]any{{"index": 1, "stance": "contradicts", "official": true, "quote": "4.1.0 (2026-09-22): streaming uploads, Python 3.14 support."}}},
	},
	{
		claim:  map[string]string{"text": "the v2 REST API is still supported", "category": "api_version", "entity": "Nimbus REST API", "attribute": "v2 support status", "claimed_value": "supported", "time_sensitivity": "high", "search_query": "Nimbus Cloud v2 REST API deprecation", "why_time_sensitive": "API versions get retired", "official_domain": "nimbuscloud.example"},
		answer: "Nimbus retired the v2 REST API on March 31, 2026.",
		results: []search.Result{
			{Title: "API v2 end of life", URL: "https://docs.nimbuscloud.example/api/v2-sunset", PublishedDate: "2026-04-01", Score: 0.93, Content: "API v2 was shut down on March 31, 2026. Requests to /v2 now return 410 Gone. Migrate to v3."},
			{Title: "Forum: is v2 still working?", URL: "https://community.nimbuscloud.example/t/v2-still-working", PublishedDate: "2025-12-05", Score: 0.61, Content: "Yes, v2 still works for now but it is deprecated."},
		},
		judgment: map[string]any{"verdict": "outdated", "current_value": "Retired on March 31, 2026; v3 is required", "confidence": 0.94, "explanation": "The official docs say API v2 was shut down on March 31, 2026 and now returns 410 Gone [1]. A December 2025 forum post predates the shutdown [2].", "corrected_text": "the v2 REST API was retired on March 31, 2026 (use v3)", "as_of": "2026-04-01",
			"sources": []map[string]any{{"index": 1, "stance": "contradicts", "official": true, "quote": "API v2 was shut down on March 31, 2026."}, {"index": 2, "stance": "supports", "official": false, "quote": "Yes, v2 still works for now but it is deprecated."}}},
	},
	{
		claim:  map[string]string{"text": "The company is led by CEO Maria Lindqvist", "category": "leadership", "entity": "Nimbus Cloud", "attribute": "CEO", "claimed_value": "Maria Lindqvist", "time_sensitivity": "medium", "search_query": "Nimbus Cloud CEO", "why_time_sensitive": "executives change", "official_domain": "nimbuscloud.example"},
		answer: "Daniel Okafor is CEO of Nimbus Cloud; Maria Lindqvist is executive chair.",
		results: []search.Result{
			{Title: "Leadership — Nimbus Cloud", URL: "https://nimbuscloud.example/about/leadership", PublishedDate: "2026-08-19", Score: 0.92, Content: "Daniel Okafor, Chief Executive Officer. Maria Lindqvist, Founder & Executive Chair."},
			{Title: "Nimbus founder hands CEO role to Okafor", URL: "https://nordicbusiness.example/2026/05/nimbus-ceo", PublishedDate: "2026-05-12", Score: 0.86, Content: "Founder Maria Lindqvist moves to executive chair; former COO Daniel Okafor becomes CEO effective May 12."},
		},
		judgment: map[string]any{"verdict": "outdated", "current_value": "Daniel Okafor (CEO since May 2026)", "confidence": 0.92, "explanation": "The company's leadership page names Daniel Okafor as CEO [1]; Lindqvist became executive chair in May 2026 [2].", "corrected_text": "The company is led by CEO Daniel Okafor", "as_of": "2026-08-19",
			"sources": []map[string]any{{"index": 1, "stance": "contradicts", "official": true, "quote": "Daniel Okafor, Chief Executive Officer."}, {"index": 2, "stance": "contradicts", "official": false, "quote": "former COO Daniel Okafor becomes CEO effective May 12."}}},
	},
	{
		claim:  map[string]string{"text": "Nimbus has around 400 employees", "category": "company", "entity": "Nimbus Cloud", "attribute": "headcount", "claimed_value": "about 400", "time_sensitivity": "medium", "search_query": "Nimbus Cloud number of employees", "why_time_sensitive": "headcount changes", "official_domain": "nimbuscloud.example"},
		answer: "Estimates vary.",
		results: []search.Result{
			{Title: "Nimbus Cloud — company profile", URL: "https://companydb.example/nimbus-cloud", Score: 0.66, Content: "Employees: 250-500. Founded 2015. Stockholm, Sweden."},
			{Title: "Nimbus Cloud hiring push (2024)", URL: "https://nordicbusiness.example/2024/02/nimbus-hiring", PublishedDate: "2024-02-20", Score: 0.58, Content: "The company plans to grow from 320 to about 400 people by the end of the year."},
		},
		judgment: map[string]any{"verdict": "unverifiable", "confidence": 0.4, "explanation": "Only an undated range (250–500) [1] and a 2024 hiring plan [2] mention headcount; there is no recent authoritative figure.", "follow_up_query": "Nimbus Cloud annual report 2026 number of employees",
			"sources": []map[string]any{{"index": 1, "stance": "neutral", "official": false}, {"index": 2, "stance": "neutral", "official": false}}},
	},
	{
		claim:  map[string]string{"text": "is headquartered in Stockholm", "category": "company", "entity": "Nimbus Cloud", "attribute": "headquarters", "claimed_value": "Stockholm", "time_sensitivity": "low", "search_query": "Nimbus Cloud headquarters", "why_time_sensitive": "companies relocate", "official_domain": "nimbuscloud.example"},
		answer: "Nimbus Cloud is headquartered in Stockholm, Sweden.",
		results: []search.Result{
			{Title: "About — Nimbus Cloud", URL: "https://nimbuscloud.example/about", PublishedDate: "2026-08-19", Score: 0.9, Content: "Headquartered in Stockholm with offices in Berlin and Toronto."},
		},
		judgment: map[string]any{"verdict": "current", "current_value": "Stockholm", "confidence": 0.9, "explanation": "The company's About page says it is headquartered in Stockholm [1].", "as_of": "2026-08-19",
			"sources": []map[string]any{{"index": 1, "stance": "supports", "official": true, "quote": "Headquartered in Stockholm with offices in Berlin and Toronto."}}},
	},
	{
		claim:  map[string]string{"text": "providers must store EU customer data in the EU and report breaches within 72 hours", "category": "regulation", "entity": "Aurora Data Act", "attribute": "data residency and breach reporting", "claimed_value": "EU storage, 72-hour breach notice", "time_sensitivity": "medium", "search_query": "Aurora Data Act breach notification deadline data residency", "why_time_sensitive": "regulations get amended", "official_domain": "eu-data-authority.example"},
		answer: "The Aurora Data Act requires EU data residency and breach notification within 72 hours.",
		results: []search.Result{
			{Title: "Aurora Data Act — obligations for providers", URL: "https://eu-data-authority.example/aurora-act/obligations", PublishedDate: "2026-06-30", Score: 0.91, Content: "Article 12: customer data of EU residents must be stored within the Union. Article 19: personal data breaches must be reported to the authority within 72 hours."},
			{Title: "Aurora Act explained", URL: "https://lawblog.example/aurora-act-explained", PublishedDate: "2026-02-11", Score: 0.74, Content: "Providers have 72 hours to report a breach, and EU data has to stay in the EU."},
		},
		judgment: map[string]any{"verdict": "current", "current_value": "EU storage required; breaches reported within 72 hours", "confidence": 0.88, "explanation": "The regulator's guidance (June 2026) confirms both the EU residency rule and the 72-hour breach notice [1], consistent with [2].", "as_of": "2026-06-30",
			"sources": []map[string]any{{"index": 1, "stance": "supports", "official": true, "quote": "Article 19: personal data breaches must be reported to the authority within 72 hours."}, {"index": 2, "stance": "supports", "official": false, "quote": "Providers have 72 hours to report a breach, and EU data has to stay in the EU."}}},
	},
}

// freshFixtures are up-to-date versions of some claims, so the Staleness Arena has a model that
// knows the current facts. They reuse the stale fixtures' search queries and results.
var freshFixtures = []fixture{
	{
		claim: map[string]string{"text": "Its Pro plan costs $15 per user per month and includes 100 GB of storage per user", "category": "pricing", "entity": "Nimbus Cloud", "attribute": "Pro plan price and storage", "claimed_value": "$15 per user per month, 100 GB", "time_sensitivity": "high", "search_query": "Nimbus Cloud Pro plan price per user", "why_time_sensitive": "SaaS prices change often", "official_domain": "nimbuscloud.example"},
		judgment: map[string]any{"verdict": "current", "current_value": "$15 per user per month, 100 GB", "confidence": 0.93, "explanation": "The official pricing page lists Pro at $15 per user per month with 100 GB per user [1], in effect since August 1, 2026 [2].", "as_of": "2026-10-02",
			"sources": []map[string]any{{"index": 1, "stance": "supports", "official": true, "quote": "Pro: $15 per user / month, 100 GB storage per user, priority support."}, {"index": 2, "stance": "supports", "official": false, "quote": "Starting August 1, 2026 the Pro tier moves from $12 to $15 per user per month"}}},
	},
	{
		claim: map[string]string{"text": "The current stable version of the Nimbus SDK is 4.1", "category": "api_version", "entity": "Nimbus SDK", "attribute": "latest stable version", "claimed_value": "4.1", "time_sensitivity": "high", "search_query": "Nimbus SDK latest stable release", "why_time_sensitive": "SDKs ship new versions", "official_domain": "nimbuscloud.example"},
		judgment: map[string]any{"verdict": "current", "current_value": "4.1 (released Sep 22, 2026)", "confidence": 0.95, "explanation": "The official changelog shows 4.1.0 as the newest release [1].", "as_of": "2026-09-22",
			"sources": []map[string]any{{"index": 1, "stance": "supports", "official": true, "quote": "4.1.0 (2026-09-22): streaming uploads, Python 3.14 support."}}},
	},
	{
		claim: map[string]string{"text": "The company is led by CEO Daniel Okafor", "category": "leadership", "entity": "Nimbus Cloud", "attribute": "CEO", "claimed_value": "Daniel Okafor", "time_sensitivity": "medium", "search_query": "Nimbus Cloud CEO", "why_time_sensitive": "executives change", "official_domain": "nimbuscloud.example"},
		judgment: map[string]any{"verdict": "current", "current_value": "Daniel Okafor (CEO since May 2026)", "confidence": 0.92, "explanation": "The leadership page names Daniel Okafor as CEO [1], since May 2026 [2].", "as_of": "2026-08-19",
			"sources": []map[string]any{{"index": 1, "stance": "supports", "official": true, "quote": "Daniel Okafor, Chief Executive Officer."}, {"index": 2, "stance": "supports", "official": false, "quote": "former COO Daniel Okafor becomes CEO effective May 12."}}},
	},
	{
		claim: map[string]string{"text": "the v2 REST API was retired in March 2026", "category": "api_version", "entity": "Nimbus REST API", "attribute": "v2 support status", "claimed_value": "retired March 2026", "time_sensitivity": "high", "search_query": "Nimbus Cloud v2 REST API deprecation", "why_time_sensitive": "API versions get retired", "official_domain": "nimbuscloud.example"},
		judgment: map[string]any{"verdict": "current", "current_value": "Retired on March 31, 2026", "confidence": 0.94, "explanation": "The official docs say API v2 was shut down on March 31, 2026 [1].", "as_of": "2026-04-01",
			"sources": []map[string]any{{"index": 1, "stance": "supports", "official": true, "quote": "API v2 was shut down on March 31, 2026."}}},
	},
}

// The follow-up search for the headcount claim finds the annual report the first search missed.
const followUpQuery = "Nimbus Cloud annual report 2026 number of employees"

var followUpResult = search.Result{
	Title: "Nimbus Cloud annual report 2026", URL: "https://nimbuscloud.example/investors/annual-report-2026", PublishedDate: "2026-03-30", Score: 0.89,
	Content: "Highlights of the year. At year end Nimbus Cloud employed 612 people across Stockholm, Berlin and Toronto. Revenue grew 41%.",
}

var followUpJudgment = map[string]any{"verdict": "outdated", "current_value": "612 employees (end of 2025, annual report)", "confidence": 0.86,
	"explanation": "The company's 2026 annual report gives 612 employees at year end [3]; the ~400 figure comes from a 2024 hiring plan [2].",
	"corrected_text": "Nimbus has around 610 employees", "as_of": "2026-03-30",
	"sources": []map[string]any{{"index": 2, "stance": "contradicts", "official": false, "quote": "The company plans to grow from 320 to about 400 people by the end of the year."}, {"index": 3, "stance": "contradicts", "official": true, "quote": "At year end Nimbus Cloud employed 612 people across Stockholm, Berlin and Toronto."}}}

// pages are the full texts Tavily Extract / Crawl return in demo mode.
var pages = map[string]string{
	"https://nimbuscloud.example/pricing": "Nimbus Cloud pricing\nSimple plans for teams of every size.\nFree: up to 3 users, 5 GB each.\nPro: $15 per user / month, 100 GB storage per user, priority support.\nBusiness: $29 per user / month, 1 TB pooled storage, SSO and audit logs.\nAll prices exclude VAT. Annual billing saves 15%.\nFAQ: Can I change plans at any time? Yes, upgrades apply immediately.",
	"https://docs.nimbuscloud.example/changelog": "Nimbus SDK changelog\n4.1.0 (2026-09-22): streaming uploads, Python 3.14 support.\n4.0.0 (2026-04-08): new async client, v3 API by default.\n3.2.4 (2025-11-02): final 3.x maintenance release.\nOlder releases are listed in the archive.",
	"https://eu-data-authority.example/aurora-act/obligations": "Aurora Data Act: obligations for providers\nArticle 12: customer data of EU residents must be stored within the Union.\nArticle 19: personal data breaches must be reported to the authority within 72 hours.\nArticle 23: fines of up to 4% of annual turnover.",
}

// sitePages is the demo site for the audit: a few pages of the fictional company's own website,
// some of which still repeat old facts.
var sitePages = []search.Page{
	{URL: "https://nimbuscloud.example/blog/choosing-team-storage", Content: "Choosing storage for your team\nNimbus Cloud is a solid pick for small teams. Its Pro plan costs $12 per user per month and includes 50 GB of storage per user. A free tier is available for up to 3 users."},
	{URL: "https://nimbuscloud.example/docs/getting-started", Content: "Getting started with the Nimbus SDK\nThe current stable version of the Nimbus SDK is 3.2, and the v2 REST API is still supported, so older integrations keep working. Install it with pip."},
	{URL: "https://nimbuscloud.example/about", Content: "About Nimbus Cloud\nThe company is led by CEO Daniel Okafor. Nimbus Cloud is headquartered in Stockholm with offices in Berlin and Toronto."},
	{URL: "https://nimbuscloud.example/legal/compliance", Content: "Compliance\nUnder the Aurora Data Act, providers must store EU customer data in the EU and report breaches within 72 hours. Nimbus Cloud meets both requirements."},
	{URL: "https://nimbuscloud.example/careers", Content: "Careers at Nimbus\nNimbus has around 400 employees and is hiring engineers in Stockholm and Berlin."},
}

// ArenaModels are the demo contestants: three fictional models with different knowledge cut-offs.
var ArenaModels = []string{"demo/atlas-2024", "demo/orion-2025", "demo/nova-2026"}

var arenaAnswers = map[string]string{
	"atlas-2024": SampleAnswer,
	"orion-2025": `Nimbus Cloud suits small teams well.

**Pricing.** Its Pro plan costs $12 per user per month and includes 50 GB of storage per user. A free tier is available for up to 3 users.

**Developer platform.** The current stable version of the Nimbus SDK is 4.1, and the v2 REST API is still supported.

**Company.** The company is led by CEO Daniel Okafor. It is headquartered in Stockholm.`,
	"nova-2026": `Nimbus Cloud is a good fit for small teams.

**Pricing.** Its Pro plan costs $15 per user per month and includes 100 GB of storage per user. A free tier is available for up to 3 users.

**Developer platform.** The current stable version of the Nimbus SDK is 4.1; the v2 REST API was retired in March 2026, so use v3.

**Company.** The company is led by CEO Daniel Okafor. Nimbus Cloud is headquartered in Stockholm.

**Compliance.** Under the Aurora Data Act, providers must store EU customer data in the EU and report breaches within 72 hours.`,
}

func allFixtures() []fixture { return append(append([]fixture(nil), fixtures...), freshFixtures...) }

// LLM fakes the extractor, the judges and the arena contestants.
type LLM struct {
	Delay time.Duration
	Now   func() time.Time
}

func (d *LLM) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := sleep(ctx, d.Delay); err != nil {
		return llm.Response{}, err
	}
	model := "fixture-" + string(req.Purpose)
	if req.Model != "" {
		model = strings.TrimPrefix(req.Model, "demo/")
	}
	meta := llm.CallMeta{Purpose: req.Purpose, Provider: "demo", Model: model, LatencyMs: d.Delay.Milliseconds()}
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
	case llm.PurposeJury:
		j := judge(input)
		jury := map[string]any{}
		for k, v := range j {
			jury[k] = v
		}
		if c, ok := jury["confidence"].(float64); ok && c > 0.1 {
			jury["confidence"] = c - 0.06
		}
		out = jury
	case llm.PurposeAnswer:
		text, ok := arenaAnswers[model]
		if !ok {
			text = SampleAnswer
		}
		meta.PromptTokens, meta.CompletionTokens = len(input)/4, len(text)/4
		return llm.Response{Text: text, Meta: meta}, nil
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
		for _, f := range allFixtures() {
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
	for _, f := range allFixtures() {
		if strings.Contains(flat, squash(fmt.Sprintf("Claim: %q", f.claim["text"]))) {
			if f.claim["attribute"] == "headcount" && strings.Contains(input, followUpResult.URL) {
				return followUpJudgment
			}
			return f.judgment
		}
	}
	return map[string]any{
		"verdict":     "unverifiable",
		"confidence":  0,
		"explanation": "Demo mode: live verification is off. Add TAVILY_API_KEY and a model provider key to check this claim against the web.",
	}
}

// Search fakes Tavily with the fixture results. Domain-restricted queries return only the
// fixture results on that domain; news queries only dated results.
type Search struct {
	Delay time.Duration
}

func (d *Search) Search(ctx context.Context, q search.Query) (*search.Response, error) {
	if err := sleep(ctx, d.Delay); err != nil {
		return nil, err
	}
	resp := &search.Response{Query: q.Text, ResponseTimeMs: d.Delay.Milliseconds()}
	if q.Text == followUpQuery {
		resp.Results = []search.Result{followUpResult}
		return resp, nil
	}
	for _, f := range fixtures {
		if f.claim["search_query"] != q.Text {
			continue
		}
		for _, r := range f.results {
			if len(q.IncludeDomains) > 0 && !onDomains(r.URL, q.IncludeDomains) {
				continue
			}
			if q.Topic == "news" && r.PublishedDate == "" {
				continue
			}
			resp.Results = append(resp.Results, r)
		}
		if len(q.IncludeDomains) == 0 && q.Topic == "" {
			resp.Answer = f.answer
		}
		break
	}
	return resp, nil
}

func onDomains(u string, domains []string) bool {
	host := u
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// Extract fakes Tavily Extract with the bundled page texts.
func (d *Search) Extract(ctx context.Context, urls []string) (*search.PagesResponse, error) {
	if err := sleep(ctx, d.Delay); err != nil {
		return nil, err
	}
	out := &search.PagesResponse{ResponseTimeMs: d.Delay.Milliseconds()}
	for _, u := range urls {
		if p, ok := pages[u]; ok {
			out.Pages = append(out.Pages, search.Page{URL: u, Content: p})
		} else {
			out.Failed++
		}
	}
	return out, nil
}

// Crawl fakes Tavily Crawl: any URL returns the fictional company's site.
func (d *Search) Crawl(ctx context.Context, url string, limit int) (*search.PagesResponse, error) {
	if err := sleep(ctx, 2*d.Delay); err != nil {
		return nil, err
	}
	ps := sitePages
	if limit > 0 && limit < len(ps) {
		ps = ps[:limit]
	}
	return &search.PagesResponse{Pages: append([]search.Page(nil), ps...), ResponseTimeMs: 2 * d.Delay.Milliseconds()}, nil
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
