package verify

import (
	"fmt"
	"sort"
	"strings"
)

// Edit is one correction applied to the answer.
type Edit struct {
	ClaimID string `json:"claim_id"`
	Old     string `json:"old"`
	New     string `json:"new"`
	Source  string `json:"source,omitempty"`
}

// Fixable reports whether a claim has a correction that can be applied in place.
func (c ClaimReport) Fixable() bool {
	return c.Located && c.Result != nil && c.Result.CorrectedText != "" &&
		(c.Result.Verdict == VerdictOutdated || c.Result.Verdict == VerdictIncorrect)
}

// ApplyFixes replaces every fixable claim span with the judge's corrected text.
// Spans never overlap (the extractor drops overlaps), so applying from the end keeps offsets valid.
func ApplyFixes(r *Report) (string, []Edit) {
	if r == nil {
		return "", nil
	}
	fixable := make([]ClaimReport, 0, len(r.Claims))
	for _, c := range r.Claims {
		if c.Fixable() && c.byteEnd <= len(r.Text) {
			fixable = append(fixable, c)
		}
	}
	sort.Slice(fixable, func(i, j int) bool { return fixable[i].byteStart > fixable[j].byteStart })
	text := r.Text
	edits := make([]Edit, 0, len(fixable))
	for _, c := range fixable {
		text = text[:c.byteStart] + c.Result.CorrectedText + text[c.byteEnd:]
		edits = append(edits, Edit{ClaimID: c.ID, Old: c.Text, New: c.Result.CorrectedText, Source: bestSourceURL(c.Result)})
	}
	// Report edits in reading order.
	for i, j := 0, len(edits)-1; i < j; i, j = i+1, j-1 {
		edits[i], edits[j] = edits[j], edits[i]
	}
	return text, edits
}

// bestSourceURL picks the source to cite for a correction: official contradicting sources first.
func bestSourceURL(v *Verification) string {
	best, bestRank := "", -1
	for _, s := range v.Sources {
		rank := 0
		if s.Stance == StanceContradicts {
			rank += 2
		}
		if s.Official {
			rank++
		}
		if s.Stance != StanceNeutral && rank > bestRank {
			best, bestRank = s.URL, rank
		}
	}
	return best
}

var verdictMark = map[Verdict]string{
	VerdictOutdated:     "⚠️ Outdated",
	VerdictIncorrect:    "❌ Incorrect",
	VerdictUnverifiable: "❔ Unverified",
}

// Footnote renders a Markdown block that the connector appends to chat answers, so any chat
// client shows the freshness check without knowing about Freshgate. edits is non-nil in
// "correct" mode, where flagged spans were already rewritten in the answer.
func Footnote(r *Report, edits []Edit) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	s := r.Summary
	b.WriteString("\n\n---\n")
	if s.Total == 0 {
		b.WriteString("**Freshness check:** no time-sensitive claims found.\n")
		return b.String()
	}
	problems := s.ByVerdict[VerdictOutdated] + s.ByVerdict[VerdictIncorrect]
	fmt.Fprintf(&b, "**Freshness check: %d/100** · %d of %d claims confirmed by current sources",
		s.Score, s.ByVerdict[VerdictCurrent], s.Total)
	if problems > 0 {
		fmt.Fprintf(&b, " · %d outdated or wrong", problems)
	}
	fmt.Fprintf(&b, " · checked %s\n", r.CheckedAt.Format("Jan 2, 2006"))

	if edits != nil {
		if len(edits) > 0 {
			b.WriteString("\nCorrected in this answer:\n")
		}
		for _, e := range edits {
			fmt.Fprintf(&b, "- ~~%s~~ → %s%s\n", oneLine(e.Old), oneLine(e.New), cite(e.Source))
		}
	}
	for _, c := range r.Claims {
		if c.Result == nil || c.Result.Verdict == VerdictCurrent {
			continue
		}
		if edits != nil && c.Fixable() {
			continue // already listed as a correction
		}
		mark := verdictMark[c.Result.Verdict]
		line := fmt.Sprintf("- %s: \"%s\"", mark, oneLine(shorten(c.Text, 140)))
		if c.Result.CurrentValue != "" && c.Result.Verdict != VerdictUnverifiable {
			line += " → now: **" + oneLine(c.Result.CurrentValue) + "**"
		}
		b.WriteString(line + cite(bestSourceURL(c.Result)) + "\n")
	}
	return b.String()
}

func cite(u string) string {
	if u == "" {
		return ""
	}
	return " ([source](" + u + "))"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func shorten(s string, n int) string { return truncateRunes(s, n) }
