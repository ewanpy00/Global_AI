package server

import "github.com/ewanpy00/Global_AI/backend/internal/demo"

// DefaultSamples returns the example answers offered in the UI. In live mode they are
// answers written the way a model with an old training cutoff would phrase them.
// Each is an input to check, not a statement of current fact.
func DefaultSamples(demoMode bool) []Sample {
	demoSample := Sample{
		ID:      "nimbus",
		Title:   "Fictional SaaS vendor (offline demo)",
		Heading: "Nimbus Cloud: vendor review",
		Note:    "Bundled fixtures about a made-up company. Works without API keys.",
		Text:    demo.SampleAnswer,
	}
	if demoMode {
		return []Sample{demoSample}
	}
	return []Sample{
		{
			ID:      "llm-pricing",
			Title:   "LLM API pricing (2023-era answer)",
			Heading: "LLM API options: pricing overview",
			Note:    "Typical answer from a model trained in mid-2023.",
			Text: `If you're building on the OpenAI API, GPT-4 is the most capable model available. The 8K-context version costs $0.03 per 1K prompt tokens and $0.06 per 1K completion tokens. For cheaper workloads, gpt-3.5-turbo costs $0.002 per 1K tokens.

On the Anthropic side, the most capable model is Claude 2, which supports a 100K-token context window.`,
		},
		{
			ID:      "dev-versions",
			Title:   "Developer stack versions (2022-era answer)",
			Heading: "Recommended developer stack",
			Note:    "Versions as a model trained in late 2022 would state them.",
			Text:    `For a new project, use the latest stable Python release, Python 3.11. On the frontend, React 18 is the current major version of React, and Node.js 18 is the active LTS release you should deploy on.`,
		},
		{
			ID:      "leadership-regulation",
			Title:   "Leadership and regulation (2022-era answer)",
			Heading: "Tech industry briefing",
			Note:    "Mix of facts that changed and facts that may still hold.",
			Text:    `Twitter is led by CEO Parag Agrawal. In Europe, the EU AI Act is still being negotiated and has not been adopted yet. In the United States, the federal minimum wage is $7.25 per hour.`,
		},
	}
}
