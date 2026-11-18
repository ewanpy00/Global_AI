// Package eval benchmarks the verification pipeline on labelled data:
//
//   - claims: single time-sensitive statements with a gold label (current, stale or
//     unverifiable) and, for stale ones, the acceptable current values;
//   - answers: AI-style answers with the spans a good extractor must find, and timeless
//     statements it must skip.
//
// It reports verdict accuracy, macro-F1, stale-detection precision/recall, coverage, score
// calibration (Brier, ECE), current-value accuracy, extraction recall/precision, latency and cost.
// With an ablation it runs the plain pipeline and the full one on the same data, so every
// accuracy feature has to earn its cost in numbers.
package eval

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

//go:embed dataset.json
var liveDataset []byte

//go:embed demo_dataset.json
var demoDataset []byte

// Gold classes. "stale" covers both outdated and incorrect verdicts.
const (
	Current      = "current"
	Stale        = "stale"
	Unverifiable = "unverifiable"
)

var classes = []string{Current, Stale, Unverifiable}

type Dataset struct {
	Version string   `json:"version"`
	Note    string   `json:"note"`
	Claims  []Item   `json:"claims"`
	Answers []Answer `json:"answers"`
}

// Item is one labelled claim.
type Item struct {
	ID             string   `json:"id"`
	Text           string   `json:"text"`
	Category       string   `json:"category"`
	Entity         string   `json:"entity"`
	Attribute      string   `json:"attribute"`
	ClaimedValue   string   `json:"claimed_value"`
	SearchQuery    string   `json:"search_query"`
	OfficialDomain string   `json:"official_domain,omitempty"`
	Gold           string   `json:"gold"`
	ExpectAny      []string `json:"expect_any,omitempty"` // any of these in current_value counts as the right new value
	Note           string   `json:"note,omitempty"`
}

// Answer is a labelled AI answer for the extraction benchmark.
type Answer struct {
	ID     string   `json:"id"`
	Text   string   `json:"text"`
	Claims []string `json:"claims"` // substrings that must be extracted as claims
	Skip   []string `json:"skip"`   // timeless statements that must not be extracted
}

// Load reads a dataset file, or the bundled one ("" → live set, or the demo set in demo mode).
func Load(path string, demo bool) (*Dataset, error) {
	raw := liveDataset
	if demo {
		raw = demoDataset
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	var ds Dataset
	if err := json.Unmarshal(raw, &ds); err != nil {
		return nil, fmt.Errorf("dataset: %w", err)
	}
	for _, it := range ds.Claims {
		switch it.Gold {
		case Current, Stale, Unverifiable:
		default:
			return nil, fmt.Errorf("dataset: claim %s has gold %q (want current|stale|unverifiable)", it.ID, it.Gold)
		}
	}
	return &ds, nil
}

// ClaimOutcome is the result for one labelled claim.
type ClaimOutcome struct {
	ID           string  `json:"id"`
	Text         string  `json:"text"`
	Category     string  `json:"category"`
	Gold         string  `json:"gold"`
	Predicted    string  `json:"predicted"`
	Verdict      string  `json:"verdict"`
	Score        int     `json:"score"`
	Confidence   float64 `json:"confidence"`
	CurrentValue string  `json:"current_value,omitempty"`
	ValueOK      *bool   `json:"value_ok,omitempty"`
	LatencyMs    int64   `json:"latency_ms"`
	Tokens       int     `json:"tokens"`
	LLMCalls     int     `json:"llm_calls"`
	CachedCalls  int     `json:"cached_calls"`
	Credits      float64 `json:"credits"`
	Hops         int     `json:"hops"`
	Contested    bool    `json:"contested,omitempty"`
	Error        string  `json:"error,omitempty"`
}

type PRF struct {
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	Support   int     `json:"support"`
}

type Metrics struct {
	N             int                       `json:"n"`
	Accuracy      float64                   `json:"accuracy"`
	MacroF1       float64                   `json:"macro_f1"`
	PerClass      map[string]PRF            `json:"per_class"`
	Confusion     map[string]map[string]int `json:"confusion"` // gold → predicted → count
	Coverage      float64                   `json:"coverage"`  // share of current/stale claims that got a definite verdict
	Brier         float64                   `json:"brier"`     // score/100 as P(current), on current/stale claims; lower is better
	ECE           float64                   `json:"ece"`       // expected calibration error over 5 bins; lower is better
	ValueAccuracy *float64                  `json:"value_accuracy,omitempty"`
	LatencyP50    int64                     `json:"latency_p50_ms"`
	LatencyP95    int64                     `json:"latency_p95_ms"`
	TokensPer     float64                   `json:"tokens_per_claim"`
	CreditsPer    float64                   `json:"credits_per_claim"`
	LLMCallsPer   float64                   `json:"llm_calls_per_claim"`
	FollowUpRate  float64                   `json:"follow_up_rate"`
	Errors        int                       `json:"errors"`
}

type ExtractionMetrics struct {
	Answers        int     `json:"answers"`
	Gold           int     `json:"gold"`
	Found          int     `json:"found"`
	Extracted      int     `json:"extracted"`
	Matching       int     `json:"matching"`
	Recall         float64 `json:"recall"`
	Precision      float64 `json:"precision"`
	SkipViolations int     `json:"skip_violations"` // timeless statements wrongly extracted
	SkipTotal      int     `json:"skip_total"`
}

// Result is one benchmark run with one feature set.
type Result struct {
	Label      string             `json:"label"`
	Features   verify.Features    `json:"features"`
	Demo       bool               `json:"demo"`
	Models     map[string]string  `json:"models"`
	Dataset    string             `json:"dataset"`
	RunAt      time.Time          `json:"run_at"`
	DurationMs int64              `json:"duration_ms"`
	Metrics    Metrics            `json:"metrics"`
	Extraction *ExtractionMetrics `json:"extraction,omitempty"`
	Claims     []ClaimOutcome     `json:"claims"`
}

// Report is what `make eval` writes and the Quality tab shows.
type Report struct {
	Runs []Result `json:"runs"`
}

// Options for one run.
type Options struct {
	Label       string
	Concurrency int // claims checked in parallel (default 3)
	Limit       int // only the first N claims (0 = all)
	Extraction  bool
	Demo        bool
	Models      map[string]string
	Progress    func(done, total int, id string)
}

// Run benchmarks engine (with its current Features) on ds.
func Run(ctx context.Context, e *verify.Engine, ds *Dataset, o Options) *Result {
	started := time.Now()
	items := ds.Claims
	if o.Limit > 0 && o.Limit < len(items) {
		items = items[:o.Limit]
	}
	conc := o.Concurrency
	if conc <= 0 {
		conc = 3
	}
	out := make([]ClaimOutcome, len(items))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for i, it := range items {
		wg.Add(1)
		go func(i int, it Item) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = checkOne(ctx, e, it)
			if o.Progress != nil {
				mu.Lock()
				done++
				o.Progress(done, len(items), it.ID)
				mu.Unlock()
			}
		}(i, it)
	}
	wg.Wait()

	res := &Result{
		Label: o.Label, Features: e.Features, Demo: o.Demo, Models: o.Models, Dataset: ds.Version,
		RunAt: started.UTC(), Claims: out, Metrics: Compute(out),
	}
	if o.Extraction && len(ds.Answers) > 0 {
		res.Extraction = extraction(ctx, e, ds.Answers)
	}
	res.DurationMs = time.Since(started).Milliseconds()
	return res
}

func checkOne(ctx context.Context, e *verify.Engine, it Item) ClaimOutcome {
	t0 := time.Now()
	rep := e.VerifyClaims(ctx, it.Text, []verify.Claim{{
		Text: it.Text, Category: verify.Category(it.Category), Entity: it.Entity, Attribute: it.Attribute,
		ClaimedValue: it.ClaimedValue, Sensitivity: verify.SensHigh, SearchQuery: it.SearchQuery, OfficialDomain: it.OfficialDomain,
	}}, nil)
	o := ClaimOutcome{ID: it.ID, Text: it.Text, Category: it.Category, Gold: it.Gold, LatencyMs: time.Since(t0).Milliseconds()}
	u := rep.Summary.Usage
	o.Tokens, o.LLMCalls, o.CachedCalls, o.Credits = u.Tokens, u.LLMCalls, u.CachedLLMCalls, u.TavilyCredits
	if len(rep.Claims) == 0 || rep.Claims[0].Result == nil {
		o.Predicted, o.Error = Unverifiable, "no result"
		return o
	}
	r := rep.Claims[0].Result
	o.Verdict, o.Score, o.Confidence, o.CurrentValue, o.Hops, o.Error = string(r.Verdict), r.Score, r.Confidence, r.CurrentValue, r.Hops, r.Error
	o.Contested = r.Jury != nil && r.Jury.Agreement == "contested"
	o.Predicted = classOf(r.Verdict)
	if it.Gold == Stale && o.Predicted == Stale && len(it.ExpectAny) > 0 {
		ok := false
		low := strings.ToLower(r.CurrentValue + " " + r.CorrectedText)
		for _, want := range it.ExpectAny {
			if strings.Contains(low, strings.ToLower(want)) {
				ok = true
				break
			}
		}
		o.ValueOK = &ok
	}
	return o
}

func classOf(v verify.Verdict) string {
	switch v {
	case verify.VerdictCurrent:
		return Current
	case verify.VerdictOutdated, verify.VerdictIncorrect:
		return Stale
	}
	return Unverifiable
}

// Compute derives all metrics from the per-claim outcomes.
func Compute(out []ClaimOutcome) Metrics {
	m := Metrics{N: len(out), PerClass: map[string]PRF{}, Confusion: map[string]map[string]int{}}
	if len(out) == 0 {
		return m
	}
	for _, c := range classes {
		m.Confusion[c] = map[string]int{}
	}
	correct := 0
	var lat []int64
	var tokens, calls, hops int
	var credits float64
	for _, o := range out {
		m.Confusion[o.Gold][o.Predicted]++
		if o.Gold == o.Predicted {
			correct++
		}
		if o.Error != "" {
			m.Errors++
		}
		lat = append(lat, o.LatencyMs)
		tokens += o.Tokens
		calls += o.LLMCalls
		credits += o.Credits
		if o.Hops > 1 {
			hops++
		}
	}
	n := float64(len(out))
	m.Accuracy = round3(float64(correct) / n)
	var f1sum float64
	var f1n int
	for _, c := range classes {
		tp := m.Confusion[c][c]
		var predicted, support int
		for _, g := range classes {
			predicted += m.Confusion[g][c]
			support += m.Confusion[c][g]
		}
		p := ratio(tp, predicted)
		r := ratio(tp, support)
		f := 0.0
		if p+r > 0 {
			f = 2 * p * r / (p + r)
		}
		m.PerClass[c] = PRF{Precision: round3(p), Recall: round3(r), F1: round3(f), Support: support}
		if support > 0 {
			f1sum += f
			f1n++
		}
	}
	if f1n > 0 {
		m.MacroF1 = round3(f1sum / float64(f1n))
	}

	// Coverage and calibration on claims whose truth is knowable.
	var definite, decided int
	var brier float64
	type pt struct{ p, y float64 }
	var pts []pt
	var valueN, valueOK int
	for _, o := range out {
		if o.Gold == Unverifiable {
			continue
		}
		definite++
		if o.Predicted != Unverifiable {
			decided++
		}
		y := 0.0
		if o.Gold == Current {
			y = 1
		}
		p := float64(o.Score) / 100
		brier += (p - y) * (p - y)
		pts = append(pts, pt{p, y})
		if o.ValueOK != nil {
			valueN++
			if *o.ValueOK {
				valueOK++
			}
		}
	}
	if definite > 0 {
		m.Coverage = round3(float64(decided) / float64(definite))
		m.Brier = round3(brier / float64(definite))
		const bins = 5
		var ece float64
		for b := 0; b < bins; b++ {
			lo, hi := float64(b)/bins, float64(b+1)/bins
			var sp, sy float64
			var k int
			for _, x := range pts {
				if x.p >= lo && (x.p < hi || (b == bins-1 && x.p <= hi)) {
					sp += x.p
					sy += x.y
					k++
				}
			}
			if k > 0 {
				ece += float64(k) / float64(len(pts)) * math.Abs(sp/float64(k)-sy/float64(k))
			}
		}
		m.ECE = round3(ece)
	}
	if valueN > 0 {
		v := round3(float64(valueOK) / float64(valueN))
		m.ValueAccuracy = &v
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	m.LatencyP50 = lat[len(lat)/2]
	m.LatencyP95 = lat[int(math.Ceil(0.95*float64(len(lat))))-1]
	m.TokensPer = round1(float64(tokens) / n)
	m.CreditsPer = round3(credits / n)
	m.LLMCallsPer = round3(float64(calls) / n)
	m.FollowUpRate = round3(float64(hops) / n)
	return m
}

func extraction(ctx context.Context, e *verify.Engine, answers []Answer) *ExtractionMetrics {
	em := &ExtractionMetrics{Answers: len(answers)}
	for _, a := range answers {
		claims, _, err := e.ExtractClaims(ctx, a.Text)
		if err != nil {
			continue
		}
		em.Gold += len(a.Claims)
		em.SkipTotal += len(a.Skip)
		em.Extracted += len(claims)
		for _, g := range a.Claims {
			for _, c := range claims {
				if overlaps(c.Text, g) {
					em.Found++
					break
				}
			}
		}
		for _, c := range claims {
			for _, g := range a.Claims {
				if overlaps(c.Text, g) {
					em.Matching++
					break
				}
			}
			for _, s := range a.Skip {
				if overlaps(c.Text, s) {
					em.SkipViolations++
					break
				}
			}
		}
	}
	em.Recall = round3(ratio(em.Found, em.Gold))
	em.Precision = round3(ratio(em.Matching, em.Extracted))
	return em
}

// overlaps is true when one text contains the other, or they share most of their words.
func overlaps(a, b string) bool {
	la, lb := strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if la == "" || lb == "" {
		return false
	}
	if strings.Contains(la, lb) || strings.Contains(lb, la) {
		return true
	}
	wa, wb := words(la), words(lb)
	shared := 0
	for w := range wb {
		if wa[w] {
			shared++
		}
	}
	small := len(wa)
	if len(wb) < small {
		small = len(wb)
	}
	return small > 0 && float64(shared)/float64(small) >= 0.6
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '$') }) {
		if len(w) > 2 {
			out[w] = true
		}
	}
	return out
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
func round1(f float64) float64 { return math.Round(f*10) / 10 }

// Markdown renders runs side by side (baseline vs full) as a table for the terminal and README.
func Markdown(runs []Result) string {
	var b strings.Builder
	b.WriteString("| Metric |")
	for _, r := range runs {
		fmt.Fprintf(&b, " %s |", r.Label)
	}
	b.WriteString("\n|---|")
	for range runs {
		b.WriteString("---:|")
	}
	b.WriteString("\n")
	row := func(name string, f func(Result) string) {
		fmt.Fprintf(&b, "| %s |", name)
		for _, r := range runs {
			fmt.Fprintf(&b, " %s |", f(r))
		}
		b.WriteString("\n")
	}
	pct := func(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }
	row("Claims", func(r Result) string { return fmt.Sprint(r.Metrics.N) })
	row("Verdict accuracy", func(r Result) string { return pct(r.Metrics.Accuracy) })
	row("Macro-F1", func(r Result) string { return fmt.Sprintf("%.3f", r.Metrics.MacroF1) })
	row("Stale precision", func(r Result) string { return pct(r.Metrics.PerClass[Stale].Precision) })
	row("Stale recall", func(r Result) string { return pct(r.Metrics.PerClass[Stale].Recall) })
	row("Coverage (decided)", func(r Result) string { return pct(r.Metrics.Coverage) })
	row("Current-value accuracy", func(r Result) string {
		if r.Metrics.ValueAccuracy == nil {
			return "–"
		}
		return pct(*r.Metrics.ValueAccuracy)
	})
	row("Brier (lower is better)", func(r Result) string { return fmt.Sprintf("%.3f", r.Metrics.Brier) })
	row("ECE (lower is better)", func(r Result) string { return fmt.Sprintf("%.3f", r.Metrics.ECE) })
	row("Follow-up rate", func(r Result) string { return pct(r.Metrics.FollowUpRate) })
	row("Latency p50 / p95", func(r Result) string {
		return fmt.Sprintf("%.1f s / %.1f s", float64(r.Metrics.LatencyP50)/1000, float64(r.Metrics.LatencyP95)/1000)
	})
	row("LLM calls per claim", func(r Result) string { return fmt.Sprintf("%.2f", r.Metrics.LLMCallsPer) })
	row("Tokens per claim", func(r Result) string { return fmt.Sprintf("%.0f", r.Metrics.TokensPer) })
	row("Tavily credits per claim", func(r Result) string { return fmt.Sprintf("%.2f", r.Metrics.CreditsPer) })
	if runs[len(runs)-1].Extraction != nil {
		row("Extraction recall", func(r Result) string {
			if r.Extraction == nil {
				return "–"
			}
			return pct(r.Extraction.Recall)
		})
		row("Extraction precision", func(r Result) string {
			if r.Extraction == nil {
				return "–"
			}
			return pct(r.Extraction.Precision)
		})
		row("Timeless statements extracted", func(r Result) string {
			if r.Extraction == nil {
				return "–"
			}
			return fmt.Sprintf("%d / %d", r.Extraction.SkipViolations, r.Extraction.SkipTotal)
		})
	}
	return b.String()
}
