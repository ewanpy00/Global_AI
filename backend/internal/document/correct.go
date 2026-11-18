package document

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"

	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

// Corrected returns the document with each fixable claim rewritten in place. The claim's words
// and the rest of its last line are covered, then the corrected wording plus that rest of line is
// set again on the same lines, condensed (or, as a last resort, shrunk) to fit. Each change gets
// a green highlight whose note keeps the original wording and the source, and a report page
// lists every edit. only limits the claims to correct (nil = every fixable claim).
//
// The original text stays in the file underneath the new words (PDF has no reliable way to edit
// text inside an existing text run), so this is a corrected copy for reading, not a redaction.
func (e *Engine) Corrected(ctx context.Context, d *Doc, r *verify.Report, only map[string]bool) ([]byte, []verify.Edit, error) {
	if r == nil {
		return nil, nil, errors.New("document has not been checked yet")
	}
	var jobs []verify.ClaimReport
	// Where each correction starts: a rewrite that takes over the rest of its line stops there.
	stops := map[int]map[int]bool{}
	for _, c := range r.Claims {
		if !c.Fixable() || (only != nil && !only[c.ID]) {
			continue
		}
		jobs = append(jobs, c)
		start, end := c.Span()
		span := d.charSpan(start, end)
		if pg, ok := firstKey(span); ok {
			if stops[pg] == nil {
				stops[pg] = map[int]bool{}
			}
			stops[pg][span[pg][0]] = true
		}
	}

	var out []byte
	var edits []verify.Edit
	err := e.open(ctx, d.Data, func(s *session) error {
		var fnt fontRef
		applied := map[string]bool{}
		for _, c := range jobs {
			start, end := c.Span()
			lines, err := s.spanLines(d, start, end)
			if errors.Is(err, errNotHorizontal) || (err == nil && len(lines) == 0) {
				continue // leave rotated or empty spans alone; the report page still lists them
			}
			if err != nil {
				return err
			}
			span := d.charSpan(start, end)
			lastPage, _ := lastKey(span)
			tail, stopped, err := s.restOfLine(lastPage, span[lastPage][1], &lines[len(lines)-1], stops[lastPage])
			if err != nil {
				return err
			}
			// New words may use the blank space up to the paragraph's right edge, except on a
			// line where another correction follows.
			for i := range lines {
				lines[i].limit = lines[i].right
				if i < len(lines)-1 || !stopped {
					if lines[i].limit, err = s.columnRight(lines[i]); err != nil {
						return err
					}
				}
			}
			if !fnt.loaded {
				if fnt, err = s.regularFont(); err != nil {
					return err
				}
			}
			placed, err := s.rewrite(lines, c.Result.CorrectedText, tail, fnt)
			if err != nil {
				return err
			}
			if err := s.highlightQuads(placed, correctedColor, "Corrected by Freshgate", correctionNote(c)); err != nil {
				return err
			}
			applied[c.ID] = true
			edits = append(edits, verify.Edit{ClaimID: c.ID, Old: c.Text, New: c.Result.CorrectedText, Source: c.Result.BestSource()})
		}
		if err := s.appendReport(d, r, applied); err != nil {
			return err
		}
		var err error
		out, err = s.save()
		return err
	})
	return out, edits, err
}

func firstKey(m map[int][2]int) (int, bool) {
	k, ok := math.MaxInt, false
	for p := range m {
		k, ok = min(k, p), true
	}
	return k, ok
}

func lastKey(m map[int][2]int) (int, bool) {
	k, ok := -1, false
	for p := range m {
		k, ok = max(k, p), true
	}
	return k, ok
}

// fontRef is the regular Go font embedded into the document being corrected.
type fontRef struct {
	loaded bool
	ref    references.FPDF_FONT
	face   face
}

func (s *session) regularFont() (fontRef, error) {
	rf, _, err := faces()
	if err != nil {
		return fontRef{}, err
	}
	ref, err := s.loadFont(rf)
	if err != nil {
		return fontRef{}, err
	}
	return fontRef{loaded: true, ref: ref, face: rf}, nil
}

var errNotHorizontal = errors.New("text is rotated")

// textLine is the part of one printed line that a rewrite covers.
type textLine struct {
	page                     int
	left, right, bottom, top float64 // loose box (font ascent to descent), page space
	baseline                 float64
	size                     float64 // effective font size in points
	color                    rgb
	limit                    float64 // how far right new text may go (≥ right)
}

func (ln textLine) capacity() float64 { return max(ln.limit, ln.right) - ln.left }

// charGeom reads the position of one character. ok is false for generated characters and spaces.
type charGeom struct {
	r                        rune
	left, right, bottom, top float64
	x, y                     float64 // origin; y is the baseline
	size                     float64
	rotated                  bool
}

func (s *session) charAt(p *openPage, i int) (charGeom, bool, error) {
	gen, err := s.in.FPDFText_IsGenerated(&requests.FPDFText_IsGenerated{TextPage: p.text, Index: i})
	if err != nil {
		return charGeom{}, false, err
	}
	u, err := s.in.FPDFText_GetUnicode(&requests.FPDFText_GetUnicode{TextPage: p.text, Index: i})
	if err != nil {
		return charGeom{}, false, err
	}
	g := charGeom{r: rune(u.Unicode)}
	if gen.IsGenerated || g.r == 0 || unicode.IsSpace(g.r) {
		return g, false, nil
	}
	box, err := s.in.FPDFText_GetLooseCharBox(&requests.FPDFText_GetLooseCharBox{TextPage: p.text, Index: i})
	if err != nil {
		return g, false, err
	}
	org, err := s.in.FPDFText_GetCharOrigin(&requests.FPDFText_GetCharOrigin{TextPage: p.text, Index: i})
	if err != nil {
		return g, false, err
	}
	m, err := s.in.FPDFText_GetMatrix(&requests.FPDFText_GetMatrix{TextPage: p.text, Index: i})
	if err != nil {
		return g, false, err
	}
	fs, err := s.in.FPDFText_GetFontSize(&requests.FPDFText_GetFontSize{TextPage: p.text, Index: i})
	if err != nil {
		return g, false, err
	}
	g.left, g.right = float64(box.Rect.Left), float64(box.Rect.Right)
	g.top, g.bottom = float64(box.Rect.Top), float64(box.Rect.Bottom)
	g.x, g.y = org.X, org.Y
	// The effective size is the font size scaled by the text matrix (many generators use Tf 1).
	g.size = fs.FontSize * math.Hypot(float64(m.Matrix.C), float64(m.Matrix.D))
	if g.size <= 0 {
		g.size = g.top - g.bottom
	}
	g.rotated = math.Abs(float64(m.Matrix.B)) > 0.02*math.Abs(float64(m.Matrix.A))
	return g, true, nil
}

// spanLines measures the printed lines a byte range of Text occupies.
func (s *session) spanLines(d *Doc, start, end int) ([]textLine, error) {
	spans := d.charSpan(start, end)
	pages := make([]int, 0, len(spans))
	for p := range spans {
		pages = append(pages, p)
	}
	sort.Ints(pages)
	var lines []textLine
	for _, pg := range pages {
		p, err := s.textPage(pg)
		if err != nil {
			return nil, err
		}
		cur := -1
		for i := spans[pg][0]; i <= spans[pg][1]; i++ {
			g, ok, err := s.charAt(p, i)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if g.rotated {
				return nil, errNotHorizontal
			}
			if cur >= 0 {
				ln := &lines[cur]
				if math.Abs(g.y-ln.baseline) < 0.35*max(g.size, ln.size) && g.x >= ln.left-g.size {
					ln.left, ln.right = min(ln.left, g.left), max(ln.right, g.right)
					ln.bottom, ln.top = min(ln.bottom, g.bottom), max(ln.top, g.top)
					continue
				}
			}
			color := rgb{0, 0, 0}
			if c, err := s.in.FPDFText_GetFillColor(&requests.FPDFText_GetFillColor{TextPage: p.text, Index: i}); err == nil {
				color = rgb{c.R, c.G, c.B}
			}
			lines = append(lines, textLine{page: pg, left: g.left, right: g.right, bottom: g.bottom, top: g.top, baseline: g.y, size: g.size, color: color})
			cur = len(lines) - 1
		}
	}
	return lines, nil
}

// restOfLine returns the text that follows character last on its printed line, up to the line
// end or the start of another correction (stopped), and widens ln to cover it.
func (s *session) restOfLine(pg, last int, ln *textLine, stops map[int]bool) (tail string, stopped bool, err error) {
	p, err := s.textPage(pg)
	if err != nil {
		return "", false, err
	}
	cnt, err := s.in.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: p.text})
	if err != nil {
		return "", false, err
	}
	var b strings.Builder
	space := false
	for i := last + 1; i < cnt.Count; i++ {
		if stops[i] {
			stopped = true
			break
		}
		g, ok, err := s.charAt(p, i)
		if err != nil {
			return "", false, err
		}
		if g.r == '\r' || g.r == '\n' {
			break
		}
		if !ok {
			space = space || unicode.IsSpace(g.r)
			continue
		}
		if g.rotated || math.Abs(g.y-ln.baseline) > 0.35*ln.size {
			break // the line wrapped without an explicit break
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(g.r)
		ln.right, ln.top, ln.bottom = max(ln.right, g.right), max(ln.top, g.top), min(ln.bottom, g.bottom)
	}
	return b.String(), stopped, nil
}

// columnRight is the right edge of the paragraph around a line: the furthest-reaching nearby
// line that overlaps it horizontally (so the other column of a two-column page is ignored).
func (s *session) columnRight(ln textLine) (float64, error) {
	rects, err := s.pageRects(ln.page)
	if err != nil {
		return 0, err
	}
	right := ln.right
	for _, q := range rects {
		if q.left < ln.right && q.right > ln.left && math.Abs(q.bottom-ln.bottom) < 6*ln.size {
			right = max(right, q.right)
		}
	}
	return right, nil
}

// pageRects returns the line rectangles of all text on a page.
func (s *session) pageRects(pg int) ([]quad, error) {
	if s.rects == nil {
		s.rects = map[int][]quad{}
	}
	if r, ok := s.rects[pg]; ok {
		return r, nil
	}
	p, err := s.textPage(pg)
	if err != nil {
		return nil, err
	}
	n, err := s.in.FPDFText_CountRects(&requests.FPDFText_CountRects{TextPage: p.text, StartIndex: 0, Count: -1})
	if err != nil {
		return nil, err
	}
	out := make([]quad, 0, n.Count)
	for k := 0; k < n.Count; k++ {
		r, err := s.in.FPDFText_GetRect(&requests.FPDFText_GetRect{TextPage: p.text, Index: k})
		if err != nil {
			return nil, err
		}
		out = append(out, quad{page: pg, left: r.Left, top: r.Top, right: r.Right, bottom: r.Bottom})
	}
	s.rects[pg] = out
	return out, nil
}

// token is one word of the text being set; fix marks words of the correction itself.
type token struct {
	text  string
	fix   bool
	space bool // a space precedes it
}

func tokenize(corrected, tail string) []token {
	var out []token
	for i, w := range strings.Fields(corrected) {
		out = append(out, token{text: w, fix: true, space: i > 0})
	}
	lead := strings.HasPrefix(tail, " ")
	for i, w := range strings.Fields(tail) {
		out = append(out, token{text: w, space: i > 0 || lead})
	}
	return out
}

// lineText joins tokens into one line; fixEnd is the byte length of its corrected part.
func lineText(ts []token) (text string, fixEnd int) {
	var b strings.Builder
	for i, t := range ts {
		if i > 0 && t.space {
			b.WriteByte(' ')
		}
		b.WriteString(t.text)
		if t.fix {
			fixEnd = b.Len()
		}
	}
	return b.String(), fixEnd
}

// layoutTokens fills the lines with tokens. When the text is longer than the space, every line
// is condensed by the same factor rather than squeezing the last one.
func layoutTokens(f face, ts []token, lines []textLine) [][]token {
	out := make([][]token, len(lines))
	if len(lines) == 0 {
		return out
	}
	capacity := 0.0
	for _, ln := range lines {
		capacity += ln.capacity()
	}
	all, _ := lineText(ts)
	g := 1.0
	if need := f.width(all, lines[0].size); capacity > 0 && need > capacity {
		g = capacity / need
	}
	k := 0
	for li, ln := range lines {
		if li == len(lines)-1 {
			out[li] = ts[k:]
			break
		}
		start := k
		for k < len(ts) {
			txt, _ := lineText(ts[start : k+1])
			if k > start && f.width(txt, ln.size)*g > ln.capacity() {
				break
			}
			k++
		}
		out[li] = ts[start:k]
	}
	return out
}

// fit condenses text up to 20 % to fit the line, then shrinks the type for the rest.
func fit(f face, text string, ln textLine) (size, scaleX float64) {
	size, scaleX = ln.size, 1
	if w := f.width(text, size); w > 0 && w > ln.capacity() {
		k := ln.capacity() / w
		if k >= 0.8 {
			scaleX = k
		} else {
			scaleX, size = 0.8, size*k/0.8
		}
	}
	return size, scaleX
}

// rewrite covers the lines and sets the corrected wording, followed by the displaced rest of the
// last line. It returns where the corrected words landed, for the highlight.
func (s *session) rewrite(lines []textLine, corrected, tail string, fnt fontRef) ([]quad, error) {
	f := fnt.face
	segs := layoutTokens(f, tokenize(f.clean(corrected), f.clean(tail)), lines)
	var placed []quad
	for i, ln := range lines {
		p, err := s.page(ln.page)
		if err != nil {
			return nil, err
		}
		const pad = 0.6
		if err := s.fillRect(p, ln.left-pad, ln.bottom-pad/2, ln.right-ln.left+2*pad, ln.top-ln.bottom+pad, white); err != nil {
			return nil, err
		}
		text, fixEnd := lineText(segs[i])
		if text == "" {
			continue
		}
		size, scaleX := fit(f, text, ln)
		x := ln.left
		if fixEnd > 0 {
			if err := s.drawText(p, fnt.ref, text[:fixEnd], size, ln.color, x, ln.baseline, scaleX); err != nil {
				return nil, err
			}
			w := f.width(text[:fixEnd], size) * scaleX
			placed = append(placed, quad{page: ln.page, left: x, right: x + w, top: ln.top, bottom: ln.bottom})
			x += w
		}
		if fixEnd < len(text) {
			if err := s.drawText(p, fnt.ref, text[fixEnd:], size, ln.color, x, ln.baseline, scaleX); err != nil {
				return nil, err
			}
		}
	}
	return placed, nil
}
