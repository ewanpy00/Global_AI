package config

import "testing"

func clearEnv(t *testing.T) {
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GROQ_API_KEY", "MISTRAL_API_KEY",
		"OPENROUTER_API_KEY", "OLLAMA_BASE_URL", "TAVILY_API_KEY", "FRESHGATE_EXTRACT_MODEL", "FRESHGATE_JUDGE_MODEL",
		"FRESHGATE_CHAT_MODEL", "FRESHGATE_DEMO", "FRESHGATE_MAX_RETRIES", "FRESHGATE_JURY_MODEL", "FRESHGATE_ARENA_MODELS", "FRESHGATE_DISABLE"} {
		t.Setenv(k, "")
	}
}

func TestFreeProviderDefaults(t *testing.T) {
	cases := []struct {
		env            map[string]string
		extract, judge string
	}{
		{map[string]string{"GROQ_API_KEY": "g"}, "groq/llama-3.3-70b-versatile", "groq/openai/gpt-oss-120b"},
		{map[string]string{"GEMINI_API_KEY": "g"}, "gemini/gemini-2.5-flash", "gemini/gemini-2.5-flash"},
		{map[string]string{"OLLAMA_BASE_URL": "http://localhost:11434/"}, "ollama/llama3.1:8b", "ollama/llama3.1:8b"},
		{map[string]string{"GROQ_API_KEY": "g", "ANTHROPIC_API_KEY": "a"}, "anthropic/claude-haiku-5-5", "anthropic/claude-opus-5-5"},
		{map[string]string{"GROQ_API_KEY": "g", "FRESHGATE_JUDGE_MODEL": "gemini/x"}, "groq/llama-3.3-70b-versatile", "gemini/x"},
	}
	for _, c := range cases {
		clearEnv(t)
		t.Setenv("TAVILY_API_KEY", "t")
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Demo {
			t.Errorf("%v: should be live mode", c.env)
		}
		if cfg.ExtractModel.String() != c.extract || cfg.JudgeModel.String() != c.judge {
			t.Errorf("%v: got %s / %s, want %s / %s", c.env, cfg.ExtractModel, cfg.JudgeModel, c.extract, c.judge)
		}
	}
}

func TestOllamaIsKeyless(t *testing.T) {
	clearEnv(t)
	t.Setenv("TAVILY_API_KEY", "t")
	t.Setenv("OLLAMA_BASE_URL", "http://localhost:11434/")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.ProviderKeys["ollama"]; !ok || cfg.ProviderBaseURLs["ollama"] != "http://localhost:11434" {
		t.Fatalf("ollama not enabled: %+v %+v", cfg.ProviderKeys, cfg.ProviderBaseURLs)
	}
}

func TestDemoWithoutTavily(t *testing.T) {
	clearEnv(t)
	t.Setenv("GROQ_API_KEY", "g")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Demo {
		t.Fatal("no Tavily key must mean demo mode")
	}
}

func TestModelRefWithSlashInModel(t *testing.T) {
	r, err := ParseModelRef("groq/openai/gpt-oss-120b")
	if err != nil || r.Provider != "groq" || r.Model != "openai/gpt-oss-120b" {
		t.Fatalf("got %+v, %v", r, err)
	}
	if _, err := ParseModelRef("no-slash"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRetriesCanBeZero(t *testing.T) {
	clearEnv(t)
	t.Setenv("FRESHGATE_MAX_RETRIES", "0")
	cfg, _ := Load()
	if cfg.MaxRetries != 0 {
		t.Fatalf("MaxRetries = %d", cfg.MaxRetries)
	}
}

func TestJuryAndArenaDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("TAVILY_API_KEY", "t")
	t.Setenv("GROQ_API_KEY", "g")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JuryModel.Model != "" {
		t.Fatalf("one provider should not get a jury, got %s", cfg.JuryModel)
	}
	t.Setenv("GEMINI_API_KEY", "x")
	cfg, _ = Load()
	if cfg.JuryModel.String() != "gemini/gemini-2.5-flash" {
		t.Fatalf("jury on the other provider expected, got %s", cfg.JuryModel)
	}
	if len(cfg.ArenaModels) < 2 {
		t.Fatalf("arena defaults: %v", cfg.ArenaModels)
	}
	t.Setenv("FRESHGATE_DISABLE", "jury, followup")
	cfg, _ = Load()
	if cfg.JuryModel.Model != "" || !cfg.Disabled["followup"] {
		t.Fatalf("disable list: jury=%s disabled=%v", cfg.JuryModel, cfg.Disabled)
	}
}
