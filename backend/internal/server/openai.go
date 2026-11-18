package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/ewanpy00/Global_AI/backend/internal/config"
	"github.com/ewanpy00/Global_AI/backend/internal/llm"
	"github.com/ewanpy00/Global_AI/backend/internal/plugin"
)

// handleChatCompletions is the connector: an OpenAI-compatible endpoint. Any app that can talk
// to OpenAI points its base URL here; the request runs through Bifrost, and the freshness plugin
// checks the answer before it is returned.
//
// Model: "provider/model" (e.g. "anthropic/claude-haiku-5-5"), a bare model id for the default
// provider, or "freshgate" for the default chat model. Header X-Freshgate-Mode: off|report|annotate|correct.
//
// stream=true is accepted, but the answer is verified before anything is sent, so the whole
// answer arrives as one chunk followed by the stop chunk.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if s.Gateway == nil {
		writeError(w, http.StatusServiceUnavailable, "the connector needs live mode: set TAVILY_API_KEY and at least one model provider key")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}
	var head struct {
		Model    string                `json:"model"`
		Stream   bool                  `json:"stream"`
		Messages []schemas.ChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(head.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages is required")
		return
	}
	params, err := chatParams(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid parameters: "+err.Error())
		return
	}
	ref, err := s.resolveModel(head.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := plugin.Mode(s.Cfg.ConnectorMode)
	if h := r.Header.Get("X-Freshgate-Mode"); h != "" {
		m, ok := plugin.ParseMode(h)
		if !ok {
			writeError(w, http.StatusBadRequest, "X-Freshgate-Mode must be off, report, annotate or correct")
			return
		}
		mode = m
	}

	bctx := schemas.NewBifrostContext(r.Context(), time.Time{})
	plugin.WithMode(bctx, mode)
	resp, berr := s.Gateway.Chat(bctx, &schemas.BifrostChatRequest{
		Provider: schemas.ModelProvider(ref.Provider),
		Model:    ref.Model,
		Input:    head.Messages,
		Params:   params,
	})
	if berr != nil {
		status := http.StatusBadGateway
		if berr.StatusCode != nil && *berr.StatusCode >= 400 {
			status = *berr.StatusCode
		}
		writeError(w, status, llm.BifrostErr(berr).Error())
		return
	}

	report := plugin.ReportFrom(bctx)
	meta := map[string]any{
		"mode":    mode,
		"report":  report,
		"routing": resp.ExtraFields.RoutingInfo,
	}
	if e := plugin.ErrorFrom(bctx); e != "" {
		meta["error"] = e
	}
	if report != nil {
		w.Header().Set("X-Freshgate-Score", strconv.Itoa(report.Summary.Score))
		w.Header().Set("X-Freshgate-Label", string(report.Summary.Label))
		w.Header().Set("X-Freshgate-Report", report.ID)
	}

	if head.Stream {
		s.writeStream(w, resp, meta)
		return
	}
	out, err := toMap(resp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	delete(out, "extra_fields")
	out["freshgate"] = meta
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) writeStream(w http.ResponseWriter, resp *schemas.BifrostChatResponse, meta map[string]any) {
	sse, err := newSSE(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	base := func(choice map[string]any) map[string]any {
		return map[string]any{
			"id": resp.ID, "object": "chat.completion.chunk", "created": resp.Created, "model": resp.Model,
			"choices": []map[string]any{choice},
		}
	}
	first, _ := json.Marshal(base(map[string]any{
		"index": 0, "delta": map[string]any{"role": "assistant", "content": llm.ResponseText(resp)}, "finish_reason": nil,
	}))
	sse.sendData(string(first))
	last := base(map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"})
	last["freshgate"] = meta
	if resp.Usage != nil {
		last["usage"] = resp.Usage
	}
	lb, _ := json.Marshal(last)
	sse.sendData(string(lb))
	sse.sendData("[DONE]")
}

// resolveModel maps the client's model string to a Bifrost provider/model pair.
func (s *Server) resolveModel(m string) (config.ModelRef, error) {
	m = strings.TrimSpace(m)
	if m == "" || m == "freshgate" {
		if s.Cfg.ChatModel.Model == "" {
			return config.ModelRef{}, fmt.Errorf("no default chat model configured; pass model as provider/model")
		}
		return s.Cfg.ChatModel, nil
	}
	if strings.Contains(m, "/") {
		return config.ParseModelRef(m)
	}
	return config.ModelRef{Provider: s.Cfg.ChatModel.Provider, Model: m}, nil
}

// chatParams decodes the OpenAI sampling/tool parameters, dropping the envelope fields so
// they are not forwarded to the provider as unknown extras.
func chatParams(body []byte) (*schemas.ChatParameters, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	for _, k := range []string{"model", "messages", "stream", "stream_options"} {
		delete(raw, k)
	}
	if len(raw) == 0 {
		return &schemas.ChatParameters{}, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var p schemas.ChatParameters
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}
