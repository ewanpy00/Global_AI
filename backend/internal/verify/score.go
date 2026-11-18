package verify

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Half-life in days: how fast a category of fact typically goes stale.
// Prices and versions change every few months, laws and leadership far less often.
var halfLifeDays = map[Category]int{
	CatPricing:    90,
	CatAPIVersion: 120,
	CatStatistic:  180,
	CatCompany:    240,
	CatLeadership: 365,
	CatRegulation: 365,
	CatOther:      365,
}

// volatility 0..1 is used when nothing could be verified: an unverified price is riskier
// than an unverified headquarters city.
var volatility = map[Category]float64{
	CatPricing:    1,
	CatAPIVersion: 1,
	CatStatistic:  0.7,
	CatCompany:    0.5,
	CatLeadership: 0.5,
	CatRegulation: 0.5,
	CatOther:      0.3,
}

const (
	freshThreshold = 75
	staleThreshold = 45
)

func labelFor(score int) Label {
	switch {
	case score >= freshThreshold:
		return LabelFresh
	case score < staleThreshold:
		return LabelStale
	}
	return LabelReview
}

// Score computes the 0..100 freshness score for one verified claim.
//
//	current       → 50 + 50·(0.5·E + 0.3·R + 0.2·A)     range 50..100
//	outdated/
//	incorrect     → 35·(1 − (0.6·E + 0.2·R + 0.2·A))    range  0..35
//	unverifiable  → 60 − 15·volatility(category)         range 45..60
//
// E = evidence strength, R = recency of newest relevant source, A = source authority.
// Strong, recent, official evidence pushes a supported claim to ~100 and a refuted one to ~0;
// weak evidence keeps both near the "needs review" band.
func Score(cat Category, v *Verification, now time.Time) (int, Breakdown) {
	hl := halfLifeDays[cat]
	if hl == 0 {
		hl = 365
	}
	b := Breakdown{Verdict: v.Verdict, Confidence: clamp01(v.Confidence), HalfLifeDays: hl}

	var taking, official int
	newest := -1
	for _, s := range v.Sources {
		if s.Stance == StanceNeutral {
			continue
		}
		taking++
		if s.Official {
			official++
		}
		if age, ok := ageDays(s.PublishedDate, now); ok && (newest < 0 || age < newest) {
			newest = age
		}
	}
	if newest < 0 && v.AsOf != "" {
		if age, ok := ageDays(v.AsOf, now); ok {
			newest = age
		}
	}

	b.Evidence = b.Confidence * math.Min(1, float64(taking)/2)
	if newest >= 0 {
		n := newest
		b.NewestAgeDays = &n
		b.Recency = math.Pow(0.5, float64(newest)/float64(hl))
	} else {
		b.Recency = 0.5 // undated evidence: neither rewarded nor punished
	}
	switch {
	case official > 0:
		b.Authority = 1
	case taking > 0:
		b.Authority = 0.6
	default:
		b.Authority = 0
	}

	var score float64
	switch v.Verdict {
	case VerdictCurrent:
		score = 50 + 50*(0.5*b.Evidence+0.3*b.Recency+0.2*b.Authority)
		b.Formula = "50 + 50·(0.5·E + 0.3·R + 0.2·A)"
	case VerdictOutdated, VerdictIncorrect:
		score = 35 * (1 - (0.6*b.Evidence + 0.2*b.Recency + 0.2*b.Authority))
		b.Formula = "35·(1 − (0.6·E + 0.2·R + 0.2·A))"
	default:
		vol := volatility[cat]
		score = 60 - 15*vol
		b.Formula = fmt.Sprintf("60 − 15·volatility(%s)=%.1f", cat, vol)
	}
	b.Evidence, b.Recency = round2(b.Evidence), round2(b.Recency)
	return int(math.Round(score)), b
}

var sensitivityWeight = map[Sensitivity]float64{SensHigh: 3, SensMedium: 2, SensLow: 1}

// Summarize computes the answer-level score: a mean of claim scores weighted by how
// time-sensitive each claim is, so a stale price matters more than a stale founding detail.
func Summarize(claims []ClaimReport) Summary {
	s := Summary{ByVerdict: map[Verdict]int{}, ByLabel: map[Label]int{}, Total: len(claims)}
	var sum, weight float64
	for _, c := range claims {
		if c.Result == nil {
			continue
		}
		w := sensitivityWeight[c.Sensitivity]
		if w == 0 {
			w = 2
		}
		sum += w * float64(c.Result.Score)
		weight += w
		s.ByVerdict[c.Result.Verdict]++
		s.ByLabel[c.Result.Label]++
	}
	if weight == 0 {
		s.Score = 100
	} else {
		s.Score = int(math.Round(sum / weight))
	}
	s.Label = labelFor(s.Score)
	// One refuted claim makes the whole answer unsafe to reuse as-is.
	if s.ByLabel[LabelStale] > 0 && s.Label == LabelFresh {
		s.Label = LabelReview
	}
	return s
}

// ageDays parses the date formats Tavily and models produce and returns the age in days.
func ageDays(s string, now time.Time) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	layouts := []string{
		time.RFC3339, time.RFC1123, time.RFC1123Z, "2006-01-02", "2006-01-02 15:04:05",
		"Mon, 02 Jan 2006 15:04:05 MST", "January 2, 2006", "Jan 2, 2006", "2006-01",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			d := int(now.Sub(t).Hours() / 24)
			if d < 0 {
				d = 0
			}
			return d, true
		}
	}
	return 0, false
}

func clamp01(f float64) float64 { return math.Max(0, math.Min(1, f)) }
func round2(f float64) float64  { return math.Round(f*100) / 100 }
