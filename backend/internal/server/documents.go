package server

// Document endpoints (PDF):
//
//	POST /api/documents                  upload a PDF (multipart field "file", or a raw application/pdf body)
//	POST /api/documents/sample           the first sample answer typeset as a PDF
//	GET  /api/documents/{id}/pages/{n}   page n (1-based) rendered as PNG
//	GET  /api/documents/{id}/file        the original PDF
//	POST /api/documents/{id}/verify      check the text: SSE like /api/verify plus a "boxes" event
//	GET  /api/documents/{id}/annotated   PDF with a highlight and note per claim and a report page
//	GET  /api/documents/{id}/corrected   PDF with outdated claims rewritten in place (?claims=c1,c3)
//	POST /api/documents/check            one call for agents: PDF in, annotated/corrected PDF or JSON out

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/ewanpy00/Global_AI/backend/internal/document"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

// EvBoxes carries the highlight rectangles of each claim on the PDF pages.
const EvBoxes = "boxes"

func (s *Server) documentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/documents", s.handleDocUpload)
	mux.HandleFunc("POST /api/documents/sample", s.handleDocSample)
	mux.HandleFunc("POST /api/documents/check", s.handleDocCheck)
	mux.HandleFunc("GET /api/documents/{id}/pages/{n}", s.handleDocPage)
	mux.HandleFunc("GET /api/documents/{id}/file", s.handleDocFile)
	mux.HandleFunc("POST /api/documents/{id}/verify", s.handleDocVerify)
	mux.HandleFunc("GET /api/documents/{id}/annotated", s.handleDocAnnotated)
	mux.HandleFunc("GET /api/documents/{id}/corrected", s.handleDocCorrected)
}

func (s *Server) docsReady(w http.ResponseWriter) bool {
	if s.Docs == nil || s.DocStore == nil {
		writeError(w, http.StatusServiceUnavailable, "document support is not enabled")
		return false
	}
	return true
}

// readPDF accepts a multipart upload (field "file") or the raw PDF as the request body.
func readPDF(w http.ResponseWriter, r *http.Request) (string, []byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, document.MaxBytes+1<<20)
	name := r.URL.Query().Get("name")
	var data []byte
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct == "multipart/form-data" {
		f, hdr, err := r.FormFile("file")
		if err != nil {
			return "", nil, fmt.Errorf("multipart field \"file\": %w", err)
		}
		defer f.Close()
		if data, err = io.ReadAll(f); err != nil {
			return "", nil, err
		}
		if name == "" {
			name = hdr.Filename
		}
	} else {
		var err error
		if data, err = io.ReadAll(r.Body); err != nil {
			return "", nil, err
		}
	}
	if len(data) == 0 {
		return "", nil, errors.New("empty upload")
	}
	return cleanName(name), data, nil
}

func cleanName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' || r == '\\' || r == '/' {
			return -1
		}
		return r
	}, filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "" || name == "." || name == "/" {
		name = "document.pdf"
	}
	return name
}

func (s *Server) handleDocUpload(w http.ResponseWriter, r *http.Request) {
	if !s.docsReady(w) {
		return
	}
	name, data, err := readPDF(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d, err := s.Docs.Load(r.Context(), name, data)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.DocStore.Put(d)
	writeJSON(w, http.StatusOK, d)
}

// samplePDF caches the typeset sample document.
type samplePDF struct {
	once sync.Once
	data []byte
	name string
	err  error
}

// handleDocSample typesets the first sample answer as a PDF, as if an agent had produced it.
func (s *Server) handleDocSample(w http.ResponseWriter, r *http.Request) {
	if !s.docsReady(w) {
		return
	}
	if len(s.Samples) == 0 {
		writeError(w, http.StatusNotFound, "no sample configured")
		return
	}
	sp := &s.sample
	sp.once.Do(func() {
		sm := s.Samples[0]
		heading := sm.Heading
		if heading == "" {
			heading = sm.Title
		}
		sp.name = sm.ID + "-report.pdf"
		sp.data, sp.err = s.Docs.TextPDF(context.WithoutCancel(r.Context()), heading, sm.Text)
	})
	if sp.err != nil {
		writeError(w, http.StatusInternalServerError, sp.err.Error())
		return
	}
	d, err := s.Docs.Load(r.Context(), sp.name, sp.data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.DocStore.Put(d)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) docFor(w http.ResponseWriter, r *http.Request) *document.Doc {
	if !s.docsReady(w) {
		return nil
	}
	d, ok := s.DocStore.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "document not found (documents are kept in memory; upload it again)")
		return nil
	}
	return d
}

func (s *Server) handleDocPage(w http.ResponseWriter, r *http.Request) {
	d := s.docFor(w, r)
	if d == nil {
		return
	}
	n, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("n"), ".png"))
	if err != nil || n < 1 || n > len(d.Pages) {
		writeError(w, http.StatusNotFound, "no such page")
		return
	}
	img, err := s.Docs.PagePNG(r.Context(), d, n-1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(img)
}

func (s *Server) handleDocFile(w http.ResponseWriter, r *http.Request) {
	d := s.docFor(w, r)
	if d == nil {
		return
	}
	writePDF(w, r, d.Data, d.Name, nil)
}

// handleDocVerify runs the pipeline on the document text and streams it like /api/verify. Right
// after the claims are found it sends their positions on the pages, so the viewer can draw them
// while they are being checked.
func (s *Server) handleDocVerify(w http.ResponseWriter, r *http.Request) {
	d := s.docFor(w, r)
	if d == nil {
		return
	}
	sse, err := newSSE(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.Cfg.VerifyTimeout)
	defer cancel()

	var boxes map[string][]document.Box
	report, err := s.Engine.Run(ctx, d.Text, func(ev verify.Event) {
		sse.send(ev.Type, ev.Data)
		if ev.Type != verify.EvClaims {
			return
		}
		data, _ := ev.Data.(map[string]any)
		claims, _ := data["claims"].([]verify.Claim)
		b, err := s.Docs.Boxes(ctx, d, claims)
		if err != nil {
			log.Printf("document %s: locate claims: %v", d.ID, err)
			return
		}
		boxes = b
		sse.send(EvBoxes, map[string]any{"boxes": b})
	})
	if err != nil {
		sse.send(verify.EvError, map[string]string{"message": err.Error()})
		return
	}
	d.SetResult(report, boxes)
	sse.send(verify.EvReport, report)
}

func (s *Server) checkedDoc(w http.ResponseWriter, r *http.Request) (*document.Doc, *verify.Report) {
	d := s.docFor(w, r)
	if d == nil {
		return nil, nil
	}
	report, _ := d.Result()
	if report == nil {
		writeError(w, http.StatusConflict, "check the document first: POST /api/documents/"+d.ID+"/verify")
		return nil, nil
	}
	return d, report
}

func (s *Server) handleDocAnnotated(w http.ResponseWriter, r *http.Request) {
	d, report := s.checkedDoc(w, r)
	if d == nil {
		return
	}
	out, err := s.Docs.Annotated(r.Context(), d, report)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writePDF(w, r, out, withSuffix(d.Name, "annotated"), report)
}

func (s *Server) handleDocCorrected(w http.ResponseWriter, r *http.Request) {
	d, report := s.checkedDoc(w, r)
	if d == nil {
		return
	}
	var only map[string]bool
	if q := r.URL.Query().Get("claims"); q != "" {
		only = map[string]bool{}
		for _, id := range strings.Split(q, ",") {
			only[strings.TrimSpace(id)] = true
		}
	}
	out, edits, err := s.Docs.Corrected(r.Context(), d, report, only)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("X-Freshgate-Edits", strconv.Itoa(len(edits)))
	writePDF(w, r, out, withSuffix(d.Name, "corrected"), report)
}

// handleDocCheck is the one-call API for agents: send the PDF you generated, get it back checked.
// ?output=annotated (default) | corrected | json
func (s *Server) handleDocCheck(w http.ResponseWriter, r *http.Request) {
	if !s.docsReady(w) {
		return
	}
	output := r.URL.Query().Get("output")
	switch output {
	case "":
		output = "annotated"
	case "annotated", "corrected", "json":
	default:
		writeError(w, http.StatusBadRequest, "output must be annotated, corrected or json")
		return
	}
	name, data, err := readPDF(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.Cfg.VerifyTimeout)
	defer cancel()
	d, err := s.Docs.Load(ctx, name, data)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	report, err := s.Engine.Run(ctx, d.Text, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	claims := make([]verify.Claim, len(report.Claims))
	for i, c := range report.Claims {
		claims[i] = c.Claim
	}
	boxes, err := s.Docs.Boxes(ctx, d, claims)
	if err != nil {
		log.Printf("document %s: locate claims: %v", d.ID, err)
	}
	d.SetResult(report, boxes)
	s.DocStore.Put(d) // the other formats stay available under /api/documents/{id}/...
	w.Header().Set("X-Freshgate-Document", d.ID)

	switch output {
	case "json":
		setScoreHeaders(w, report)
		writeJSON(w, http.StatusOK, map[string]any{"document": d, "report": report, "boxes": boxes})
	case "corrected":
		out, edits, err := s.Docs.Corrected(ctx, d, report, nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("X-Freshgate-Edits", strconv.Itoa(len(edits)))
		writePDF(w, r, out, withSuffix(d.Name, "corrected"), report)
	default:
		out, err := s.Docs.Annotated(ctx, d, report)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writePDF(w, r, out, withSuffix(d.Name, "annotated"), report)
	}
}

func setScoreHeaders(w http.ResponseWriter, r *verify.Report) {
	if r != nil {
		w.Header().Set("X-Freshgate-Score", strconv.Itoa(r.Summary.Score))
		w.Header().Set("X-Freshgate-Label", string(r.Summary.Label))
	}
}

// writePDF sends a PDF as a download, or inline with ?inline=1.
func writePDF(w http.ResponseWriter, r *http.Request, data []byte, name string, report *verify.Report) {
	setScoreHeaders(w, report)
	disp := "attachment"
	if r.URL.Query().Get("inline") == "1" {
		disp = "inline"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func withSuffix(name, suffix string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return base + "-" + suffix + ".pdf"
}
