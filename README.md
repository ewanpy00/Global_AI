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

Ways to use it:

1. **Inspector** (web UI): paste any AI answer. Claims are highlighted in place (green, amber,
   red) and verified in parallel while you watch. Each card shows the current value, the
   sources and the score breakdown. *Apply fix* rewrites the outdated sentences as a diff.
   A waterfall shows every Bifrost and Tavily call.
2. **Connector** (Bifrost plugin): an OpenAI-compatible endpoint. Point any app or agent at
   `http://localhost:8080/v1` instead of the model provider. Every answer goes through Bifrost,
   and a Bifrost `PostLLMHook` plugin verifies it before it reaches the client. It either
   annotates the answer with a footnote, corrects it in place, or only attaches the report.
3. **Documents** (PDF): upload a report, brief or proposal that an agent generated. The pages
   are shown with every claim highlighted on the page as it is verified. You get the PDF back
   either **annotated** (a highlight and a reviewer note per claim, plus a report page) or
   **corrected** (outdated statements rewritten in place with current facts, each change
   marked and sourced). Agents can do the same in one call: `POST /api/documents/check`.
4. **Staleness Arena**: ask one question, Bifrost sends it to several models (different
   providers, different training cut-offs), every answer is fact-checked live, and the models are
   ranked by how current their facts are. Answers stream in side by side with their claims
   lighting up green and red, then the leaderboard animates in.
5. **Site audit**: give it a docs site, help centre or pricing pages. Tavily Crawl walks the site,
   every page goes through the pipeline, and the pages that tell customers something no longer
   true come first, with the current value and a source for each stale fact.
6. **MCP server**: agents in Claude Code, Claude Desktop, Cursor and others get the tools
   `check_freshness`, `correct_text`, `audit_site` and `compare_models`, so an agent can check its
   own answer before it sends it. HTTP at `/mcp`, or stdio with `freshgate -mcp`.
7. **Quality**: a labelled benchmark (`make eval`) measures accuracy, calibration and cost, and
   compares the plain pipeline with the full one. The Quality tab shows the latest results.

## Accuracy features

The plain pipeline (one search, one judge per claim) is the baseline. Each upgrade below can be
switched off with `FRESHGATE_DISABLE`, and `make eval` measures what it adds:

| Feature | What it does | Why |
|---|---|---|
| Official-source search (`official`) | The extractor names the entity's own domain (`python.org`, `stripe.com`, a regulator). A second, cheap Tavily search restricted to that domain runs in parallel, and its results go first. For leadership, company and statistics claims without a domain it is a news search over the past year. | Aggregators and old blog posts outrank official pages for many queries. |
| Full-page reading (`fetch`) | When the official page's snippet is thin (pricing tables, changelogs), Tavily Extract pulls the page and the judge sees the paragraphs that mention the claim's keywords. | The decisive row of a pricing table is rarely in the snippet. |
| Follow-up search (`followup`) | If the verdict is "unverifiable" or confidence is below 0.6, the judge proposes a better query; one more search runs and the claim is judged again. | Turns "couldn't tell" into an answer where the first search missed. |
| Quote grounding (`grounding`) | Every stance must quote its source verbatim. Quotes not found in the source text are discarded, and a verdict with no verified quote has its confidence halved. | A deterministic guard against the judge inventing evidence. |
| Second judge (`jury`) | An independent judge on another provider (picked automatically when two providers are configured) reads the same evidence in parallel. Agreement averages confidence; if one says "current" and the other "outdated", the claim is marked contested and left for a human. | Different model families have different blind spots and cut-offs. |
| Shared searches (`share`) | Claims about the same entity and attribute share one search per run. | Fewer credits, same evidence. |

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
- **Second judge.** The jury runs on a different provider through the same gateway, so
  cross-checking two model families is one config line.
- **Ledger plugin** (`backend/internal/llm/ledger.go`), a second `LLMPlugin` on the pipeline's own calls:
  its `PreLLMHook` short-circuits identical requests from a cache (re-checking an answer, re-running
  the benchmark or checking the same claim from several pages costs nothing) and rejects calls once
  the run's token budget (`FRESHGATE_TOKEN_BUDGET`) is spent; its `PostLLMHook` stores replies and
  charges their tokens to the run.
- **Arena routing.** The arena asks each contestant with fallbacks disabled, so a ranking can never
  quietly come from a fallback model.
- **Telemetry.** Provider, model, latency, tokens, fallback use and cache hits come from Bifrost's
  response metadata and show up in the UI trace.

**Tavily** handles every fact check:

- **Search**, per claim, with `include_published_date` so recency can be scored, `include_answer`
  as a hint to the judge, `advanced` depth with `chunks_per_source` by default, plus a
  domain-restricted search (`include_domains`) on the official site, or a `topic: news` +
  `time_range: year` search for leadership and company facts. The judge's follow-up query is a
  third search when needed.
- **Extract** reads the full official page when its snippet is too thin to judge.
- **Crawl** walks a website for the site audit.

Every Tavily call shows its credits in the trace and the run summary. Results are cached for an
hour, so repeated demo runs stay fast and don't use extra credits.

## PDF documents

PDF handling runs on [PDFium](https://pdfium.googlesource.com/pdfium/) compiled to
WebAssembly ([go-pdfium](https://github.com/klippa-app/go-pdfium), executed in-process by
wazero), so the Go binary needs no CGO, no system libraries and no external services.

1. **Read.** The text layer is extracted with the page and position of every character, so a
   claim found in the text maps straight back to rectangles on the page.
2. **Check.** The text goes through the same pipeline as a chat answer (extract → Tavily →
   judge → score). The stream adds a `boxes` event right after the claims are found, so the
   viewer draws them on the pages while they are being verified.
3. **Annotated PDF.** The original file plus a standard Highlight annotation on each claim
   (red outdated, yellow unverified, green current) whose note holds the verdict, current
   value, suggested fix and source, and a report page at the end. Opens in any PDF viewer.
4. **Corrected PDF.** Each outdated claim is covered and the corrected wording is typeset on
   the same lines in the claim's size and colour. The rest of the line moves with it, and the
   new text may use the blank space up to the paragraph's right edge. If it still doesn't fit,
   it is condensed by up to 20 %, then shrunk. Every change carries a green highlight whose
   note keeps the original wording and the source.

```bash
# one call from an agent: PDF in, corrected PDF out (or output=annotated | json)
curl -s --data-binary @report.pdf -H 'Content-Type: application/pdf' \
  'http://localhost:8080/api/documents/check?output=corrected' -o report-checked.pdf
```

Limits: PDFs need a text layer (no OCR for scans yet); the text of the first pages up to
60,000 characters is checked; uploads are capped at 25 MB and kept in memory only. The
corrected copy is for reading: the original words remain in the file under the new ones
(a PDF text run cannot be edited reliably in place), so it is not a redaction. The
replacement font is Go Regular, not the document's own font.

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

### Benchmark

```bash
make eval        # live: plain pipeline vs Freshgate on ~48 labelled claims + 5 answers
make eval-demo   # offline, on the demo fixtures (checks the harness only)
```

The dataset (`backend/internal/eval/dataset.json`) has stale claims (old versions, former CEOs,
changed prices, repealed or adopted laws), claims that are still true, and claims about fictional
entities that no source can confirm. Labels are as of November 2026; facts age, so re-check them
before quoting numbers later. Each claim has the acceptable new values, so the benchmark also
checks that a caught stale claim got the right current value. The answers measure extraction
recall and precision, and whether timeless statements ("founded in 2015") are wrongly extracted.

Reported: verdict accuracy, macro-F1, stale precision and recall, coverage (claims with a definite
verdict), current-value accuracy, Brier score and calibration error of the freshness score,
extraction recall/precision, latency p50/p95, model calls, tokens and Tavily credits per claim.
Results go to `backend/eval-results/latest.json` and the Quality tab. A full live run costs about
2-3 Tavily credits per claim per configuration.

### MCP

```bash
claude mcp add --transport http freshgate http://localhost:8080/mcp   # Claude Code, server running
```

Claude Desktop / Cursor (stdio, no server needed):

```json
{"mcpServers": {"freshgate": {"command": "/path/to/Global_AI/backend/bin/freshgate", "args": ["-mcp"],
  "env": {"TAVILY_API_KEY": "…", "GROQ_API_KEY": "…"}}}}
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
| `POST /api/documents` | Upload a PDF (multipart `file` or raw `application/pdf` body); returns pages and text |
| `POST /api/documents/sample` | The first sample answer typeset as a PDF |
| `GET /api/documents/{id}/pages/{n}` | Page `n` rendered as PNG |
| `POST /api/documents/{id}/verify` | Same SSE stream as `/api/verify`, plus `boxes` (claim positions on the pages) |
| `GET /api/documents/{id}/annotated` | Annotated PDF (`?inline=1` to view in the browser) |
| `GET /api/documents/{id}/corrected` | Corrected PDF; `?claims=c1,c3` limits which fixes are applied |
| `POST /api/documents/check` | One call: PDF in, `?output=annotated\|corrected\|json` out; score in `X-Freshgate-Score` |
| `GET /v1/models` | Configured models |
| `POST /api/arena` `{"question", "models"?}` | SSE: `arena_models`, `arena_answer`, `arena_event` (pipeline events per model), `arena_result`, `arena_done` (leaderboard) |
| `POST /api/audit` `{"url", "limit"?}` | SSE: `audit_pages`, `page_event`, `audit_page`, `audit_done` (pages ranked stalest first) |
| `GET /api/eval` | Latest benchmark result |
| `POST /mcp` | MCP (JSON-RPC, Streamable HTTP): `check_freshness`, `correct_text`, `audit_site`, `compare_models` |

## Layout

```
backend/
  cmd/freshgate/        entrypoint: config → Bifrost + plugin → pipeline → HTTP
  internal/llm/         Bifrost gateway (Account, routing, fallbacks, jury), ledger plugin (cache + budget)
  internal/plugin/      Bifrost LLMPlugin: the connector (annotate / correct / report)
  internal/verify/      pipeline, evidence gathering, grounding, jury, prompts, scoring, fixes
  internal/search/      Tavily client (Search, Extract, Crawl) with cache and credit accounting
  internal/eval/        benchmark: labelled datasets, metrics, ablation
  internal/mcp/         Model Context Protocol server (HTTP + stdio)
  internal/document/    PDFium (WebAssembly): text with positions, page images, annotations,
                        in-place corrections, report page
  internal/demo/        offline fixtures for demo mode
  internal/server/      HTTP: SSE, OpenAI-compatible endpoint, arena, site audit, MCP tools, static UI
web/                    React + Vite UI (Inspector, Arena, Documents, Site audit, Connect, Quality)
```

## Roadmap

- Documents: DOCX, OCR for scanned PDFs, and true text replacement (redaction) in PDFs.
- Bifrost semantic cache for repeated claims across users.
- Watch mode: re-check saved answers and documents on a schedule and alert when a fact goes stale.
- Value timeline per claim (how a price or version changed over time, from dated sources).
- Browser extension that overlays highlights on ChatGPT and Claude.
