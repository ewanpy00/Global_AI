// Package server exposes Freshgate over HTTP:
//
//	POST /api/verify             stream a verification run as Server-Sent Events (Inspector UI)
//	GET  /api/config             what is configured (models, search, demo mode, samples)
//	POST /v1/chat/completions    OpenAI-compatible connector: chat through Bifrost + freshness plugin
//	GET  /v1/models              model list for OpenAI clients
//	/                            the built web UI
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/config"
	"github.com/ewanpy00/Global_AI/backend/internal/llm"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

// Server wires HTTP handlers to the pipeline and (in live mode) the Bifrost gateway.
type Server struct {
	Cfg     *config.Config
	Engine  *verify.Engine
	Gateway *llm.Gateway // nil in demo mode
	Samples []Sample
	// Warnings are configuration problems found at startup (e.g. a model the provider does not offer).
	Warnings []string
}

type Sample struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Note  string `json:"note"`
	Text  string `json:"text"`
}

const maxBody = 1 << 20

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("POST /api/verify", s.handleVerify)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.Handle("/", s.staticHandler())
	return withCORS(withLogging(mux))
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	models := map[string]string{}
	if !s.Cfg.Demo {
		models["extract"] = s.Cfg.ExtractModel.String()
		models["judge"] = s.Cfg.JudgeModel.String()
		models["chat"] = s.Cfg.ChatModel.String()
	} else {
		models["extract"], models["judge"] = "demo/fixture-extract", "demo/fixture-judge"
	}
	fallbacks := make([]string, 0, len(s.Cfg.Fallbacks))
	for _, f := range s.Cfg.Fallbacks {
		fallbacks = append(fallbacks, f.String())
	}
	providers := make([]string, 0, len(s.Cfg.ProviderKeys))
	for p := range s.Cfg.ProviderKeys {
		providers = append(providers, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"demo":           s.Cfg.Demo,
		"models":         models,
		"fallbacks":      fallbacks,
		"providers":      providers,
		"search":         map[string]any{"provider": "tavily", "depth": s.Cfg.TavilySearchDepth, "max_results": s.Cfg.TavilyMaxResults, "configured": s.Cfg.TavilyKey != ""},
		"connector":      map[string]any{"available": s.Gateway != nil, "default_mode": s.Cfg.ConnectorMode},
		"max_claims":     s.Cfg.MaxClaims,
		"max_chars":      verify.MaxAnswerChars,
		"samples":        s.Samples,
		"freshness_band": map[string]int{"fresh": 75, "stale": 45},
		"warnings":       s.Warnings,
	})
}

type verifyRequest struct {
	Text string `json:"text"`
}

// handleVerify streams the run as SSE. Each pipeline event becomes one SSE message;
// the final "report" message carries the whole report.
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	sse, err := newSSE(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.Cfg.VerifyTimeout)
	defer cancel()

	report, err := s.Engine.Run(ctx, req.Text, func(ev verify.Event) { sse.send(ev.Type, ev.Data) })
	if err != nil {
		sse.send(verify.EvError, map[string]string{"message": err.Error()})
		return
	}
	sse.send(verify.EvReport, report)
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	var data []map[string]any
	seen := map[string]bool{}
	for _, m := range []config.ModelRef{s.Cfg.ChatModel, s.Cfg.ExtractModel, s.Cfg.JudgeModel} {
		if m.Model == "" || seen[m.String()] {
			continue
		}
		seen[m.String()] = true
		data = append(data, map[string]any{"id": m.String(), "object": "model", "owned_by": m.Provider})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) staticHandler() http.Handler {
	dir := s.Cfg.WebDir
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "Freshgate API is running. Build the UI with `npm run build` in web/, or run `npm run dev` there.\n")
		})
	}
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Single-page app: unknown paths fall back to index.html.
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError uses the OpenAI error shape so connector clients display it properly.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "freshgate_error"}})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Freshgate-Mode")
		w.Header().Set("Access-Control-Expose-Headers", "X-Freshgate-Score, X-Freshgate-Label, X-Freshgate-Report")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }

// Flush keeps SSE streaming working through the logging wrapper.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
		}
	})
}
