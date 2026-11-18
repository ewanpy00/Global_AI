# Freshgate: a stale-answer detector for AI apps

LLMs state outdated facts with full confidence: last year's prices, deprecated API versions,
repealed rules, former CEOs. Freshgate is a verification layer that sits between an AI and its
user. It finds the time-sensitive claims in an answer, re-checks each one against live web
sources, and gives every claim a freshness score. It can also correct the answer.

Built for the Geek Room Global AI Hackathon 2026, problem statement 06 (Tavily).

## What it does

| Requirement | How Freshgate does it |
|---|---|
| Claim identification | A fast model (via Bifrost) extracts time-sensitive claims as exact quotes, with category, entity, attribute, claimed value and a search query. |
| Freshness detection | Claims are typed `pricing`, `api_version`, `regulation`, `leadership`, `company`, `statistic` and rated by how fast they go stale. |
| Live verification | Each claim gets a Tavily search with publish dates. A strong model (via Bifrost) compares the claim with the sources and returns `current`, `outdated`, `incorrect` or `unverifiable`, the current value, per-source stance, and a corrected sentence. |
| Freshness scoring | A transparent 0–100 score per claim from verdict, evidence strength, source recency (decayed by a per-category half-life) and source authority, plus a weighted score for the whole answer. |

Two ways to use it:

1. **Inspector** (web UI): paste any AI answer. Claims are highlighted in place (green, amber,
   red) and verified in parallel while you watch. Each card shows the current value, the
   sources and the score breakdown. *Apply fix* rewrites the outdated sentences as a diff.
   A waterfall shows every Bifrost and Tavily call.
2. **Connector** (Bifrost plugin): an OpenAI-compatible endpoint. Point any app or agent at
   `http://localhost:8080/v1` instead of the model provider. Every answer goes through Bifrost,
   and a Bifrost `PostLLMHook` plugin verifies it before it reaches the client. It either
   annotates the answer with a footnote, corrects it in place, or only attaches the report.

## How Tavily and Bifrost are used

```
                 ┌──────────────────────── Freshgate (one Go binary) ───────────────────────┐
 your app ──────►│ /v1/chat/completions ─► Bifrost core ─► provider (Anthropic/OpenAI/...)   │
 (OpenAI SDK)    │                             │                                            │
                 │                     PostLLMHook: freshness plugin                        │
                 │                             ▼                                            │
 Inspector UI ──►│ /api/verify (SSE) ─► pipeline: extract ─► Tavily search ─► judge ─► score │
                 │                     (Bifrost)    (live web)     (Bifrost)                 │
                 └───────────────────────────────────────────────────────────────────────────┘
```

**Bifrost** (`github.com/maximhq/bifrost/core`, embedded as a Go SDK) handles every model call:

- **Per-step routing.** A cheap, fast model extracts claims and a strong model judges them.
  Both are configurable as `provider/model`.
- **Fallbacks.** If the primary provider fails, Bifrost retries on the models listed in
  `FRESHGATE_FALLBACK_MODELS`.
- **Plugin hook.** The connector is a real `schemas.LLMPlugin` (`backend/internal/plugin`).
  Its `PostLLMHook` fact-checks the provider's answer before Bifrost returns it.
- **Telemetry.** Provider, model, latency, tokens and fallback use come from Bifrost's
  response metadata and show up in the UI trace.

**Tavily** handles every fact check: one search per claim, with `include_published_date` so
recency can be scored, `include_answer` as a hint to the judge, and `advanced` depth by
default. Results are cached for an hour, so repeated demo runs stay fast and don't use extra
credits.

## Scoring

```
current       → 50 + 50·(0.5·E + 0.3·R + 0.2·A)      50..100
outdated /
incorrect     → 35·(1 − (0.6·E + 0.2·R + 0.2·A))      0..35
unverifiable  → 60 − 15·volatility(category)         45..60

E  evidence  = judge confidence × min(1, sources that took a side / 2)
R  recency   = 0.5 ^ (age of newest relevant source / category half-life)
               half-life: pricing 90d, API/version 120d, statistic 180d, company 240d, laws/leadership 365d
A  authority = 1 if an official source took a side, 0.6 otherwise
```

Labels: **fresh** ≥ 75, **review** 45–74, **stale** < 45. The answer score is the average of
the claim scores, weighted by time-sensitivity (high = 3, medium = 2, low = 1). One stale
claim keeps the whole answer out of "fresh". The UI shows the breakdown for every claim.

## Run it

Requirements: Go 1.27+, Node 20+.

```bash
cp .env.example .env    # add TAVILY_API_KEY and e.g. ANTHROPIC_API_KEY
make run                # builds the UI, serves everything on http://localhost:8080
```

### Free setup (no paid keys)

The pipeline does not depend on one vendor. Bifrost can route it to any supported provider:

| Option | What to set | Notes |
|---|---|---|
| **Groq** (free tier, no card) | `GROQ_API_KEY` | Fast. Defaults: `groq/llama-3.3-70b-versatile` extracts, `groq/openai/gpt-oss-120b` judges. |
| **Gemini** (Google AI Studio free tier) | `GEMINI_API_KEY` | The free tier covers Flash models. Default: `gemini/gemini-2.5-flash`. Free-tier prompts may be used by Google. |
| **Ollama** (local, offline) | `OLLAMA_BASE_URL=http://localhost:11434` | No key. `ollama pull llama3.1:8b`. Small local models judge less reliably. |
| Mix | e.g. `GROQ_API_KEY` + `GEMINI_API_KEY`, `FRESHGATE_FALLBACK_MODELS=gemini/gemini-2.5-flash` | When Groq hits its rate limit, Bifrost falls back to Gemini. |

Tavily is required either way. Its free plan gives 1,000 credits a month: with
`TAVILY_SEARCH_DEPTH=basic` (1 credit per claim) that is roughly 100 checks of an 8-claim
answer. On free tiers also set `FRESHGATE_CONCURRENCY=2`. Bifrost retries 429s with backoff
(`FRESHGATE_MAX_RETRIES`, default 3).

Free model line-ups change often. On startup Freshgate asks each provider for its live model
list and warns, in the log and in the UI, if a configured model is not offered. `make models`
prints every model id your keys can use. Set any of them with `FRESHGATE_EXTRACT_MODEL` and
`FRESHGATE_JUDGE_MODEL`.

Without keys it starts in **demo mode**: the pipeline runs on bundled fixtures about a
fictional company (all `*.example` domains), so the UI can be built and shown offline. Any
other text gets an honest "unverifiable, demo mode" result.

Development with hot reload:

```bash
make dev-api            # Go API on :8080
make dev-web            # Vite on :5173, proxies /api and /v1 to :8080
make test               # Go tests (incl. an end-to-end Bifrost test) + TS typecheck
make models             # list model ids your provider keys can use
```

### Connector example

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/v1", api_key="unused")
reply = client.chat.completions.create(
    model="freshgate",  # default chat model, or any "provider/model"
    messages=[{"role": "user", "content": "What does GitHub Copilot cost?"}],
    extra_headers={"X-Freshgate-Mode": "correct"},  # off | report | annotate | correct
)
print(reply.choices[0].message.content)  # answer with outdated claims corrected
# reply.model_extra["freshgate"]["report"] holds the full per-claim report
```

The response also carries `X-Freshgate-Score` and `X-Freshgate-Label` headers.
`stream: true` is accepted, but because the answer is checked before it is sent, it arrives
as a single chunk.

## API

| Endpoint | |
|---|---|
| `POST /api/verify` `{"text": "..."}` | Server-Sent Events: `stage`, `claims`, `claim_status`, `trace`, `claim_result`, `summary`, then `report` (full JSON) |
| `GET /api/config` | Models, search settings, demo flag, sample answers |
| `POST /v1/chat/completions` | OpenAI-compatible connector (live mode only) |
| `GET /v1/models` | Configured models |

## Layout

```
backend/
  cmd/freshgate/        entrypoint: config → Bifrost + plugin → pipeline → HTTP
  internal/llm/         Bifrost gateway (Account, routing, fallbacks), model interface
  internal/plugin/      Bifrost LLMPlugin: the connector (annotate / correct / report)
  internal/verify/      pipeline, prompts, span location, scoring, fixes, footnote
  internal/search/      Tavily client with cache
  internal/demo/        offline fixtures for demo mode
  internal/server/      HTTP: SSE, OpenAI-compatible endpoint, static UI, integration tests
web/                    React + Vite UI (Inspector, Connector playground)
```

## Roadmap

- Documents: PDF / DOCX upload. Extract the text, run the same pipeline, return an annotated
  PDF and a corrected copy.
- Bifrost semantic cache for repeated claims across users.
- Domain allow-lists per category (official docs for API claims, regulators for laws).
- Browser extension that overlays highlights on ChatGPT and Claude.
