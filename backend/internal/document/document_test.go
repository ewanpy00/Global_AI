package document

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klippa-app/go-pdfium/requests"

	"github.com/ewanpy00/Global_AI/backend/internal/demo"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

var engine = NewEngine()

func TestMain(m *testing.M) {
	code := m.Run()
	_ = engine.Close()
	os.Exit(code)
}

var ctx = context.Background()

func loadSample(t *testing.T) *Doc {
	t.Helper()
	data, err := engine.TextPDF(ctx, "Nimbus Cloud: vendor review", demo.SampleAnswer)
	if err != nil {
		t.Fatal(err)
	}
	d, err := engine.Load(ctx, "nimbus-review.pdf", data)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLoadReadsTextFromGeneratedPDF(t *testing.T) {
	d := loadSample(t)
	if d.TotalPages != 1 || d.CheckedPages != 1 || len(d.Pages) != 1 {
		t.Fatalf("pages: total %d checked %d sizes %d", d.TotalPages, d.CheckedPages, len(d.Pages))
	}
	if p := d.Pages[0]; p.Width != 595 || p.Height != 842 {
		t.Fatalf("page size %vx%v, want A4", p.Width, p.Height)
	}
	flat := collapse(d.Text)
	for _, want := range []string{"Nimbus Cloud: vendor review", "Its Pro plan costs $12 per user per month", "CEO Maria Lindqvist", "within 72 hours."} {
		if !strings.Contains(flat, want) {
			t.Errorf("extracted text lacks %q:\n%s", want, d.Text)
		}
	}
	if strings.Contains(d.Text, "**") {
		t.Error("markdown markers leaked into the PDF")
	}
	if d.Text != strings.TrimSpace(d.Text) || len(d.offs) != len(d.refs) || len(d.offs) == 0 {
		t.Fatal("text/offset mapping is inconsistent")
	}
}

func TestRejectsNonPDF(t *testing.T) {
	if _, err := engine.Load(ctx, "x.pdf", []byte("hello")); err == nil {
		t.Fatal("expected an error")
	}
}

func runDemo(t *testing.T, d *Doc) *verify.Report {
	t.Helper()
	e := &verify.Engine{LLM: &demo.LLM{}, Search: &demo.Search{}, MaxClaims: 12, Concurrency: 4}
	r, err := e.Run(ctx, d.Text, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func claimsOf(r *verify.Report) []verify.Claim {
	out := make([]verify.Claim, len(r.Claims))
	for i, c := range r.Claims {
		out[i] = c.Claim
	}
	return out
}

func TestPipelineAndBoxesOnPDF(t *testing.T) {
	d := loadSample(t)
	r := runDemo(t, d)
	if len(r.Claims) != 8 {
		t.Fatalf("got %d claims, want 8", len(r.Claims))
	}
	if r.Summary.ByVerdict[verify.VerdictOutdated] != 4 {
		t.Fatalf("verdicts: %+v", r.Summary.ByVerdict)
	}
	boxes, err := engine.Boxes(ctx, d, claimsOf(r))
	if err != nil {
		t.Fatal(err)
	}
	lastY := -1.0
	for _, c := range r.Claims {
		if !c.Located {
			t.Fatalf("claim %s not located in PDF text", c.ID)
		}
		bs := boxes[c.ID]
		if len(bs) == 0 {
			t.Fatalf("claim %s has no boxes", c.ID)
		}
		for _, b := range bs {
			if b.Page != 0 || b.X < 0 || b.Y < 0 || b.W <= 0 || b.H <= 0 || b.X+b.W > 1 || b.Y+b.H > 1 || b.H > 0.05 {
				t.Fatalf("claim %s: bad box %+v", c.ID, b)
			}
		}
		if bs[0].Y < lastY-0.001 {
			t.Errorf("claim %s starts above the previous claim (%.3f < %.3f)", c.ID, bs[0].Y, lastY)
		}
		lastY = bs[0].Y
	}
}

func annotCount(t *testing.T, data []byte, page int) int {
	t.Helper()
	n := 0
	err := engine.open(ctx, data, func(s *session) error {
		p, err := s.page(page)
		if err != nil {
			return err
		}
		res, err := s.in.FPDFPage_GetAnnotCount(&requests.FPDFPage_GetAnnotCount{Page: p.ref()})
		if err != nil {
			return err
		}
		n = res.Count
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func saveForInspection(t *testing.T, name string, data []byte) {
	if dir := os.Getenv("FRESHGATE_TEST_OUT"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
}

func TestAnnotatedAndCorrectedPDF(t *testing.T) {
	d := loadSample(t)
	r := runDemo(t, d)

	annotated, err := engine.Annotated(ctx, d, r)
	if err != nil {
		t.Fatal(err)
	}
	saveForInspection(t, "annotated.pdf", annotated)
	if n := annotCount(t, annotated, 0); n != 8 {
		t.Fatalf("annotated page 1 has %d annotations, want 8", n)
	}
	ad, err := engine.Load(ctx, "annotated.pdf", annotated)
	if err != nil {
		t.Fatal(err)
	}
	if ad.TotalPages < 2 {
		t.Fatalf("annotated copy has %d pages, want the report page appended", ad.TotalPages)
	}
	for i := range ad.Pages {
		if png, err := engine.PagePNG(ctx, ad, i); err == nil {
			saveForInspection(t, fmt.Sprintf("annotated-p%d.png", i+1), png)
		}
	}
	flat := collapse(ad.Text)
	for _, want := range []string{"Freshgate freshness report", "OUTDATED · page 1", "Suggested: ", "Source: https://"} {
		if !strings.Contains(flat, want) {
			t.Errorf("report page lacks %q", want)
		}
	}

	corrected, edits, err := engine.Corrected(ctx, d, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	saveForInspection(t, "corrected.pdf", corrected)
	if len(edits) != 4 {
		t.Fatalf("got %d edits, want 4", len(edits))
	}
	if n := annotCount(t, corrected, 0); n != 4 {
		t.Fatalf("corrected page 1 has %d highlights, want 4", n)
	}
	cd, err := engine.Load(ctx, "corrected.pdf", corrected)
	if err != nil {
		t.Fatal(err)
	}
	flat = collapse(cd.Text)
	for _, want := range []string{"$15 per user per month", "SDK is 4.1", "CEO Daniel Okafor", "Corrected to: "} {
		if !strings.Contains(flat, want) {
			t.Errorf("corrected copy lacks %q", want)
		}
	}
	if png, err := engine.PagePNG(ctx, cd, 0); err != nil || len(png) < 1000 {
		t.Fatalf("render corrected page: %v (%d bytes)", err, len(png))
	} else {
		saveForInspection(t, "corrected-p1.png", png)
	}

	// Only the requested claims are corrected.
	_, some, err := engine.Corrected(ctx, d, r, map[string]bool{edits[0].ClaimID: true})
	if err != nil || len(some) != 1 {
		t.Fatalf("partial correction: %d edits, %v", len(some), err)
	}
}

func TestLayoutTokens(t *testing.T) {
	rf, _, err := faces()
	if err != nil {
		t.Fatal(err)
	}
	ts := tokenize("one two three four five six seven eight", ", and the rest")
	if txt, fixEnd := lineText(ts); txt != "one two three four five six seven eight, and the rest" || txt[:fixEnd] != "one two three four five six seven eight" {
		t.Fatalf("lineText = %q (fix part %q)", txt, txt[:fixEnd])
	}
	lines := []textLine{{left: 0, right: 200, size: 11}, {left: 0, right: 120, size: 11}}
	segs := layoutTokens(rf, ts, lines)
	var all []string
	for _, seg := range segs {
		if len(seg) == 0 {
			t.Fatalf("empty line in %v", segs)
		}
		txt, _ := lineText(seg)
		all = append(all, txt)
	}
	if got := strings.Join(all, " "); got != "one two three four five six seven eight , and the rest" &&
		got != "one two three four five six seven eight, and the rest" {
		t.Fatalf("words lost or reordered: %q", got)
	}
	// Too long for the space: both lines are condensed by the same factor, and the last line
	// is not left with a disproportionate share.
	long := tokenize(strings.Repeat("word ", 30), "")
	segs = layoutTokens(rf, long, lines)
	if a, b := len(segs[0]), len(segs[1]); a < b {
		t.Fatalf("the wider line got fewer words (%d vs %d)", a, b)
	}
}

func TestStoreEvictsOldest(t *testing.T) {
	st := NewStore(2)
	a, b, c := &Doc{ID: "a", Data: make([]byte, 10)}, &Doc{ID: "b"}, &Doc{ID: "c"}
	st.Put(a)
	st.Put(b)
	st.Put(b) // re-putting must not count twice
	st.Put(c)
	if _, ok := st.Get("a"); ok {
		t.Fatal("oldest document should be evicted")
	}
	if _, ok := st.Get("c"); !ok {
		t.Fatal("newest document missing")
	}
	st.maxBytes = 5
	st.Put(&Doc{ID: "big", Data: make([]byte, 8)})
	if _, ok := st.Get("big"); !ok || len(st.order) != 1 {
		t.Fatalf("byte cap: kept %v", st.order)
	}
}
