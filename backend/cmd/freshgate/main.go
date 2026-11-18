// Command freshgate runs the stale-answer detector: the Inspector API, the OpenAI-compatible
// connector, the Staleness Arena, the site audit, an MCP server and the web UI, with every model
// call routed through an embedded Bifrost gateway and every search through Tavily.
//
//	freshgate                 HTTP server (UI, API, connector, /mcp)
//	freshgate -mcp            MCP server on stdio (Claude Desktop, Cursor, …)
//	freshgate -eval           benchmark on the labelled dataset (see internal/eval)
//	freshgate -list-models    model ids your provider keys can use
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ewanpy00/Global_AI/backend/internal/config"
	"github.com/ewanpy00/Global_AI/backend/internal/demo"
	"github.com/ewanpy00/Global_AI/backend/internal/document"
	"github.com/ewanpy00/Global_AI/backend/internal/eval"
	"github.com/ewanpy00/Global_AI/backend/internal/llm"
	"github.com/ewanpy00/Global_AI/backend/internal/plugin"
	"github.com/ewanpy00/Global_AI/backend/internal/search"
	"github.com/ewanpy00/Global_AI/backend/internal/server"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

func main() {
	listModels := flag.Bool("list-models", false, "print the models each configured provider offers, then exit")
	mcpStdio := flag.Bool("mcp", false, "serve the MCP tools on stdin/stdout instead of HTTP")
	runEval := flag.Bool("eval", false, "run the benchmark on the labelled dataset, then exit")
	evalData := flag.String("eval-data", "", "dataset JSON (default: the bundled live set, or the demo set in demo mode)")
	evalAblation := flag.Bool("eval-ablation", true, "also run the plain pipeline (all accuracy features off) for comparison")
	evalLimit := flag.Int("eval-limit", 0, "only the first N claims (0 = all)")
	evalOut := flag.String("eval-out", "eval-results", "directory for the result JSON")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *listModels {
		printModels(cfg)
		return
	}
	if *mcpStdio {
		log.SetOutput(os.Stderr) // stdout belongs to the protocol
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p, err := buildPipeline(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer p.close()

	if *runEval {
		if err := benchmark(ctx, cfg, p, *evalData, *evalAblation, *evalLimit, *evalOut); err != nil {
			log.Fatal(err)
		}
		return
	}

	srv := &server.Server{
		Cfg:      cfg,
		Engine:   p.engine,
		Gateway:  p.gateway,
		Samples:  server.DefaultSamples(cfg.Demo),
		Warnings: p.warnings,
		Crawler:  p.crawler,
		EvalFile: env("FRESHGATE_EVAL_FILE", filepath.Join(*evalOut, "latest.json")),
	}
	for _, m := range cfg.ArenaModels {
		srv.ArenaModels = append(srv.ArenaModels, m.String())
	}
	if cfg.Demo {
		srv.ArenaModels = demo.ArenaModels
	}

	if *mcpStdio {
		log.Printf("freshgate MCP server on stdio (%s mode)", modeName(cfg))
		if err := srv.MCP().ServeStdio(ctx, os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}

	docs := document.NewEngine()
	docs.Warmup() // compiles PDFium (WebAssembly) in the background
	defer docs.Close()
	srv.Docs, srv.DocStore = docs, document.NewStore(32)

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
	log.Printf("freshgate listening on %s (MCP at /mcp)", cfg.Addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

type pipeline struct {
	engine   *verify.Engine
	gateway  *llm.Gateway
	crawler  search.Crawler
	warnings []string
	close    func()
}

// buildPipeline wires the engine to Bifrost and Tavily (live) or to the bundled fixtures (demo).
func buildPipeline(ctx context.Context, cfg *config.Config) (*pipeline, error) {
	engine := &verify.Engine{MaxClaims: cfg.MaxClaims, Concurrency: cfg.Concurrency, TokenBudget: cfg.TokenBudget}
	f := verify.AllFeatures()
	f.OfficialSearch = !cfg.Disabled["official"]
	f.FetchPages = !cfg.Disabled["fetch"]
	f.FollowUp = !cfg.Disabled["followup"]
	f.Grounding = !cfg.Disabled["grounding"]
	f.ShareSearch = !cfg.Disabled["share"]
	p := &pipeline{engine: engine, close: func() {}}

	if cfg.Demo {
		log.Printf("DEMO MODE: no TAVILY_API_KEY or provider key set; using bundled fixtures (fictional data)")
		engine.LLM = &demo.LLM{Delay: 700 * time.Millisecond}
		ds := &demo.Search{Delay: 500 * time.Millisecond}
		engine.Search, engine.Fetch, p.crawler = ds, ds, ds
		f.Jury = !cfg.Disabled["jury"]
		engine.Features = f
		return p, nil
	}
	if cfg.ExtractModel.Model == "" || cfg.JudgeModel.Model == "" {
		return nil, errors.New("set FRESHGATE_EXTRACT_MODEL and FRESHGATE_JUDGE_MODEL (provider/model); run with -list-models to see what your key offers")
	}
	mode, _ := plugin.ParseMode(cfg.ConnectorMode)
	fresh := plugin.New(mode, cfg.VerifyTimeout)
	gw, err := llm.NewGateway(ctx, cfg, fresh)
	if err != nil {
		return nil, fmt.Errorf("bifrost: %w", err)
	}
	tav := search.NewTavily(cfg.TavilyKey, cfg.TavilySearchDepth, cfg.TavilyMaxResults)
	engine.LLM = gw
	engine.Search, engine.Fetch, p.crawler = tav, tav, tav
	f.Jury = gw.HasRoute(llm.PurposeJury)
	engine.Features = f
	fresh.SetEngine(engine)
	p.gateway = gw
	p.close = gw.Shutdown

	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	p.warnings = gw.CheckModels(checkCtx)
	cancel()
	for _, w := range p.warnings {
		log.Printf("WARNING: %s", w)
	}
	jury := "off"
	if f.Jury {
		jury = cfg.JuryModel.String()
	}
	log.Printf("live mode: extract=%s judge=%s jury=%s chat=%s, search=tavily(%s), connector mode=%s",
		cfg.ExtractModel, cfg.JudgeModel, jury, cfg.ChatModel, cfg.TavilySearchDepth, cfg.ConnectorMode)
	return p, nil
}

// benchmark runs the labelled dataset with the plain pipeline (optional) and the full one,
// prints the comparison and writes eval-results/latest.json for the Quality tab.
func benchmark(ctx context.Context, cfg *config.Config, p *pipeline, data string, ablation bool, limit int, outDir string) error {
	ds, err := eval.Load(data, cfg.Demo)
	if err != nil {
		return err
	}
	models := map[string]string{"extract": cfg.ExtractModel.String(), "judge": cfg.JudgeModel.String()}
	if cfg.Demo {
		models = map[string]string{"extract": "demo/fixture-extract", "judge": "demo/fixture-judge"}
	}
	if p.engine.Features.Jury {
		models["jury"] = cfg.JuryModel.String()
		if cfg.Demo {
			models["jury"] = "demo/fixture-jury"
		}
	}
	full := p.engine.Features
	type cfgRun struct {
		label string
		f     verify.Features
	}
	runs := []cfgRun{}
	if ablation {
		runs = append(runs, cfgRun{"baseline", verify.Features{}})
	}
	runs = append(runs, cfgRun{"freshgate", full})

	n := len(ds.Claims)
	if limit > 0 && limit < n {
		n = limit
	}
	log.Printf("benchmark %s: %d claims, %d answers, %d configuration(s)", ds.Version, n, len(ds.Answers), len(runs))
	var results []eval.Result
	for _, r := range runs {
		p.engine.Features = r.f
		res := eval.Run(ctx, p.engine, ds, eval.Options{
			Label: r.label, Concurrency: cfg.Concurrency, Limit: limit, Extraction: true, Demo: cfg.Demo, Models: models,
			Progress: func(done, total int, id string) {
				fmt.Fprintf(os.Stderr, "\r  %-10s %3d/%d  %-24s", r.label, done, total, id)
			},
		})
		fmt.Fprintln(os.Stderr)
		results = append(results, *res)
	}
	p.engine.Features = full

	fmt.Println()
	fmt.Println(eval.Markdown(results))
	if ds.Note != "" {
		fmt.Println(ds.Note)
	}
	for _, o := range results[len(results)-1].Claims {
		if o.Gold != o.Predicted {
			fmt.Printf("  miss %-22s gold=%-12s got=%-12s %s\n", o.ID, o.Gold, o.Predicted, o.Error)
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(eval.Report{Runs: results}, "", "  ")
	stamp := time.Now().UTC().Format("20060102-150405")
	if cfg.Demo {
		stamp += "-demo"
	}
	for _, name := range []string{"latest.json", "run-" + stamp + ".json"} {
		if err := os.WriteFile(filepath.Join(outDir, name), b, 0o644); err != nil {
			return err
		}
	}
	log.Printf("wrote %s", filepath.Join(outDir, "latest.json"))
	return nil
}

func modeName(cfg *config.Config) string {
	if cfg.Demo {
		return "demo"
	}
	return "live"
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
