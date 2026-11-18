package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/config"
	"github.com/ewanpy00/Global_AI/backend/internal/demo"
	"github.com/ewanpy00/Global_AI/backend/internal/document"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

// One PDFium runtime for all tests in the package (compiling it takes a couple of seconds).
var testDocs = document.NewEngine()

func newDocServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := &Server{
		Cfg:      &config.Config{Demo: true, VerifyTimeout: time.Minute, WebDir: t.TempDir()},
		Engine:   &verify.Engine{LLM: &demo.LLM{}, Search: &demo.Search{}, MaxClaims: 12, Concurrency: 4},
		Samples:  DefaultSamples(true),
		Docs:     testDocs,
		DocStore: document.NewStore(8),
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url, contentType string, body io.Reader) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

type docJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Text  string `json:"text"`
	Pages []struct {
		Width float64 `json:"width"`
	} `json:"pages"`
}

func TestDocumentFlow(t *testing.T) {
	ts := newDocServer(t)

	res, body := do(t, "POST", ts.URL+"/api/documents/sample", "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("sample: %d %s", res.StatusCode, body)
	}
	var doc docJSON
	if err := json.Unmarshal(body, &doc); err != nil || doc.ID == "" || len(doc.Pages) != 1 || !strings.Contains(doc.Text, "Nimbus") {
		t.Fatalf("sample doc: %v %s", err, body)
	}
	base := ts.URL + "/api/documents/" + doc.ID

	if res, body := do(t, "GET", base+"/pages/1", "", nil); res.StatusCode != 200 || !bytes.HasPrefix(body, []byte("\x89PNG")) {
		t.Fatalf("page image: %d", res.StatusCode)
	}
	if res, _ := do(t, "GET", base+"/pages/2", "", nil); res.StatusCode != 404 {
		t.Fatalf("missing page: %d", res.StatusCode)
	}
	if res, _ := do(t, "GET", base+"/annotated", "", nil); res.StatusCode != http.StatusConflict {
		t.Fatalf("annotated before verify: %d", res.StatusCode)
	}

	res, body = do(t, "POST", base+"/verify", "", nil)
	stream := string(body)
	if res.StatusCode != 200 || !strings.Contains(stream, "event: report") || strings.Contains(stream, "event: error") {
		t.Fatalf("verify stream:\n%s", stream)
	}
	var boxes struct {
		Boxes map[string][]document.Box `json:"boxes"`
	}
	for _, block := range strings.Split(stream, "\n\n") {
		if strings.HasPrefix(block, "event: boxes\ndata: ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "event: boxes\ndata: ")), &boxes); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(boxes.Boxes) != 8 {
		t.Fatalf("got boxes for %d claims, want 8", len(boxes.Boxes))
	}
	// Boxes arrive before the first verdict, so the viewer can draw pending highlights.
	if strings.Index(stream, "event: boxes") > strings.Index(stream, "event: claim_result") {
		t.Fatal("boxes should be sent before the claim results")
	}

	res, body = do(t, "GET", base+"/annotated", "", nil)
	if res.StatusCode != 200 || !bytes.HasPrefix(body, []byte("%PDF-")) || res.Header.Get("X-Freshgate-Label") != "stale" {
		t.Fatalf("annotated: %d %v", res.StatusCode, res.Header)
	}
	if cd := res.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "nimbus-report-annotated.pdf") {
		t.Fatalf("content disposition %q", cd)
	}
	if res, _ := do(t, "GET", base+"/corrected?claims=c1&inline=1", "", nil); res.StatusCode != 200 || res.Header.Get("X-Freshgate-Edits") != "1" ||
		!strings.HasPrefix(res.Header.Get("Content-Disposition"), "inline") {
		t.Fatalf("corrected c1 only: %d %v", res.StatusCode, res.Header)
	}

	// The agent API: raw PDF in, corrected PDF or JSON out, in one call.
	_, original := do(t, "GET", base+"/file", "", nil)
	res, body = do(t, "POST", ts.URL+"/api/documents/check?output=corrected&name=agent.pdf", "application/pdf", bytes.NewReader(original))
	if res.StatusCode != 200 || res.Header.Get("X-Freshgate-Edits") != "4" || !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Fatalf("check corrected: %d %v %.200s", res.StatusCode, res.Header, body)
	}
	if res.Header.Get("X-Freshgate-Document") == "" {
		t.Fatal("check should return the stored document id")
	}

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	fw, _ := mw.CreateFormFile("file", "agent output.pdf")
	_, _ = fw.Write(original)
	_ = mw.Close()
	res, body = do(t, "POST", ts.URL+"/api/documents/check?output=json", mw.FormDataContentType(), &form)
	var out struct {
		Document docJSON                   `json:"document"`
		Report   verify.Report             `json:"report"`
		Boxes    map[string][]document.Box `json:"boxes"`
	}
	if err := json.Unmarshal(body, &out); err != nil || res.StatusCode != 200 {
		t.Fatalf("check json: %d %v %.300s", res.StatusCode, err, body)
	}
	if len(out.Report.Claims) != 8 || len(out.Boxes) != 8 || out.Document.Name != "agent output.pdf" {
		t.Fatalf("check json: %d claims, %d boxed, name %q", len(out.Report.Claims), len(out.Boxes), out.Document.Name)
	}
}

func TestDocumentUploadRejectsNonPDF(t *testing.T) {
	ts := newDocServer(t)
	res, body := do(t, "POST", ts.URL+"/api/documents", "application/pdf", strings.NewReader("just text"))
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "not a PDF") {
		t.Fatalf("got %d %s", res.StatusCode, body)
	}
	if res, _ := do(t, "GET", ts.URL+"/api/documents/nope/annotated", "", nil); res.StatusCode != 404 {
		t.Fatalf("unknown document: %d", res.StatusCode)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":   "passwd",
		`C:\docs\report.pdf`: "report.pdf",
		"a\"b\n.pdf":         "ab.pdf",
		"":                   "document.pdf",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}
