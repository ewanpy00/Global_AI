// Package verify is the stale-answer detection pipeline:
// extract time-sensitive claims → search live sources (Tavily) → judge each claim (LLM via Bifrost) → score.
package verify

import "time"

type Category string

const (
	CatPricing    Category = "pricing"
	CatAPIVersion Category = "api_version"
	CatRegulation Category = "regulation"
	CatLeadership Category = "leadership"
	CatCompany    Category = "company"
	CatStatistic  Category = "statistic"
	CatOther      Category = "other"
)

func normalizeCategory(s string) Category {
	switch c := Category(s); c {
	case CatPricing, CatAPIVersion, CatRegulation, CatLeadership, CatCompany, CatStatistic:
		return c
	}
	return CatOther
}

type Sensitivity string

const (
	SensHigh   Sensitivity = "high"
	SensMedium Sensitivity = "medium"
	SensLow    Sensitivity = "low"
)

func normalizeSensitivity(s string) Sensitivity {
	switch v := Sensitivity(s); v {
	case SensHigh, SensMedium, SensLow:
		return v
	}
	return SensMedium
}

// Claim is one time-sensitive statement found in the answer.
type Claim struct {
	ID           string      `json:"id"`
	Text         string      `json:"text"` // verbatim span of the answer
	Category     Category    `json:"category"`
	Entity       string      `json:"entity"`
	Attribute    string      `json:"attribute"`
	ClaimedValue string      `json:"claimed_value"`
	Sensitivity  Sensitivity `json:"time_sensitivity"`
	SearchQuery  string      `json:"search_query"`
	Why          string      `json:"why_time_sensitive"`

	// Position of the span in the answer, in UTF-16 code units so the browser can slice
	// the string directly. Located is false when the model's quote could not be found.
	Start   int  `json:"start"`
	End     int  `json:"end"`
	Located bool `json:"located"`

	byteStart, byteEnd int
}

// Span returns the claim's byte offsets in the checked text (meaningful when Located).
func (c Claim) Span() (start, end int) { return c.byteStart, c.byteEnd }

type Verdict string

const (
	VerdictCurrent      Verdict = "current"      // evidence says it is still true
	VerdictOutdated     Verdict = "outdated"     // was true, has since changed
	VerdictIncorrect    Verdict = "incorrect"    // evidence says it was never true
	VerdictUnverifiable Verdict = "unverifiable" // evidence missing or conflicting
)

func normalizeVerdict(s string) Verdict {
	switch v := Verdict(s); v {
	case VerdictCurrent, VerdictOutdated, VerdictIncorrect, VerdictUnverifiable:
		return v
	}
	return VerdictUnverifiable
}

type Stance string

const (
	StanceSupports    Stance = "supports"
	StanceContradicts Stance = "contradicts"
	StanceNeutral     Stance = "neutral"
)

// Source is a web page used as evidence for one claim.
type Source struct {
	Index         int     `json:"index"`
	Title         string  `json:"title"`
	URL           string  `json:"url"`
	Domain        string  `json:"domain"`
	Snippet       string  `json:"snippet"`
	PublishedDate string  `json:"published_date,omitempty"`
	Relevance     float64 `json:"relevance"`
	Stance        Stance  `json:"stance"`
	Official      bool    `json:"official"`
}

type Label string

const (
	LabelFresh  Label = "fresh"
	LabelReview Label = "review"
	LabelStale  Label = "stale"
)

// Breakdown explains how a freshness score was computed. The UI shows it on hover.
type Breakdown struct {
	Verdict       Verdict `json:"verdict"`
	Confidence    float64 `json:"confidence"`
	Evidence      float64 `json:"evidence"`  // 0..1: confidence × how many sources took a side
	Recency       float64 `json:"recency"`   // 0..1: decay of the newest relevant source by category half-life
	Authority     float64 `json:"authority"` // 0..1: official sources weigh more
	HalfLifeDays  int     `json:"half_life_days"`
	NewestAgeDays *int    `json:"newest_age_days,omitempty"`
	Formula       string  `json:"formula"`
}

// Verification is the outcome for one claim.
type Verification struct {
	Verdict       Verdict   `json:"verdict"`
	CurrentValue  string    `json:"current_value,omitempty"`
	Confidence    float64   `json:"confidence"`
	Explanation   string    `json:"explanation"`
	CorrectedText string    `json:"corrected_text,omitempty"`
	AsOf          string    `json:"as_of,omitempty"`
	Sources       []Source  `json:"sources"`
	SearchQuery   string    `json:"search_query"`
	SearchSummary string    `json:"search_summary,omitempty"`
	Score         int       `json:"score"`
	Label         Label     `json:"label"`
	Breakdown     Breakdown `json:"breakdown"`
	Error         string    `json:"error,omitempty"`
}

type ClaimReport struct {
	Claim
	Result *Verification `json:"result"`
}

type Summary struct {
	Score      int             `json:"score"`
	Label      Label           `json:"label"`
	Total      int             `json:"total"`
	ByVerdict  map[Verdict]int `json:"by_verdict"`
	ByLabel    map[Label]int   `json:"by_label"`
	DurationMs int64           `json:"duration_ms"`
}

// Report is the full result of one verification run.
type Report struct {
	ID        string        `json:"id"`
	Text      string        `json:"text"`
	CheckedAt time.Time     `json:"checked_at"`
	Claims    []ClaimReport `json:"claims"`
	Summary   Summary       `json:"summary"`
	Trace     []TraceEntry  `json:"trace"`
}

// TraceEntry records one external call so the UI can show what Bifrost and Tavily did.
type TraceEntry struct {
	Kind    string `json:"kind"` // "llm" | "search"
	Step    string `json:"step"` // extract | judge | search
	ClaimID string `json:"claim_id,omitempty"`
	AtMs    int64  `json:"at_ms"` // offset from run start

	// llm (Bifrost)
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	Fallback         bool   `json:"fallback,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`

	// search (Tavily)
	Query   string `json:"query,omitempty"`
	Results int    `json:"results,omitempty"`
	Cached  bool   `json:"cached,omitempty"`

	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// Event is streamed to the UI while a run progresses.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

const (
	EvStage       = "stage"        // {"stage": "extracting"|"verifying"|"done"}
	EvClaims      = "claims"       // {"claims": [...Claim]}
	EvClaimStatus = "claim_status" // {"claim_id", "status": "searching"|"judging"}
	EvClaimResult = "claim_result" // {"claim_id", "result": Verification}
	EvTrace       = "trace"        // TraceEntry
	EvSummary     = "summary"      // Summary
	EvReport      = "report"       // Report
	EvError       = "error"        // {"message"}
)
