// Package llm is the narrow model interface the verification pipeline depends on.
// The production implementation routes every call through Bifrost (bifrost.go);
// tests and demo mode plug in fakes.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Purpose tells the router which model tier a call needs and labels it in traces.
type Purpose string

const (
	PurposeExtract Purpose = "extract" // find time-sensitive claims: cheap, fast model
	PurposeJudge   Purpose = "judge"   // compare a claim with evidence: strong model
	PurposeJury    Purpose = "jury"    // independent second judge on another provider (cross-check)
	PurposeAnswer  Purpose = "answer"  // Staleness Arena: a model answers the user's question itself
)

type Message struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
}

type Request struct {
	Purpose   Purpose
	System    string
	Messages  []Message
	MaxTokens int
	// Model overrides the route for Purpose ("provider/model"). The arena uses it to ask several models.
	Model string
	// NoFallback disables Bifrost fallbacks, so the answer is guaranteed to come from Model.
	NoFallback bool
}

// CallMeta describes how Bifrost served a call. It feeds the pipeline trace in the UI,
// which is how the demo shows what Bifrost contributes.
type CallMeta struct {
	Purpose          Purpose `json:"purpose"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Fallback         bool    `json:"fallback"`
	LatencyMs        int64   `json:"latency_ms"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	// Cached is true when the Bifrost ledger plugin served the reply from its cache (no provider call).
	Cached bool `json:"cached,omitempty"`
}

type Response struct {
	Text string
	Meta CallMeta
}

// Client is anything that can complete a chat request.
type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// ErrNoJSON is returned when a model reply contains no JSON value.
var ErrNoJSON = errors.New("model reply contained no JSON")

// DecodeJSON pulls the first JSON object or array out of a model reply and decodes it.
// Models wrap JSON in prose or ``` fences often enough that strict parsing is not worth it.
func DecodeJSON(text string, v any) error {
	s := strings.TrimSpace(stripThinking(text))
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	if json.Unmarshal([]byte(s), v) == nil {
		return nil
	}
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return ErrNoJSON
	}
	// Walk forward to the matching close bracket, respecting strings.
	open, closeCh := s[start], byte('}')
	if open == '[' {
		closeCh = ']'
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		ch := s[i]
		switch {
		case esc:
			esc = false
		case ch == '\\' && inStr:
			esc = true
		case ch == '"':
			inStr = !inStr
		case inStr:
		case ch == open:
			depth++
		case ch == closeCh:
			depth--
			if depth == 0 {
				return json.Unmarshal([]byte(s[start:i+1]), v)
			}
		}
	}
	return ErrNoJSON
}

// stripThinking removes <think>…</think> blocks that open-weight reasoning models (Qwen, DeepSeek,
// some Ollama models) put before the answer; their prose often contains braces.
func stripThinking(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			return s
		}
		end := strings.Index(s[start:], "</think>")
		if end < 0 {
			return s[:start]
		}
		s = s[:start] + s[start+end+len("</think>"):]
	}
}
