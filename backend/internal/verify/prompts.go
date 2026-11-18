package verify

import (
	"fmt"
	"strings"
	"time"
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
- "official_domain": the domain of the entity's own website, or of the authority responsible for the fact (e.g. "python.org", "stripe.com", "eur-lex.europa.eu", "sec.gov"), only if you are confident it exists; otherwise "". No scheme, no path.

Return at most %d claims, the most time-sensitive first. If there are none, return an empty list.
Reply with JSON only, no prose:
{"claims":[{"text":"","category":"","entity":"","attribute":"","claimed_value":"","time_sensitivity":"","search_query":"","why_time_sensitive":"","official_domain":""}]}`,
		now.Format("January 2, 2006"), maxClaims)
}

func extractUserPrompt(answer string) string {
	return "AI-generated answer to analyse:\n<answer>\n" + answer + "\n</answer>"
}

func judgeSystemPrompt(now time.Time, quotes bool) string {
	quoteRule, quoteField := "", ""
	if quotes {
		quoteRule = `
- For every source with stance "supports" or "contradicts", copy into "quote" the exact passage (one sentence, at most 200 characters, verbatim, no paraphrase) from that source that shows the stance. A stance without a verbatim quote is discarded.`
		quoteField = `,"quote":"verbatim passage"`
	}
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
- The search engine summary is a hint and can be wrong; check it against the sources.%s
- If your verdict is "unverifiable" or your confidence is below 0.6, put in "follow_up_query" the web search query most likely to find the decisive evidence (e.g. the official page that states the value). Otherwise leave it empty.

Reply with JSON only, no prose:
{"verdict":"current|outdated|incorrect|unverifiable",
 "current_value":"the value today according to the evidence, empty if unknown",
 "confidence":0.0,
 "explanation":"one or two sentences that cite sources as [1], [2]",
 "corrected_text":"only for outdated/incorrect: the claim text rewritten so it is accurate today, same language, tone and length, changing as little as possible; otherwise empty",
 "as_of":"YYYY-MM-DD date of the newest evidence you relied on, empty if unknown",
 "follow_up_query":"",
 "sources":[{"index":1,"stance":"supports|contradicts|neutral","official":false%s}]}
"stance" is relative to the claim as written. "official" is true when the source is published by the entity itself or by the authority responsible for the fact.`,
		now.Format("January 2, 2006"), quoteRule, quoteField)
}

const (
	maxSnippet     = 1200 // characters of a search result shown to the judge
	maxFetchedPage = 3500 // characters of a fetched official page (focused on the claim's keywords)
)

func judgeUserPrompt(c Claim, ev *evidence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Claim: %q\n", c.Text)
	fmt.Fprintf(&b, "Category: %s\nEntity: %s\nAttribute: %s\nClaimed value: %s\n\n", c.Category, c.Entity, c.Attribute, c.ClaimedValue)
	fmt.Fprintf(&b, "Web search queries: %s\n", strings.Join(ev.queries, " | "))
	if ev.answer != "" {
		fmt.Fprintf(&b, "Search engine summary (may be wrong): %s\n", ev.answer)
	}
	b.WriteString("\nSources:\n")
	if len(ev.items) == 0 {
		b.WriteString("(no results)\n")
	}
	for i, it := range ev.items {
		date := it.res.PublishedDate
		if date == "" {
			date = "unknown"
		}
		note := ""
		if it.fetched {
			note = " (full page, passages most relevant to the claim)"
		}
		fmt.Fprintf(&b, "[%d] %s\nURL: %s\nPublished: %s%s\n%s\n\n", i+1, it.res.Title, it.res.URL, date, note, it.shown)
	}
	return b.String()
}
