package document

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/klippa-app/go-pdfium/requests"

	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

const (
	// MaxBytes bounds an upload.
	MaxBytes = 25 << 20
	// MaxPages bounds how many pages are read. Text beyond verify.MaxAnswerChars is not checked.
	MaxPages = 60
)

// ErrNoText means the PDF has no text layer, e.g. a scan.
var ErrNoText = errors.New("this PDF has no extractable text (a scan or an image-only PDF); OCR is not supported yet")

type Page struct {
	Index  int     `json:"index"`
	Width  float64 `json:"width"` // points, as displayed (rotation applied)
	Height float64 `json:"height"`
}

// Doc is an uploaded PDF and its text layer. Text is what the pipeline checks; every rune of it
// that came from the PDF maps back to a PDFium character, so claim spans can be drawn on pages.
type Doc struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Size         int    `json:"size"`
	Pages        []Page `json:"pages"`
	TotalPages   int    `json:"total_pages"`
	CheckedPages int    `json:"checked_pages"` // pages whose text is included in Text
	Text         string `json:"text"`

	Data []byte `json:"-"`

	// offs[i] is the byte offset in Text of a rune that came from character refs[i].
	offs []int
	refs []charRef

	mu     sync.Mutex
	report *verify.Report
	boxes  map[string][]Box
	images map[int][]byte
}

type charRef struct {
	page, index int
}

// Result returns the last verification of this document, if any.
func (d *Doc) Result() (*verify.Report, map[string][]Box) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.report, d.boxes
}

// SetResult stores a finished verification so the annotated and corrected files can be built.
func (d *Doc) SetResult(r *verify.Report, boxes map[string][]Box) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.report, d.boxes = r, boxes
}

// Load reads a PDF: page sizes and the text layer of as many pages as one verification run
// can take.
func (e *Engine) Load(ctx context.Context, name string, data []byte) (*Doc, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("file is larger than %d MB", MaxBytes>>20)
	}
	if !bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF-")) {
		return nil, errors.New("not a PDF file")
	}
	d := &Doc{ID: newID(), Name: name, Size: len(data), Data: data, images: map[int][]byte{}}
	err := e.open(ctx, data, func(s *session) error {
		n, err := s.pageCount()
		if err != nil {
			return err
		}
		d.TotalPages = n
		var text strings.Builder
		runes := 0
		for i := 0; i < n && i < MaxPages; i++ {
			p, err := s.textPage(i)
			if err != nil {
				return err
			}
			d.Pages = append(d.Pages, Page{Index: i, Width: p.width, Height: p.height})
			if d.CheckedPages < i {
				continue // an earlier page already hit the length limit; keep reading sizes only
			}
			pageText, refs, err := s.pageText(p)
			if err != nil {
				return err
			}
			sep := ""
			if text.Len() > 0 && strings.TrimSpace(pageText) != "" {
				sep = "\n\n"
			}
			add := utf8.RuneCountInString(sep + pageText)
			if runes+add > verify.MaxAnswerChars {
				if i > 0 {
					continue
				}
				// A single huge first page: keep what fits.
				pageText, refs = truncateRunes(pageText, refs, verify.MaxAnswerChars)
			}
			text.WriteString(sep)
			base := text.Len()
			for j := range refs {
				d.offs = append(d.offs, base+refs[j].off)
				d.refs = append(d.refs, charRef{page: i, index: refs[j].index})
			}
			text.WriteString(pageText)
			runes += add
			d.CheckedPages = i + 1
		}
		d.setText(text.String())
		return nil
	})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(d.Text) == "" {
		return nil, ErrNoText
	}
	return d, nil
}

// setText stores the text trimmed the same way the pipeline trims its input, so claim offsets
// from a report index Text directly.
func (d *Doc) setText(full string) {
	lead := len(full) - len(strings.TrimLeftFunc(full, unicode.IsSpace))
	d.Text = strings.TrimSpace(full)
	offs, refs := d.offs[:0], d.refs[:0]
	for i, off := range d.offs {
		if off >= lead && off-lead < len(d.Text) {
			offs = append(offs, off-lead)
			refs = append(refs, d.refs[i])
		}
	}
	d.offs, d.refs = offs, refs
}

type runeRef struct {
	off   int // byte offset in the page text
	index int // PDFium character index
}

// pageText returns the page's text and, for every rune, the PDFium character it came from.
// PDFium inserts generated spaces and CR LF line breaks; CR is dropped.
func (s *session) pageText(p *openPage) (string, []runeRef, error) {
	cnt, err := s.in.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: p.text})
	if err != nil {
		return "", nil, err
	}
	if cnt.Count <= 0 {
		return "", nil, nil
	}
	var b strings.Builder
	refs := make([]runeRef, 0, cnt.Count)
	add := func(r rune, index int) {
		switch {
		case r == '\r' || r == 0 || r == 0xFFFE || r == 0xFFFF || !utf8.ValidRune(r):
			return
		case r == 2:
			r = '-' // PDFium marks some line-end hyphens with STX
		case r < 0x20 && r != '\n' && r != '\t':
			return
		}
		refs = append(refs, runeRef{off: b.Len(), index: index})
		b.WriteRune(r)
	}

	// Fast path: one call for the whole page, valid when it has one rune per character.
	all, err := s.in.FPDFText_GetText(&requests.FPDFText_GetText{TextPage: p.text, StartIndex: 0, Count: cnt.Count})
	if err == nil && utf8.RuneCountInString(all.Text) == cnt.Count {
		i := 0
		for _, r := range all.Text {
			add(r, i)
			i++
		}
		return b.String(), refs, nil
	}
	// Slow path: characters PDFium cannot map to Unicode are missing from GetText.
	for i := 0; i < cnt.Count; i++ {
		u, err := s.in.FPDFText_GetUnicode(&requests.FPDFText_GetUnicode{TextPage: p.text, Index: i})
		if err != nil {
			return "", nil, err
		}
		add(rune(u.Unicode), i)
	}
	return b.String(), refs, nil
}

func truncateRunes(s string, refs []runeRef, n int) (string, []runeRef) {
	if n >= len(refs) {
		return s, refs
	}
	return s[:refs[n].off], refs[:n]
}

// charSpan maps a byte range of Text to the PDFium characters it covers: page → [first, last].
func (d *Doc) charSpan(start, end int) map[int][2]int {
	out := map[int][2]int{}
	for i := sort.SearchInts(d.offs, start); i < len(d.offs) && d.offs[i] < end; i++ {
		r := d.refs[i]
		cur, ok := out[r.page]
		if !ok {
			out[r.page] = [2]int{r.index, r.index}
			continue
		}
		cur[0], cur[1] = min(cur[0], r.index), max(cur[1], r.index)
		out[r.page] = cur
	}
	return out
}

// pageOf returns the 0-based page where a byte offset of Text lies, or -1.
func (d *Doc) pageOf(off int) int {
	i := sort.SearchInts(d.offs, off)
	if i < len(d.refs) {
		return d.refs[i].page
	}
	return -1
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Store keeps recent documents in memory, evicting the oldest beyond max documents or
// maxBytes of PDF data in total (page images are not counted).
type Store struct {
	mu       sync.Mutex
	docs     map[string]*Doc
	order    []string
	max      int
	maxBytes int
	bytes    int
}

func NewStore(max int) *Store {
	return &Store{docs: map[string]*Doc{}, max: max, maxBytes: 256 << 20}
}

func (s *Store) Put(d *Doc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.docs[d.ID]; ok {
		s.bytes -= len(old.Data)
	} else {
		s.order = append(s.order, d.ID)
	}
	s.docs[d.ID] = d
	s.bytes += len(d.Data)
	for len(s.order) > 1 && (len(s.order) > s.max || s.bytes > s.maxBytes) {
		id := s.order[0]
		s.order = s.order[1:]
		if old, ok := s.docs[id]; ok {
			s.bytes -= len(old.Data)
			delete(s.docs, id)
		}
	}
}

func (s *Store) Get(id string) (*Doc, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[id]
	return d, ok
}
