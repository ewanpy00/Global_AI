package document

import (
	"bytes"
	"context"
	"fmt"
	"image/png"

	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/requests"
)

// RenderDPI gives sharp pages on high-density screens at a moderate file size.
const RenderDPI = 144

var pngEncoder = png.Encoder{CompressionLevel: png.BestSpeed}

// PagePNG renders one page of the original document. Results are cached on the Doc.
func (e *Engine) PagePNG(ctx context.Context, d *Doc, index int) ([]byte, error) {
	if index < 0 || index >= len(d.Pages) {
		return nil, fmt.Errorf("page %d does not exist", index+1)
	}
	d.mu.Lock()
	cached, ok := d.images[index]
	d.mu.Unlock()
	if ok {
		return cached, nil
	}
	var out []byte
	err := e.open(ctx, d.Data, func(s *session) error {
		p, err := s.page(index)
		if err != nil {
			return err
		}
		res, err := s.in.RenderPageInDPI(&requests.RenderPageInDPI{
			Page:        p.ref(),
			DPI:         RenderDPI,
			RenderFlags: enums.FPDF_RENDER_FLAG_ANNOT,
		})
		if err != nil {
			return fmt.Errorf("render page %d: %w", index+1, err)
		}
		defer res.CleanupFunc()
		var buf bytes.Buffer
		if err := pngEncoder.Encode(&buf, res.Result.RenderedImage); err != nil {
			return err
		}
		out = buf.Bytes()
		return nil
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.images[index] = out
	d.mu.Unlock()
	return out, nil
}
