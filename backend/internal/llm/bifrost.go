package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/ewanpy00/Global_AI/backend/internal/config"
)

type contextKey string

// ctxInternal marks requests that Freshgate itself sends (claim extraction, judging).
// The freshness plugin skips them, otherwise it would try to verify its own JSON.
const ctxInternal contextKey = "freshgate.internal"

// IsInternal reports whether a Bifrost request was issued by the verification pipeline.
func IsInternal(ctx *schemas.BifrostContext) bool {
	v, _ := ctx.Value(ctxInternal).(bool)
	return v
}

// Gateway owns the embedded Bifrost instance. Every model call in Freshgate goes through it:
// the pipeline's own calls (Complete) and the connector's pass-through chat calls (Chat).
type Gateway struct {
	bf        *bifrost.Bifrost
	routes    map[Purpose]config.ModelRef
	fallbacks []config.ModelRef
	chat      config.ModelRef
}

// NewGateway starts Bifrost with the configured providers and the given plugins.
func NewGateway(ctx context.Context, cfg *config.Config, plugins ...schemas.LLMPlugin) (*Gateway, error) {
	if len(cfg.ProviderKeys) == 0 {
		return nil, errors.New("no provider API keys configured")
	}
	acc := &account{keys: map[schemas.ModelProvider]string{}, baseURLs: map[schemas.ModelProvider]string{}, maxRetries: cfg.MaxRetries}
	for p, k := range cfg.ProviderKeys {
		acc.keys[schemas.ModelProvider(p)] = k
	}
	for p, u := range cfg.ProviderBaseURLs {
		acc.baseURLs[schemas.ModelProvider(p)] = u
	}
	bf, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:    acc,
		LLMPlugins: plugins,
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelWarn),
	})
	if err != nil {
		return nil, fmt.Errorf("init bifrost: %w", err)
	}
	return &Gateway{
		bf: bf,
		routes: map[Purpose]config.ModelRef{
			PurposeExtract: cfg.ExtractModel,
			PurposeJudge:   cfg.JudgeModel,
		},
		fallbacks: cfg.Fallbacks,
		chat:      cfg.ChatModel,
	}, nil
}

func (g *Gateway) Shutdown() { g.bf.Shutdown() }

// Routes returns the model chosen for each pipeline step (shown in the UI).
func (g *Gateway) Routes() map[Purpose]config.ModelRef { return g.routes }

// Complete implements Client for the verification pipeline.
func (g *Gateway) Complete(ctx context.Context, req Request) (Response, error) {
	route, ok := g.routes[req.Purpose]
	if !ok || route.Model == "" {
		return Response{}, fmt.Errorf("no model routed for purpose %q", req.Purpose)
	}
	bctx := schemas.NewBifrostContext(ctx, time.Time{})
	bctx.SetValue(ctxInternal, true)

	msgs := make([]schemas.ChatMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, textMessage(schemas.ChatMessageRoleSystem, req.System))
	}
	for _, m := range req.Messages {
		msgs = append(msgs, textMessage(schemas.ChatMessageRole(m.Role), m.Content))
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}
	breq := &schemas.BifrostChatRequest{
		Provider:  schemas.ModelProvider(route.Provider),
		Model:     route.Model,
		Input:     msgs,
		Params:    &schemas.ChatParameters{MaxCompletionTokens: &maxTokens},
		Fallbacks: g.fallbackList(route),
	}
	resp, berr := g.bf.ChatCompletionRequest(bctx, breq)
	if berr != nil {
		return Response{}, BifrostErr(berr)
	}
	text := ResponseText(resp)
	if text == "" {
		return Response{}, errors.New("model returned an empty reply")
	}
	meta := CallMeta{
		Purpose:   req.Purpose,
		Provider:  string(resp.ExtraFields.RoutingInfo.Provider),
		Model:     resp.ExtraFields.RoutingInfo.Model,
		Fallback:  resp.ExtraFields.RoutingInfo.IsFallback,
		LatencyMs: resp.ExtraFields.Latency,
	}
	if meta.Provider == "" {
		meta.Provider, meta.Model = route.Provider, route.Model
	}
	if resp.Usage != nil {
		meta.PromptTokens = resp.Usage.PromptTokens
		meta.CompletionTokens = resp.Usage.CompletionTokens
	}
	return Response{Text: text, Meta: meta}, nil
}

// Chat forwards a client's chat request through Bifrost. Plugins (the freshness check) run on it.
func (g *Gateway) Chat(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if req.Provider == "" {
		req.Provider = schemas.ModelProvider(g.chat.Provider)
		if req.Model == "" {
			req.Model = g.chat.Model
		}
	}
	if len(req.Fallbacks) == 0 {
		req.Fallbacks = g.fallbackList(config.ModelRef{Provider: string(req.Provider), Model: req.Model})
	}
	return g.bf.ChatCompletionRequest(ctx, req)
}

func (g *Gateway) fallbackList(primary config.ModelRef) []schemas.Fallback {
	var out []schemas.Fallback
	for _, f := range g.fallbacks {
		if f == primary {
			continue
		}
		out = append(out, schemas.Fallback{Provider: schemas.ModelProvider(f.Provider), Model: f.Model})
	}
	return out
}

func textMessage(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: &text}}
}

// ResponseText returns the assistant text of the first choice.
func ResponseText(resp *schemas.BifrostChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	choice := resp.Choices[0]
	if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil {
		return ""
	}
	return MessageText(choice.ChatNonStreamResponseChoice.Message)
}

// MessageText flattens a chat message's content to plain text.
func MessageText(m *schemas.ChatMessage) string {
	if m == nil || m.Content == nil {
		return ""
	}
	if m.Content.ContentStr != nil {
		return *m.Content.ContentStr
	}
	var b strings.Builder
	for _, block := range m.Content.ContentBlocks {
		if block.Text != nil {
			b.WriteString(*block.Text)
		}
	}
	return b.String()
}

// SetResponseText replaces the assistant text of the first choice.
func SetResponseText(resp *schemas.BifrostChatResponse, text string) {
	if resp == nil || len(resp.Choices) == 0 {
		return
	}
	choice := resp.Choices[0]
	if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil {
		return
	}
	msg := choice.ChatNonStreamResponseChoice.Message
	if msg.Content == nil {
		msg.Content = &schemas.ChatMessageContent{}
	}
	msg.Content.ContentStr = &text
	msg.Content.ContentBlocks = nil
}

// BifrostErr converts a Bifrost error into a Go error with a readable message.
func BifrostErr(e *schemas.BifrostError) error {
	if e == nil {
		return nil
	}
	msg := "bifrost request failed"
	if e.Error != nil {
		if e.Error.Message != "" {
			msg = e.Error.Message
		} else if e.Error.Error != nil {
			msg = e.Error.Error.Error()
		}
	}
	if e.StatusCode != nil {
		return fmt.Errorf("%s (status %d)", msg, *e.StatusCode)
	}
	return errors.New(msg)
}

// account is the Bifrost Account backed by environment keys.
type account struct {
	keys       map[schemas.ModelProvider]string
	baseURLs   map[schemas.ModelProvider]string
	maxRetries int
}

func (a *account) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	out := make([]schemas.ModelProvider, 0, len(a.keys))
	for p := range a.keys {
		out = append(out, p)
	}
	return out, nil
}

func (a *account) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	k, ok := a.keys[p]
	if !ok {
		return nil, fmt.Errorf("provider %s is not configured", p)
	}
	key := schemas.Key{
		ID:     string(p) + "-env",
		Name:   string(p) + " (env)",
		Value:  *schemas.NewSecretVar(k),
		Models: schemas.WhiteList{"*"},
		Weight: 1,
	}
	if p == schemas.Ollama {
		// Ollama is keyless; Bifrost reads the server URL from the key config.
		key.OllamaKeyConfig = &schemas.OllamaKeyConfig{URL: *schemas.NewSecretVar(a.baseURLs[p])}
	}
	return []schemas.Key{key}, nil
}

func (a *account) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if _, ok := a.keys[p]; !ok {
		return nil, fmt.Errorf("provider %s is not configured", p)
	}
	nc := schemas.DefaultNetworkConfig
	nc.BaseURL = a.baseURLs[p]
	nc.DefaultRequestTimeoutInSeconds = 90
	// Free tiers answer bursts with 429; Bifrost retries with exponential backoff and jitter.
	nc.MaxRetries = a.maxRetries
	nc.RetryBackoffInitial = time.Second
	nc.RetryBackoffMax = 10 * time.Second
	return &schemas.ProviderConfig{
		NetworkConfig:            nc,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}
