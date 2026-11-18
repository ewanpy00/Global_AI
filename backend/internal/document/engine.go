// Package document checks PDF files. It uses PDFium compiled to WebAssembly (run in-process by
// wazero, so no CGO, external binaries or network) to extract text with the position of every
// character, render pages for the browser, and write the results back into the PDF: review
// highlights with notes, a report page, and in-place corrections.
package document

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// Engine owns the PDFium runtime. It is safe for concurrent use: every operation borrows its
// own PDFium instance from a small pool.
type Engine struct {
	once sync.Once
	pool pdfium.Pool
	err  error
}

func NewEngine() *Engine { return &Engine{} }

// Warmup compiles the WebAssembly module in the background (about two seconds), so the first
// upload does not pay for it.
func (e *Engine) Warmup() { go func() { _ = e.init() }() }

func (e *Engine) init() error {
	e.once.Do(func() {
		e.pool, e.err = webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 2, MaxTotal: 4})
	})
	return e.err
}

// Close releases the runtime. The engine cannot be used afterwards.
func (e *Engine) Close() error {
	e.once.Do(func() { e.err = errors.New("document engine closed") })
	if e.pool != nil {
		return e.pool.Close()
	}
	return nil
}

const opTimeout = 60 * time.Second

// session is one PDFium instance with one open document. Pages are loaded lazily and closed
// together with the document.
type session struct {
	in    pdfium.Pdfium
	doc   references.FPDF_DOCUMENT
	pages map[int]*openPage
	rects map[int][]quad // all text line rectangles per page, loaded on demand
}

type openPage struct {
	index         int
	page          references.FPDF_PAGE
	text          references.FPDF_TEXTPAGE
	hasText       bool
	width, height float64
	dirty         bool // page objects were added, content must be regenerated before saving
}

func (p *openPage) ref() requests.Page { return requests.Page{ByReference: &p.page} }

// open runs fn with data opened as a document, or with a new empty document when data is nil.
func (e *Engine) open(ctx context.Context, data []byte, fn func(*session) error) error {
	if err := e.init(); err != nil {
		return fmt.Errorf("pdf engine: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	in, err := e.pool.GetInstanceWithContext(ctx)
	if err != nil {
		return fmt.Errorf("pdf engine busy: %w", err)
	}
	defer in.Close()

	s := &session{in: in, pages: map[int]*openPage{}}
	if data == nil {
		res, err := in.FPDF_CreateNewDocument(&requests.FPDF_CreateNewDocument{})
		if err != nil {
			return fmt.Errorf("create PDF: %w", err)
		}
		s.doc = res.Document
	} else {
		res, err := in.OpenDocument(&requests.OpenDocument{File: &data})
		if err != nil {
			return fmt.Errorf("cannot open PDF (damaged or password-protected?): %w", err)
		}
		s.doc = res.Document
	}
	defer s.close()
	return fn(s)
}

func (s *session) close() {
	for _, p := range s.pages {
		if p.hasText {
			_, _ = s.in.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: p.text})
		}
		_, _ = s.in.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: p.page})
	}
	_, _ = s.in.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: s.doc})
}

func (s *session) pageCount() (int, error) {
	res, err := s.in.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: s.doc})
	if err != nil {
		return 0, err
	}
	return res.PageCount, nil
}

func (s *session) page(i int) (*openPage, error) {
	if p, ok := s.pages[i]; ok {
		return p, nil
	}
	res, err := s.in.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: s.doc, Index: i})
	if err != nil {
		return nil, fmt.Errorf("load page %d: %w", i+1, err)
	}
	p := &openPage{index: i, page: res.Page}
	s.pages[i] = p
	w, err := s.in.FPDF_GetPageWidthF(&requests.FPDF_GetPageWidthF{Page: p.ref()})
	if err != nil {
		return nil, err
	}
	h, err := s.in.FPDF_GetPageHeightF(&requests.FPDF_GetPageHeightF{Page: p.ref()})
	if err != nil {
		return nil, err
	}
	p.width, p.height = float64(w.PageWidth), float64(h.PageHeight)
	return p, nil
}

// textPage returns the page with its text layer loaded.
func (s *session) textPage(i int) (*openPage, error) {
	p, err := s.page(i)
	if err != nil {
		return nil, err
	}
	if !p.hasText {
		res, err := s.in.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: p.ref()})
		if err != nil {
			return nil, fmt.Errorf("read text of page %d: %w", i+1, err)
		}
		p.text, p.hasText = res.TextPage, true
	}
	return p, nil
}

// save regenerates the content of changed pages and writes a complete new file.
func (s *session) save() ([]byte, error) {
	for _, p := range s.pages {
		if p.dirty {
			if _, err := s.in.FPDFPage_GenerateContent(&requests.FPDFPage_GenerateContent{Page: p.ref()}); err != nil {
				return nil, fmt.Errorf("write page %d: %w", p.index+1, err)
			}
			p.dirty = false
		}
	}
	res, err := s.in.FPDF_SaveAsCopy(&requests.FPDF_SaveAsCopy{Document: s.doc, Flags: requests.SaveFlagNoIncremental})
	if err != nil {
		return nil, fmt.Errorf("save PDF: %w", err)
	}
	if res.FileBytes == nil {
		return nil, errors.New("save PDF: no output")
	}
	return *res.FileBytes, nil
}
