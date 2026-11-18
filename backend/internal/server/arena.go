package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/llm"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

// Staleness Arena: one question goes to several models through Bifrost, every answer is
// fact-checked with Tavily, and the models are ranked by how current their facts are.

const (
	maxArenaModels    = 6
	arenaParallel     = 3
	arenaAnswerTokens = 900
)

const arenaSystem = `You are a helpful assistant. Answer the user's question directly and concretely. Include the specific current prices, version numbers, names and dates that the question calls for. Keep it under 200 words. State what you believe is true today; do not add disclaimers about your training data.`

type arenaRequest struct {
	Question string   `json:"question"`
	Models   []string `json:"models"`
}

// ArenaEntry is one model's row on the leaderboard.
type ArenaEntry struct {
	Rank         int     `json:"rank"`
	Model        string  `json:"model"`
	Score        int     `json:"score"`
	Label        string  `json:"label"`
	Claims       int     `json:"claims"`
	Stale        int     `json:"stale"`
	Current      int     `json:"current"`
	Unverifiable int     `json:"unverifiable"`
	AnswerMs     int64   `json:"answer_ms"`
	CheckMs      int64   `json:"check_ms"`
	Tokens       int     `json:"tokens"`
	Credits      float64 `json:"credits"`
	Answer       string  `json:"answer,omitempty"`
	Error        string  `json:"error,omitempty"`
}

func (s *Server) arenaModels() []string {
	return s.ArenaModels
}

func (s *Server) handleArena(w http.ResponseWriter, r *http.Request) {
	var req arenaRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	models, err := s.pickArenaModels(req.Models)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeError(w, http.StatusBadRequest, "question is required")
		return
	}
	sse, err := newSSE(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*s.Cfg.VerifyTimeout)
	defer cancel()
	var mu sync.Mutex
	emit := func(event string, data any) {
		mu.Lock()
		defer mu.Unlock()
		sse.send(event, data)
	}
	board := s.RunArena(ctx, req.Question, models, emit)
	emit("arena_done", map[string]any{"leaderboard": board})
}

func (s *Server) pickArenaModels(req []string) ([]string, error) {
	models := req
	if len(models) == 0 {
		models = s.arenaModels()
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		if !strings.Contains(m, "/") {
			return nil, errors.New("models must be provider/model, e.g. groq/llama-3.3-70b-versatile")
		}
		seen[m] = true
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, errors.New("no arena models configured: set FRESHGATE_ARENA_MODELS")
	}
	if len(out) > maxArenaModels {
		out = out[:maxArenaModels]
	}
	return out, nil
}

// RunArena asks every model, checks every answer and returns the ranked leaderboard.
// emit (optional) receives arena_models, arena_answer, arena_event and arena_result events.
func (s *Server) RunArena(ctx context.Context, question string, models []string, emit func(string, any)) []ArenaEntry {
	if emit == nil {
		emit = func(string, any) {}
	}
	emit("arena_models", map[string]any{"models": models, "question": question})
	entries := make([]ArenaEntry, len(models))
	sem := make(chan struct{}, arenaParallel)
	var wg sync.WaitGroup
	for i, m := range models {
		wg.Add(1)
		go func(i int, m string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			entries[i] = s.arenaContestant(ctx, question, m, emit)
		}(i, m)
	}
	wg.Wait()
	return rank(entries)
}

func (s *Server) arenaContestant(ctx context.Context, question, model string, emit func(string, any)) ArenaEntry {
	e := ArenaEntry{Model: model, Label: "error"}
	t0 := time.Now()
	resp, err := s.Engine.LLM.Complete(ctx, llm.Request{
		Purpose:    llm.PurposeAnswer,
		Model:      model,
		NoFallback: true, // the answer must come from this model, or the ranking means nothing
		System:     arenaSystem,
		Messages:   []llm.Message{{Role: "user", Content: question}},
		MaxTokens:  arenaAnswerTokens,
	})
	e.AnswerMs = time.Since(t0).Milliseconds()
	if err != nil {
		e.Error = err.Error()
		emit("arena_answer", map[string]any{"model": model, "error": e.Error, "latency_ms": e.AnswerMs})
		emit("arena_result", e)
		return e
	}
	e.Answer = strings.TrimSpace(resp.Text)
	answerTokens := resp.Meta.PromptTokens + resp.Meta.CompletionTokens
	emit("arena_answer", map[string]any{"model": model, "text": e.Answer, "latency_ms": e.AnswerMs, "tokens": answerTokens,
		"provider": resp.Meta.Provider, "served_by": resp.Meta.Model})

	t1 := time.Now()
	report, err := s.Engine.Run(ctx, e.Answer, func(ev verify.Event) {
		if ev.Type == verify.EvTrace {
			return // the leaderboard needs progress, not the full waterfall
		}
		emit("arena_event", map[string]any{"model": model, "event": ev})
	})
	e.CheckMs = time.Since(t1).Milliseconds()
	if err != nil {
		e.Error = err.Error()
		emit("arena_result", e)
		return e
	}
	sum := report.Summary
	e.Score, e.Label, e.Claims = sum.Score, string(sum.Label), sum.Total
	e.Stale = sum.ByVerdict[verify.VerdictOutdated] + sum.ByVerdict[verify.VerdictIncorrect]
	e.Current = sum.ByVerdict[verify.VerdictCurrent]
	e.Unverifiable = sum.ByVerdict[verify.VerdictUnverifiable]
	e.Tokens = answerTokens + sum.Usage.Tokens
	e.Credits = sum.Usage.TavilyCredits
	emit("arena_result", e)
	return e
}

// rank orders by freshness score, then fewer stale claims, then more checkable claims.
// Failed contestants go last.
func rank(entries []ArenaEntry) []ArenaEntry {
	out := append([]ArenaEntry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Error == "") != (b.Error == "") {
			return a.Error == ""
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Stale != b.Stale {
			return a.Stale < b.Stale
		}
		return a.Claims > b.Claims
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}
