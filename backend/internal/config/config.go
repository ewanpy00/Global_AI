// Package config reads Freshgate settings from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ModelRef names a model the way Bifrost routes it: provider + model id.
type ModelRef struct {
	Provider string
	Model    string
}

func (m ModelRef) String() string { return m.Provider + "/" + m.Model }

// ParseModelRef parses "provider/model". The model part may itself contain slashes
// (e.g. "openrouter/meta-llama/llama-3"), so only the first slash splits.
func ParseModelRef(s string) (ModelRef, error) {
	s = strings.TrimSpace(s)
	provider, model, ok := strings.Cut(s, "/")
	if !ok || provider == "" || model == "" {
		return ModelRef{}, fmt.Errorf("model %q must look like provider/model", s)
	}
	return ModelRef{Provider: provider, Model: model}, nil
}

// Config is the full runtime configuration.
type Config struct {
	Addr string

	// Provider API keys handed to Bifrost. Only providers with a key are enabled.
	ProviderKeys map[string]string

	// Optional base URL overrides per provider (used by tests and self-hosted gateways).
	ProviderBaseURLs map[string]string

	// Model routing per pipeline step. Fallbacks are tried by Bifrost in order.
	ExtractModel ModelRef
	JudgeModel   ModelRef
	ChatModel    ModelRef // default model for the connector when a client omits the provider
	Fallbacks    []ModelRef

	TavilyKey         string
	TavilySearchDepth string
	TavilyMaxResults  int

	// MaxClaims caps how many claims one answer can produce (cost guard).
	MaxClaims int
	// Concurrency is how many claims are verified in parallel.
	Concurrency int
	// ConnectorMode is the default behaviour of the Bifrost plugin: off | report | annotate | correct.
	ConnectorMode string
	// VerifyTimeout bounds one full verification run.
	VerifyTimeout time.Duration
	// MaxRetries is how often Bifrost retries a failed provider call (429s on free tiers).
	MaxRetries int

	// Demo is true when no live keys are configured: the pipeline runs on bundled fixtures.
	Demo bool

	WebDir string
}

var providerEnv = map[string]string{
	"anthropic":  "ANTHROPIC_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"gemini":     "GEMINI_API_KEY",
	"groq":       "GROQ_API_KEY",
	"mistral":    "MISTRAL_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	c := &Config{
		Addr:              env("FRESHGATE_ADDR", ":8080"),
		ProviderKeys:      map[string]string{},
		ProviderBaseURLs:  map[string]string{},
		TavilyKey:         os.Getenv("TAVILY_API_KEY"),
		TavilySearchDepth: env("TAVILY_SEARCH_DEPTH", "advanced"),
		TavilyMaxResults:  envInt("TAVILY_MAX_RESULTS", 5),
		MaxClaims:         envInt("FRESHGATE_MAX_CLAIMS", 12),
		Concurrency:       envInt("FRESHGATE_CONCURRENCY", 4),
		ConnectorMode:     env("FRESHGATE_CONNECTOR_MODE", "annotate"),
		VerifyTimeout:     time.Duration(envInt("FRESHGATE_VERIFY_TIMEOUT_SEC", 120)) * time.Second,
		MaxRetries:        envIntAllowZero("FRESHGATE_MAX_RETRIES", 3),
		WebDir:            env("FRESHGATE_WEB_DIR", "../web/dist"),
	}
	for provider, name := range providerEnv {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			c.ProviderKeys[provider] = v
		}
		if v := strings.TrimSpace(os.Getenv(strings.ToUpper(provider) + "_BASE_URL")); v != "" {
			c.ProviderBaseURLs[provider] = v
		}
	}
	// Ollama runs locally and needs no key: setting its URL enables it.
	if v := strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL")); v != "" {
		c.ProviderKeys["ollama"] = ""
		c.ProviderBaseURLs["ollama"] = strings.TrimRight(v, "/")
	}

	defExtract, defJudge := defaultModels(c.ProviderKeys)
	parse := func(key, def string) (ModelRef, error) {
		s := env(key, def)
		if s == "" {
			return ModelRef{}, nil
		}
		return ParseModelRef(s)
	}
	var err error
	if c.ExtractModel, err = parse("FRESHGATE_EXTRACT_MODEL", defExtract); err != nil {
		return nil, err
	}
	if c.JudgeModel, err = parse("FRESHGATE_JUDGE_MODEL", defJudge); err != nil {
		return nil, err
	}
	if c.ChatModel, err = parse("FRESHGATE_CHAT_MODEL", defExtract); err != nil {
		return nil, err
	}
	for _, s := range splitList(os.Getenv("FRESHGATE_FALLBACK_MODELS")) {
		ref, err := ParseModelRef(s)
		if err != nil {
			return nil, err
		}
		c.Fallbacks = append(c.Fallbacks, ref)
	}

	switch c.ConnectorMode {
	case "off", "report", "annotate", "correct":
	default:
		return nil, fmt.Errorf("FRESHGATE_CONNECTOR_MODE must be off|report|annotate|correct, got %q", c.ConnectorMode)
	}

	forceDemo := os.Getenv("FRESHGATE_DEMO") == "1"
	c.Demo = forceDemo || len(c.ProviderKeys) == 0 || c.TavilyKey == ""
	return c, nil
}

// defaultModels picks extractor/judge defaults from whichever providers are configured.
// Extraction is high-volume and simple, so it gets the cheap fast model; the verdict needs judgement.
// Paid providers win over free ones. Free-tier model line-ups change often, so these are only
// starting points: at startup the gateway checks them against the provider's live model list.
func defaultModels(keys map[string]string) (extract, judge string) {
	has := func(p string) bool { _, ok := keys[p]; return ok }
	switch {
	case has("anthropic"):
		return "anthropic/claude-haiku-5-5", "anthropic/claude-opus-5-5"
	case has("openai"):
		return "openai/gpt-5-mini", "openai/gpt-5"
	case has("groq"): // free tier, no card
		return "groq/llama-3.3-70b-versatile", "groq/openai/gpt-oss-120b"
	case has("gemini"): // free tier covers Flash models only
		return "gemini/gemini-2.5-flash", "gemini/gemini-2.5-flash"
	case has("mistral"):
		return "mistral/mistral-small-latest", "mistral/mistral-medium-latest"
	case has("ollama"): // local, free, offline
		return "ollama/llama3.1:8b", "ollama/llama3.1:8b"
	}
	// OpenRouter has no safe default: free models carry a ":free" suffix and change weekly.
	return "", ""
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// envIntAllowZero is envInt for settings where 0 is meaningful (e.g. "no retries").
func envIntAllowZero(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
