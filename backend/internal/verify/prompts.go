package verify

import (
	"fmt"
	"strings"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/search"
)

func extractSystemPrompt(now time.Time, maxClaims int) string {
	return fmt.Sprintf(`You extract time-sensitive factual claims from AI-generated answers for a freshness checker. Today is %s.

A claim is time-sensitive when it could have become false since the answer was written. Categories:
- pricing: prices, plans, fees, free tiers, quotas, rate limits
- api_version: software, API, SDK or model versions; endpoints, parameters, deprecations, support status
- regulation: laws, regulations, compliance rules, tax rates, legal deadlines
- leadership: CEOs, executives, founders' current roles, board, ownership
- company: headcount, headquarters, funding, valuation, acquisitions, products offered, availability
- statistic: market share, user counts, rankings, benchmarks, records
- other: anything else that is current-state and could change

Skip claims that cannot go stale: definitions, explanations, math, opinions, and historical facts tied to a fixed past date ("X was founded in 2015").

For each claim:
- "text": copy the exact characters from the answer: one contiguous span, the shortest that fully states the claim (a clause or sentence, at most 300 characters). Never paraphrase, translate, or join separate spans.
- "entity": who or what the claim is about; "attribute": what property; "claimed_value": the value the answer states.
- "time_sensitivity": "high" if it changes within months (prices, versions), "medium" within a year or two, "low" rarely.
- "search_query": a web search query that would surface the current authoritative value, naming the entity and attribute. Keep it short; do not add years unless the claim is about a specific year.
- "why_time_sensitive": a few words.

Return at most %d claims, the most time-sensitive first. If there are none, return an empty list.
Reply with JSON only, no prose:
{"claims":[{"text":"","category":"","entity":"","attribute":"","claimed_value":"","time_sensitivity":"","search_query":"","why_time_sensitive":""}]}`,
		now.Format("January 2, 2006"), maxClaims)
}

func extractUserPrompt(answer string) string {
	return "AI-generated answer to analyse:\n<answer>\n" + answer + "\n</answer>"
}

func judgeSystemPrompt(now time.Time) string {
	return fmt.Sprintf(`You check whether a claim taken from an AI-generated answer is still accurate today, using only the web evidence provided. Today is %s.

Verdicts:
- "current": the evidence shows the claim is accurate today.
- "outdated": the claim was accurate at some point, but the evidence shows it has since changed (new price, version, law, person, number).
- "incorrect": the evidence shows the claim was never accurate.
- "unverifiable": the evidence is missing, off-topic, or conflicting without a clear winner.

Rules:
- Base the verdict on the sources, not on your own memory. Your training data may itself be stale.
- Prefer recent and official sources (the organisation's own site or docs, government or regulator sites) over aggregators, forums and old news. When sources conflict, trust the most recent authoritative one.
- Note publish dates. A source from before a change can support an old value without proving it is current.
- The search engine summary is a hint and can be wrong; check it against the sources.

Reply with JSON only, no prose:
{"verdict":"current|outdated|incorrect|unverifiable",
 "current_value":"the value today according to the evidence, empty if unknown",
 "confidence":0.0,
 "explanation":"one or two sentences that cite sources as [1], [2]",
 "corrected_text":"only for outdated/incorrect: the claim text rewritten so it is accurate today, same language, tone and length, changing as little as possible; otherwise empty",
 "as_of":"YYYY-MM-DD date of the newest evidence you relied on, empty if unknown",
 "sources":[{"index":1,"stance":"supports|contradicts|neutral","official":false}]}
"stance" is relative to the claim as written. "official" is true when the source is published by the entity itself or by the authority responsible for the fact.`,
		now.Format("January 2, 2006"))
}

const maxSnippet = 1200

func judgeUserPrompt(c Claim, res *search.Response) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Claim: %q\n", c.Text)
	fmt.Fprintf(&b, "Category: %s\nEntity: %s\nAttribute: %s\nClaimed value: %s\n\n", c.Category, c.Entity, c.Attribute, c.ClaimedValue)
	fmt.Fprintf(&b, "Web search query: %s\n", res.Query)
	if res.Answer != "" {
		fmt.Fprintf(&b, "Search engine summary (may be wrong): %s\n", res.Answer)
	}
	b.WriteString("\nSources:\n")
	if len(res.Results) == 0 {
		b.WriteString("(no results)\n")
	}
	for i, r := range res.Results {
		date := r.PublishedDate
		if date == "" {
			date = "unknown"
		}
		snippet := truncateRunes(r.Content, maxSnippet)
		fmt.Fprintf(&b, "[%d] %s\nURL: %s\nPublished: %s\n%s\n\n", i+1, r.Title, r.URL, date, snippet)
	}
	return b.String()
}
