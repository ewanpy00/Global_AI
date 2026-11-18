// Command freshgate runs the stale-answer detector: the Inspector API, the OpenAI-compatible
// connector, and the web UI, with every model call routed through an embedded Bifrost gateway.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/config"
	"github.com/ewanpy00/Global_AI/backend/internal/demo"
	"github.com/ewanpy00/Global_AI/backend/internal/document"
	"github.com/ewanpy00/Global_AI/backend/internal/llm"
	"github.com/ewanpy00/Global_AI/backend/internal/plugin"
	"github.com/ewanpy00/Global_AI/backend/internal/search"
	"github.com/ewanpy00/Global_AI/backend/internal/server"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

func main() {
	listModels := flag.Bool("list-models", false, "print the models each configured provider offers, then exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *listModels {
		printModels(cfg)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	docs := document.NewEngine()
	docs.Warmup() // compiles PDFium (WebAssembly) in the background
	defer docs.Close()
	srv := &server.Server{Cfg: cfg, Samples: server.DefaultSamples(cfg.Demo), Docs: docs, DocStore: document.NewStore(32)}
	engine := &verify.Engine{MaxClaims: cfg.MaxClaims, Concurrency: cfg.Concurrency}

	if cfg.Demo {
		log.Printf("DEMO MODE: no TAVILY_API_KEY or provider key set; using bundled fixtures (fictional data)")
		engine.LLM = &demo.LLM{Delay: 700 * time.Millisecond}
		engine.Search = &demo.Search{Delay: 500 * time.Millisecond}
	} else {
		if cfg.ExtractModel.Model == "" || cfg.JudgeModel.Model == "" {
			log.Fatalf("set FRESHGATE_EXTRACT_MODEL and FRESHGATE_JUDGE_MODEL (provider/model); run with -list-models to see what your key offers")
		}
		mode, _ := plugin.ParseMode(cfg.ConnectorMode)
		fresh := plugin.New(mode, cfg.VerifyTimeout)
		gw, err := llm.NewGateway(ctx, cfg, fresh)
		if err != nil {
			log.Fatalf("bifrost: %v", err)
		}
		defer gw.Shutdown()
		engine.LLM = gw
		engine.Search = search.NewTavily(cfg.TavilyKey, cfg.TavilySearchDepth, cfg.TavilyMaxResults)
		fresh.SetEngine(engine)
		srv.Gateway = gw
		checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		srv.Warnings = gw.CheckModels(checkCtx)
		cancel()
		for _, w := range srv.Warnings {
			log.Printf("WARNING: %s", w)
		}
		log.Printf("live mode: extract=%s judge=%s chat=%s, search=tavily(%s), connector mode=%s",
			cfg.ExtractModel, cfg.JudgeModel, cfg.ChatModel, cfg.TavilySearchDepth, cfg.ConnectorMode)
	}
	srv.Engine = engine

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	log.Printf("freshgate listening on %s", cfg.Addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func printModels(cfg *config.Config) {
	if len(cfg.ProviderKeys) == 0 {
		log.Fatal("no provider configured: set e.g. GROQ_API_KEY, GEMINI_API_KEY or OLLAMA_BASE_URL")
	}
	gw, err := llm.NewGateway(context.Background(), cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer gw.Shutdown()
	for provider := range cfg.ProviderKeys {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		ids, err := gw.ListModels(ctx, provider)
		cancel()
		if err != nil {
			fmt.Printf("%s: error: %v\n", provider, err)
			continue
		}
		fmt.Printf("%s (%d models):\n", provider, len(ids))
		for _, id := range ids {
			fmt.Printf("  %s/%s\n", provider, id)
		}
	}
}
