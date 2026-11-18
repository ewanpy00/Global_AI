package document

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/structs"

	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

// Highlight colours follow the usual reviewer convention and the web UI.
var highlightColor = map[verify.Label]rgb{
	verify.LabelFresh:  {134, 239, 172},
	verify.LabelReview: {253, 224, 71},
	verify.LabelStale:  {252, 165, 165},
}

var correctedColor = rgb{134, 239, 172}

var labelInk = map[verify.Label]rgb{verify.LabelFresh: inkFresh, verify.LabelReview: inkReview, verify.LabelStale: inkStale}

var verdictText = map[verify.Verdict]string{
	verify.VerdictCurrent:      "Current",
	verify.VerdictOutdated:     "Outdated",
	verify.VerdictIncorrect:    "Incorrect",
	verify.VerdictUnverifiable: "Unverified",
}

var labelText = map[verify.Label]string{verify.LabelFresh: "fresh", verify.LabelReview: "needs review", verify.LabelStale: "stale"}

// Annotated returns the document with a highlight and a note on every checked claim, plus a
// report page at the end. Original page content is left untouched.
func (e *Engine) Annotated(ctx context.Context, d *Doc, r *verify.Report) ([]byte, error) {
	if r == nil {
		return nil, errors.New("document has not been checked yet")
	}
	var out []byte
	err := e.open(ctx, d.Data, func(s *session) error {
		for _, c := range r.Claims {
			if c.Result == nil || !c.Located {
				continue
			}
			start, end := c.Span()
			quads, err := s.spanQuads(d, start, end)
			if err != nil {
				return err
			}
			subject := verdictText[c.Result.Verdict] + " claim"
			if err := s.highlightQuads(quads, highlightColor[c.Result.Label], subject, claimNote(c, false)); err != nil {
				return err
			}
		}
		if err := s.appendReport(d, r, nil); err != nil {
			return err
		}
		var err error
		out, err = s.save()
		return err
	})
	return out, err
}

// highlightQuads adds one Highlight annotation per page covering the given line rectangles.
func (s *session) highlightQuads(quads []quad, color rgb, subject, contents string) error {
	byPage := map[int][]quad{}
	for _, q := range quads {
		byPage[q.page] = append(byPage[q.page], q)
	}
	for pg, qs := range byPage {
		p, err := s.page(pg)
		if err != nil {
			return err
		}
		res, err := s.in.FPDFPage_CreateAnnot(&requests.FPDFPage_CreateAnnot{Page: p.ref(), Subtype: enums.FPDF_ANNOT_SUBTYPE_HIGHLIGHT})
		if err != nil {
			return fmt.Errorf("add highlight: %w", err)
		}
		a := res.Annotation
		err = func() error {
			bounds := structs.FPDF_FS_RECTF{Left: math.MaxFloat32, Bottom: math.MaxFloat32, Right: -math.MaxFloat32, Top: -math.MaxFloat32}
			for _, q := range qs {
				l, t, r, b := float32(q.left), float32(q.top), float32(q.right), float32(q.bottom)
				if _, err := s.in.FPDFAnnot_AppendAttachmentPoints(&requests.FPDFAnnot_AppendAttachmentPoints{Annotation: a,
					AttachmentPoints: structs.FPDF_FS_QUADPOINTSF{X1: l, Y1: t, X2: r, Y2: t, X3: l, Y3: b, X4: r, Y4: b}}); err != nil {
					return err
				}
				bounds.Left, bounds.Bottom = min(bounds.Left, l), min(bounds.Bottom, b)
				bounds.Right, bounds.Top = max(bounds.Right, r), max(bounds.Top, t)
			}
			if _, err := s.in.FPDFAnnot_SetRect(&requests.FPDFAnnot_SetRect{Annotation: a, Rect: bounds}); err != nil {
				return err
			}
			if _, err := s.in.FPDFAnnot_SetColor(&requests.FPDFAnnot_SetColor{Annotation: a, ColorType: enums.FPDFANNOT_COLORTYPE_Color, R: color[0], G: color[1], B: color[2], A: 255}); err != nil {
				return err
			}
			for k, v := range map[string]string{"Contents": contents, "T": "Freshgate", "Subj": subject} {
				if _, err := s.in.FPDFAnnot_SetStringValue(&requests.FPDFAnnot_SetStringValue{Annotation: a, Key: k, Value: v}); err != nil {
					return err
				}
			}
			_, err := s.in.FPDFAnnot_SetFlags(&requests.FPDFAnnot_SetFlags{Annotation: a, Flags: enums.FPDF_ANNOT_FLAG_PRINT})
			return err
		}()
		_, _ = s.in.FPDFPage_CloseAnnot(&requests.FPDFPage_CloseAnnot{Annotation: a})
		if err != nil {
			return fmt.Errorf("write highlight: %w", err)
		}
	}
	return nil
}

// claimNote is the comment text attached to a highlight.
func claimNote(c verify.ClaimReport, corrected bool) string {
	res := c.Result
	var b strings.Builder
	fmt.Fprintf(&b, "%s · freshness %d/100 (%s)", verdictText[res.Verdict], res.Score, labelText[res.Label])
	if res.CurrentValue != "" {
		fmt.Fprintf(&b, "\nNow: %s", res.CurrentValue)
		if res.AsOf != "" {
			fmt.Fprintf(&b, " · as of %s", res.AsOf)
		}
	}
	if c.Fixable() && !corrected {
		fmt.Fprintf(&b, "\nSuggested: %s", collapse(res.CorrectedText))
	}
	if res.Explanation != "" {
		fmt.Fprintf(&b, "\n%s", res.Explanation)
	}
	if src := res.BestSource(); src != "" {
		fmt.Fprintf(&b, "\nSource: %s", src)
	}
	return b.String()
}

func correctionNote(c verify.ClaimReport) string {
	return fmt.Sprintf("Corrected by Freshgate.\nOriginal: “%s”\n%s", collapse(c.Text), claimNote(c, true))
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }
