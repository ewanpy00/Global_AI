package document

import (
	"context"
	"math"
	"sort"

	"github.com/klippa-app/go-pdfium/requests"

	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

// Box is a highlight rectangle on a page as fractions of the displayed page size, measured from
// the top-left corner, so the browser can place it over the page image at any zoom.
type Box struct {
	Page int     `json:"page"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
}

// quad is a text rectangle in PDF page space (origin bottom-left, points).
type quad struct {
	page                     int
	left, top, right, bottom float64
}

// spanQuads returns one rectangle per text line that the byte range of Text covers. PDFium
// merges the characters of each line, the same way Chrome draws search hits.
func (s *session) spanQuads(d *Doc, start, end int) ([]quad, error) {
	spans := d.charSpan(start, end)
	pages := make([]int, 0, len(spans))
	for p := range spans {
		pages = append(pages, p)
	}
	sort.Ints(pages)
	var out []quad
	for _, pg := range pages {
		p, err := s.textPage(pg)
		if err != nil {
			return nil, err
		}
		first, last := spans[pg][0], spans[pg][1]
		n, err := s.in.FPDFText_CountRects(&requests.FPDFText_CountRects{TextPage: p.text, StartIndex: first, Count: last - first + 1})
		if err != nil {
			return nil, err
		}
		for k := 0; k < n.Count; k++ {
			r, err := s.in.FPDFText_GetRect(&requests.FPDFText_GetRect{TextPage: p.text, Index: k})
			if err != nil {
				return nil, err
			}
			if r.Right-r.Left < 0.5 || r.Top-r.Bottom < 0.5 {
				continue // generated line breaks have empty boxes
			}
			out = append(out, quad{page: pg, left: r.Left, top: r.Top, right: r.Right, bottom: r.Bottom})
		}
	}
	return out, nil
}

// box converts a page-space rectangle to display fractions. PDFium's display matrix accounts
// for the crop box and page rotation, exactly as the rendered image does.
func (s *session) box(q quad) (Box, error) {
	p, err := s.page(q.page)
	if err != nil {
		return Box{}, err
	}
	sx, sy := int(math.Round(p.width*100)), int(math.Round(p.height*100))
	conv := func(x, y float64) (float64, float64, error) {
		r, err := s.in.FPDF_PageToDevice(&requests.FPDF_PageToDevice{Page: p.ref(), SizeX: sx, SizeY: sy, PageX: x, PageY: y})
		if err != nil {
			return 0, 0, err
		}
		return float64(r.DeviceX), float64(r.DeviceY), nil
	}
	x0, y0, err := conv(q.left, q.top)
	if err != nil {
		return Box{}, err
	}
	x1, y1, err := conv(q.right, q.bottom)
	if err != nil {
		return Box{}, err
	}
	return Box{
		Page: q.page,
		X:    math.Min(x0, x1) / float64(sx),
		Y:    math.Min(y0, y1) / float64(sy),
		W:    math.Abs(x1-x0) / float64(sx),
		H:    math.Abs(y1-y0) / float64(sy),
	}, nil
}

// Boxes maps every located claim to its highlight boxes.
func (e *Engine) Boxes(ctx context.Context, d *Doc, claims []verify.Claim) (map[string][]Box, error) {
	out := map[string][]Box{}
	err := e.open(ctx, d.Data, func(s *session) error {
		for _, c := range claims {
			if !c.Located {
				continue
			}
			start, end := c.Span()
			quads, err := s.spanQuads(d, start, end)
			if err != nil {
				return err
			}
			for _, q := range quads {
				b, err := s.box(q)
				if err != nil {
					return err
				}
				out[c.ID] = append(out[c.ID], b)
			}
		}
		return nil
	})
	return out, err
}
