package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Budget caps the tokens one verification run may spend. The Ledger plugin enforces it inside
// Bifrost: once the run is over budget, further model calls are short-circuited with an error
// instead of reaching the provider.
type Budget struct {
	Limit  int64 // 0 = unlimited
	used   atomic.Int64
	calls  atomic.Int64
	cached atomic.Int64
}

func (b *Budget) Used() int64        { return b.used.Load() }
func (b *Budget) Calls() int64       { return b.calls.Load() }
func (b *Budget) CachedCalls() int64 { return b.cached.Load() }

type budgetKey struct{}

// WithBudget attaches a token budget to a context; Gateway.Complete hands it to Bifrost.
func WithBudget(ctx context.Context, b *Budget) context.Context {
	return context.WithValue(ctx, budgetKey{}, b)
}

func budgetFrom(ctx interface{ Value(any) any }) *Budget {
	b, _ := ctx.Value(budgetKey{}).(*Budget)
	return b
}

// ctxCacheHit is set by the Ledger when it answered from its cache.
const ctxCacheHit contextKey = "freshgate.cache_hit"

// Ledger is a Bifrost LLMPlugin for the pipeline's own model calls:
//   - PreLLMHook serves repeated identical requests (same model, prompt and evidence) from an
//     in-memory cache, short-circuiting the provider call. Re-checking an answer, re-running the
//     benchmark or checking the same claim from several pages costs nothing.
//   - PreLLMHook rejects calls once the run's token budget is spent.
//   - PostLLMHook stores replies and charges their tokens to the run's budget.
//
// It only acts on internal requests (IsInternal); connector traffic passes through untouched.
type Ledger struct {
	ttl time.Duration
	max int

	mu    sync.Mutex
	cache map[string]ledgerEntry
	hits  atomic.Int64
}

type ledgerEntry struct {
	raw []byte
	at  time.Time
}

var _ schemas.LLMPlugin = (*Ledger)(nil)

func NewLedger(ttl time.Duration, maxEntries int) *Ledger {
	if maxEntries <= 0 {
		maxEntries = 4096
	}
	return &Ledger{ttl: ttl, max: maxEntries, cache: map[string]ledgerEntry{}}
}

func (l *Ledger) GetName() string { return "freshgate-ledger" }
func (l *Ledger) Cleanup() error  { return nil }

// Hits is how many provider calls the cache saved since start.
func (l *Ledger) Hits() int64 { return l.hits.Load() }

func (l *Ledger) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error { return nil }

type ctxKeyLedger string

const ctxLedgerKey ctxKeyLedger = "freshgate.ledger_key"

func (l *Ledger) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if !IsInternal(ctx) || req == nil || req.ChatRequest == nil {
		return req, nil, nil
	}
	if b := budgetFrom(ctx); b != nil && b.Limit > 0 && b.used.Load() >= b.Limit {
		no := false
		status := http.StatusTooManyRequests
		return req, &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
			StatusCode:     &status,
			AllowFallbacks: &no,
			Error:          &schemas.ErrorField{Message: fmt.Sprintf("token budget of %d for this run is spent (Freshgate ledger)", b.Limit)},
		}}, nil
	}
	key := ledgerKey(req.ChatRequest)
	ctx.SetValue(ctxLedgerKey, key)
	if l.ttl <= 0 {
		return req, nil, nil
	}
	l.mu.Lock()
	e, ok := l.cache[key]
	l.mu.Unlock()
	if !ok || time.Since(e.at) > l.ttl {
		return req, nil, nil
	}
	var resp schemas.BifrostChatResponse
	if json.Unmarshal(e.raw, &resp) != nil {
		return req, nil, nil
	}
	l.hits.Add(1)
	ctx.SetValue(ctxCacheHit, true)
	if b := budgetFrom(ctx); b != nil {
		b.cached.Add(1)
	}
	return req, &schemas.LLMPluginShortCircuit{Response: &schemas.BifrostResponse{ChatResponse: &resp}}, nil
}

func (l *Ledger) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, berr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if !IsInternal(ctx) || berr != nil || resp == nil || resp.ChatResponse == nil {
		return resp, berr, nil
	}
	if hit, _ := ctx.Value(ctxCacheHit).(bool); hit {
		return resp, berr, nil
	}
	if b := budgetFrom(ctx); b != nil {
		b.calls.Add(1)
		if u := resp.ChatResponse.Usage; u != nil {
			b.used.Add(int64(u.PromptTokens + u.CompletionTokens))
		}
	}
	key, _ := ctx.Value(ctxLedgerKey).(string)
	if key == "" || l.ttl <= 0 || ResponseText(resp.ChatResponse) == "" {
		return resp, berr, nil
	}
	raw, err := json.Marshal(resp.ChatResponse)
	if err != nil {
		return resp, berr, nil
	}
	l.mu.Lock()
	if len(l.cache) >= l.max {
		l.evictLocked()
	}
	l.cache[key] = ledgerEntry{raw: raw, at: time.Now()}
	l.mu.Unlock()
	return resp, berr, nil
}

// evictLocked drops expired entries, or the oldest half when nothing has expired.
func (l *Ledger) evictLocked() {
	var oldest []time.Time
	for k, e := range l.cache {
		if time.Since(e.at) > l.ttl {
			delete(l.cache, k)
		} else {
			oldest = append(oldest, e.at)
		}
	}
	if len(l.cache) < l.max {
		return
	}
	// median cut-off
	var sum time.Duration
	now := time.Now()
	for _, t := range oldest {
		sum += now.Sub(t)
	}
	avg := sum / time.Duration(len(oldest))
	for k, e := range l.cache {
		if now.Sub(e.at) >= avg {
			delete(l.cache, k)
		}
	}
}

func ledgerKey(r *schemas.BifrostChatRequest) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|", r.Provider, r.Model)
	b, _ := json.Marshal(r.Input)
	h.Write(b)
	if r.Params != nil && r.Params.MaxCompletionTokens != nil {
		fmt.Fprintf(h, "|%d", *r.Params.MaxCompletionTokens)
	}
	return hex.EncodeToString(h.Sum(nil))
}
