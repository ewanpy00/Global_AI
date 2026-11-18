// Package plugin holds the Bifrost plugin that turns Freshgate into a connector:
// every chat completion that passes through Bifrost is fact-checked for stale claims
// before the response reaches the client.
package plugin

import (
	"context"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/ewanpy00/Global_AI/backend/internal/llm"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

// Mode controls what the plugin does with a response.
type Mode string

const (
	ModeOff      Mode = "off"      // pass through untouched
	ModeReport   Mode = "report"   // attach the report to the request context only (returned as JSON field / headers)
	ModeAnnotate Mode = "annotate" // also append a Markdown freshness footnote to the answer
	ModeCorrect  Mode = "correct"  // also rewrite outdated claims in place and list the corrections
)

func ParseMode(s string) (Mode, bool) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case ModeOff, ModeReport, ModeAnnotate, ModeCorrect:
		return m, true
	}
	return "", false
}

type contextKey string

const (
	ctxMode   contextKey = "freshgate.mode"
	ctxReport contextKey = "freshgate.report"
	ctxError  contextKey = "freshgate.error"
)

// WithMode sets the mode for one request (the connector reads it from the X-Freshgate-Mode header).
func WithMode(ctx *schemas.BifrostContext, m Mode) { ctx.SetValue(ctxMode, m) }

// ReportFrom returns the report the plugin attached to a request, if any.
func ReportFrom(ctx *schemas.BifrostContext) *verify.Report {
	r, _ := ctx.Value(ctxReport).(*verify.Report)
	return r
}

// ErrorFrom returns why verification failed for a request, if it did.
func ErrorFrom(ctx *schemas.BifrostContext) string {
	s, _ := ctx.Value(ctxError).(string)
	return s
}

// Freshness implements schemas.LLMPlugin.
type Freshness struct {
	engine      atomic.Pointer[verify.Engine]
	defaultMode Mode
	timeout     time.Duration
}

var _ schemas.LLMPlugin = (*Freshness)(nil)

func New(defaultMode Mode, timeout time.Duration) *Freshness {
	return &Freshness{defaultMode: defaultMode, timeout: timeout}
}

// SetEngine wires the pipeline in after Bifrost starts (the pipeline itself calls models through Bifrost).
func (p *Freshness) SetEngine(e *verify.Engine) { p.engine.Store(e) }

func (p *Freshness) GetName() string { return "freshgate-freshness" }
func (p *Freshness) Cleanup() error  { return nil }

func (p *Freshness) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

func (p *Freshness) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook runs after the provider answered. It verifies the answer and, depending on
// the mode, annotates or corrects it. Verification failures never break the chat: the
// original answer is returned and the error is recorded on the context.
func (p *Freshness) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, berr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if berr != nil || resp == nil || resp.ChatResponse == nil || llm.IsInternal(ctx) {
		return resp, berr, nil
	}
	mode := p.defaultMode
	if m, ok := ctx.Value(ctxMode).(Mode); ok {
		mode = m
	}
	engine := p.engine.Load()
	if mode == ModeOff || engine == nil {
		return resp, berr, nil
	}
	text := llm.ResponseText(resp.ChatResponse)
	if strings.TrimSpace(text) == "" {
		return resp, berr, nil
	}

	// A fresh context: values from the outer Bifrost request (routing, key selection, retries)
	// must not leak into the pipeline's own Bifrost calls. Cancellation still propagates.
	vctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	report, err := engine.Run(vctx, text, nil)
	if err != nil {
		log.Printf("freshgate: verification skipped: %v", err)
		ctx.SetValue(ctxError, err.Error())
		return resp, berr, nil
	}
	ctx.SetValue(ctxReport, report)

	switch mode {
	case ModeAnnotate:
		llm.SetResponseText(resp.ChatResponse, text+verify.Footnote(report, nil))
	case ModeCorrect:
		fixed, edits := verify.ApplyFixes(report)
		llm.SetResponseText(resp.ChatResponse, fixed+verify.Footnote(report, edits))
	}
	return resp, berr, nil
}
