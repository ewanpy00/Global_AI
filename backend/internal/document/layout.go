package document

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/structs"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// face is an embedded TrueType font (the Go fonts: Latin, Greek, Cyrillic) plus its metrics,
// used to typeset text we add to PDFs.
type face struct {
	ttf []byte
	sf  *sfnt.Font
}

var (
	facesOnce             sync.Once
	regularFace, boldFace face
	facesErr              error
)

func faces() (face, face, error) {
	facesOnce.Do(func() {
		var r, b *sfnt.Font
		if r, facesErr = sfnt.Parse(goregular.TTF); facesErr != nil {
			return
		}
		if b, facesErr = sfnt.Parse(gobold.TTF); facesErr != nil {
			return
		}
		regularFace, boldFace = face{goregular.TTF, r}, face{gobold.TTF, b}
	})
	return regularFace, boldFace, facesErr
}

// width is the advance width of s in points at the given size.
func (f face) width(s string, size float64) float64 {
	var buf sfnt.Buffer
	ppem := fixed.Int26_6(size * 64)
	var w fixed.Int26_6
	for _, r := range s {
		gi, err := f.sf.GlyphIndex(&buf, r)
		if err != nil {
			continue
		}
		adv, err := f.sf.GlyphAdvance(&buf, gi, ppem, font.HintingNone)
		if err == nil {
			w += adv
		}
	}
	return float64(w) / 64
}

// clean flattens whitespace and replaces characters the font cannot draw.
func (f face) clean(s string) string {
	var buf sfnt.Buffer
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		if gi, err := f.sf.GlyphIndex(&buf, r); err != nil || gi == 0 {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// wrap breaks text into lines no wider than maxW.
func (f face) wrap(text string, size, maxW float64) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		try := word
		if line != "" {
			try = line + " " + word
		}
		if f.width(try, size) <= maxW {
			line = try
			continue
		}
		if line != "" {
			lines = append(lines, line)
		}
		// A word longer than the line (e.g. a URL) is broken by characters.
		for f.width(word, size) > maxW {
			_, cut := utf8.DecodeRuneInString(word)
			for i := range word {
				if i > 0 && f.width(word[:i], size) > maxW {
					break
				}
				cut = max(cut, i)
			}
			lines = append(lines, word[:cut])
			word = word[cut:]
		}
		line = word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

type rgb [3]uint

var (
	inkText   = rgb{15, 23, 42}
	inkMuted  = rgb{100, 116, 139}
	inkLink   = rgb{8, 145, 178}
	inkFresh  = rgb{22, 163, 74}
	inkReview = rgb{180, 120, 0}
	inkStale  = rgb{220, 38, 38}
	white     = rgb{255, 255, 255}
)

type style struct {
	size    float64
	bold    bool
	color   rgb
	indent  float64
	leading float64 // line height as a multiple of size; 0 = 1.35
}

// writer typesets paragraphs onto new pages appended to the session's document.
type writer struct {
	s             *session
	reg, bold     references.FPDF_FONT
	regF, boldF   face
	w, h, margin  float64
	page          *openPage
	y             float64 // top of the next line, from the bottom edge
	pagesAdded    int
	firstNewIndex int
}

func (s *session) loadFont(f face) (references.FPDF_FONT, error) {
	res, err := s.in.FPDFText_LoadFont(&requests.FPDFText_LoadFont{Document: s.doc, Data: f.ttf, FontType: enums.FPDF_FONT_TRUETYPE, CID: true})
	if err != nil {
		return "", fmt.Errorf("embed font: %w", err)
	}
	return res.Font, nil
}

func (s *session) newWriter(w, h, margin float64) (*writer, error) {
	rf, bf, err := faces()
	if err != nil {
		return nil, err
	}
	reg, err := s.loadFont(rf)
	if err != nil {
		return nil, err
	}
	bold, err := s.loadFont(bf)
	if err != nil {
		return nil, err
	}
	return &writer{s: s, reg: reg, bold: bold, regF: rf, boldF: bf, w: w, h: h, margin: margin, firstNewIndex: -1}, nil
}

func (wr *writer) newPage() error {
	n, err := wr.s.pageCount()
	if err != nil {
		return err
	}
	res, err := wr.s.in.FPDFPage_New(&requests.FPDFPage_New{Document: wr.s.doc, PageIndex: n, Width: wr.w, Height: wr.h})
	if err != nil {
		return fmt.Errorf("add page: %w", err)
	}
	p := &openPage{index: n, page: res.Page, width: wr.w, height: wr.h, dirty: true}
	wr.s.pages[n] = p
	wr.page = p
	wr.y = wr.h - wr.margin
	wr.pagesAdded++
	if wr.firstNewIndex < 0 {
		wr.firstNewIndex = n
	}
	return nil
}

func (wr *writer) para(text string, st style) error {
	f, fnt := wr.regF, wr.reg
	if st.bold {
		f, fnt = wr.boldF, wr.bold
	}
	lead := st.leading
	if lead == 0 {
		lead = 1.35
	}
	lh := st.size * lead
	for _, ln := range f.wrap(f.clean(text), st.size, wr.w-2*wr.margin-st.indent) {
		if wr.page == nil || wr.y-lh < wr.margin {
			if err := wr.newPage(); err != nil {
				return err
			}
		}
		baseline := wr.y - st.size
		if err := wr.s.drawText(wr.page, fnt, ln, st.size, st.color, wr.margin+st.indent, baseline, 1); err != nil {
			return err
		}
		wr.y -= lh
	}
	return nil
}

func (wr *writer) gap(pts float64) { wr.y -= pts }

// keep starts a new page unless pts of vertical space are left, so a block is not split badly.
func (wr *writer) keep(pts float64) error {
	if wr.page == nil || wr.y-pts < wr.margin {
		return wr.newPage()
	}
	return nil
}

// drawText places one line of text with its baseline at (x, y). scaleX < 1 condenses it.
func (s *session) drawText(p *openPage, fnt references.FPDF_FONT, text string, size float64, color rgb, x, y, scaleX float64) error {
	obj, err := s.in.FPDFPageObj_CreateTextObj(&requests.FPDFPageObj_CreateTextObj{Document: s.doc, Font: fnt, FontSize: float32(size)})
	if err != nil {
		return err
	}
	if _, err := s.in.FPDFText_SetText(&requests.FPDFText_SetText{PageObject: obj.PageObject, Text: text}); err != nil {
		return err
	}
	if _, err := s.in.FPDFPageObj_SetFillColor(&requests.FPDFPageObj_SetFillColor{PageObject: obj.PageObject, FillColor: structs.FPDF_COLOR{R: color[0], G: color[1], B: color[2], A: 255}}); err != nil {
		return err
	}
	if _, err := s.in.FPDFPageObj_Transform(&requests.FPDFPageObj_Transform{PageObject: obj.PageObject, Transform: structs.FPDF_FS_MATRIX{A: float32(scaleX), D: 1, E: float32(x), F: float32(y)}}); err != nil {
		return err
	}
	if _, err := s.in.FPDFPage_InsertObject(&requests.FPDFPage_InsertObject{Page: p.ref(), PageObject: obj.PageObject}); err != nil {
		return err
	}
	p.dirty = true
	return nil
}

// fillRect paints a filled rectangle (no stroke) in page space.
func (s *session) fillRect(p *openPage, x, y, w, h float64, color rgb) error {
	obj, err := s.in.FPDFPageObj_CreateNewRect(&requests.FPDFPageObj_CreateNewRect{X: float32(x), Y: float32(y), W: float32(w), H: float32(h)})
	if err != nil {
		return err
	}
	if _, err := s.in.FPDFPageObj_SetFillColor(&requests.FPDFPageObj_SetFillColor{PageObject: obj.PageObject, FillColor: structs.FPDF_COLOR{R: color[0], G: color[1], B: color[2], A: 255}}); err != nil {
		return err
	}
	if _, err := s.in.FPDFPath_SetDrawMode(&requests.FPDFPath_SetDrawMode{PageObject: obj.PageObject, FillMode: enums.FPDF_FILLMODE_WINDING, Stroke: false}); err != nil {
		return err
	}
	if _, err := s.in.FPDFPage_InsertObject(&requests.FPDFPage_InsertObject{Page: p.ref(), PageObject: obj.PageObject}); err != nil {
		return err
	}
	p.dirty = true
	return nil
}

// TextPDF typesets a short Markdown-like text as an A4 PDF: "# " and "## " headings,
// "- " bullets, paragraphs separated by blank lines, ** markers dropped. It produces the
// bundled sample document, so it stands in for "a report an agent generated".
func (e *Engine) TextPDF(ctx context.Context, title, text string) ([]byte, error) {
	var out []byte
	err := e.open(ctx, nil, func(s *session) error {
		wr, err := s.newWriter(595, 842, 64)
		if err != nil {
			return err
		}
		if title != "" {
			if err := wr.para(title, style{size: 20, bold: true, color: inkText, leading: 1.2}); err != nil {
				return err
			}
			wr.gap(10)
		}
		for _, block := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
			block = strings.TrimSpace(strings.ReplaceAll(block, "**", ""))
			if block == "" {
				continue
			}
			for _, ln := range strings.Split(block, "\n") {
				ln = strings.TrimSpace(ln)
				var err error
				switch {
				case strings.HasPrefix(ln, "## "):
					wr.gap(4)
					err = wr.para(ln[3:], style{size: 13, bold: true, color: inkText})
				case strings.HasPrefix(ln, "# "):
					wr.gap(6)
					err = wr.para(ln[2:], style{size: 16, bold: true, color: inkText})
				case strings.HasPrefix(ln, "- "), strings.HasPrefix(ln, "* "):
					err = wr.para("•  "+ln[2:], style{size: 11, color: inkText, indent: 8, leading: 1.45})
				default:
					err = wr.para(ln, style{size: 11, color: inkText, leading: 1.45})
				}
				if err != nil {
					return err
				}
			}
			wr.gap(9)
		}
		out, err = s.save()
		return err
	})
	return out, err
}
