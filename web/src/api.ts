// Types mirror backend/internal/verify/types.go.

export type Category = "pricing" | "api_version" | "regulation" | "leadership" | "company" | "statistic" | "other";
export type Verdict = "current" | "outdated" | "incorrect" | "unverifiable";
export type Label = "fresh" | "review" | "stale";
export type Stance = "supports" | "contradicts" | "neutral";

export interface Claim {
  id: string;
  text: string;
  category: Category;
  entity: string;
  attribute: string;
  claimed_value: string;
  time_sensitivity: "high" | "medium" | "low";
  search_query: string;
  why_time_sensitive: string;
  official_domain?: string;
  start: number;
  end: number;
  located: boolean;
}

export interface Source {
  index: number;
  title: string;
  url: string;
  domain: string;
  snippet: string;
  published_date?: string;
  relevance: number;
  stance: Stance;
  official: boolean;
  quote?: string;
  grounded?: boolean;
  origin?: "search" | "official" | "news" | "followup";
  fetched?: boolean;
}

export interface Grounding {
  cited: number;
  verified: number;
}

export interface JuryVote {
  model: string;
  verdict: Verdict;
  confidence: number;
  current_value?: string;
  explanation?: string;
  agreement: "agree" | "partial" | "contested";
}

export interface Usage {
  llm_calls: number;
  cached_llm_calls: number;
  tokens: number;
  searches: number;
  cached_searches: number;
  shared_searches: number;
  page_fetches: number;
  tavily_credits: number;
  follow_ups: number;
  contested: number;
  quotes_cited: number;
  quotes_verified: number;
}

export interface Features {
  official_search: boolean;
  fetch_pages: boolean;
  follow_up: boolean;
  grounding: boolean;
  share_search: boolean;
  jury: boolean;
}

export interface Breakdown {
  verdict: Verdict;
  confidence: number;
  evidence: number;
  recency: number;
  authority: number;
  half_life_days: number;
  newest_age_days?: number;
  formula: string;
}

export interface Verification {
  verdict: Verdict;
  current_value?: string;
  confidence: number;
  explanation: string;
  corrected_text?: string;
  as_of?: string;
  sources: Source[] | null;
  search_query: string;
  search_summary?: string;
  score: number;
  label: Label;
  breakdown: Breakdown;
  error?: string;
  search_queries?: string[];
  hops?: number;
  follow_up_query?: string;
  grounding?: Grounding;
  jury?: JuryVote;
}

export interface Summary {
  score: number;
  label: Label;
  total: number;
  by_verdict: Partial<Record<Verdict, number>>;
  by_label: Partial<Record<Label, number>>;
  duration_ms: number;
  usage?: Usage;
}

export interface TraceEntry {
  kind: "llm" | "search";
  step: "extract" | "judge" | "jury" | "answer" | "search" | "official" | "news" | "followup" | "fetch" | "crawl";
  claim_id?: string;
  hop?: number;
  shared?: boolean;
  credits?: number;
  at_ms: number;
  provider?: string;
  model?: string;
  fallback?: boolean;
  prompt_tokens?: number;
  completion_tokens?: number;
  query?: string;
  results?: number;
  cached?: boolean;
  latency_ms: number;
  error?: string;
}

export interface Report {
  id: string;
  text: string;
  checked_at: string;
  claims: (Claim & { result: Verification | null })[];
  summary: Summary;
  trace: TraceEntry[];
}

export interface Sample {
  id: string;
  title: string;
  heading: string;
  note: string;
  text: string;
}

/** A highlight rectangle on a PDF page, as fractions of the page size from the top-left corner. */
export interface Box {
  page: number;
  x: number;
  y: number;
  w: number;
  h: number;
}

/** An uploaded PDF (backend/internal/document.Doc). */
export interface DocInfo {
  id: string;
  name: string;
  size: number;
  pages: { index: number; width: number; height: number }[];
  total_pages: number;
  checked_pages: number;
  text: string;
}

export interface AppConfig {
  demo: boolean;
  models: Record<string, string>;
  fallbacks: string[];
  providers: string[];
  search: { provider: string; depth: string; max_results: number; configured: boolean };
  connector: { available: boolean; default_mode: string };
  max_claims: number;
  max_chars: number;
  samples: Sample[];
  warnings: string[] | null;
  documents?: { available: boolean; max_mb: number; max_pages: number };
  features?: Features;
  jury?: string;
  arena?: { models: string[] | null };
  audit?: { available: boolean; default_pages: number; max_pages: number };
  token_budget?: number;
  cache_hits?: number;
}

export type StreamEvent =
  | { type: "stage"; data: { stage: "extracting" | "verifying" | "done" } }
  | { type: "claims"; data: { claims: Claim[] } }
  | { type: "claim_status"; data: { claim_id: string; status: "searching" | "fetching" | "judging" | "following_up" } }
  | { type: "claim_result"; data: { claim_id: string; result: Verification } }
  | { type: "trace"; data: TraceEntry }
  | { type: "summary"; data: Summary }
  | { type: "boxes"; data: { boxes: Record<string, Box[]> } }
  | { type: "report"; data: Report }
  | { type: "error"; data: { message: string } };

export async function fetchConfig(): Promise<AppConfig> {
  const res = await fetch("/api/config");
  if (!res.ok) throw new Error(`config: HTTP ${res.status}`);
  return res.json();
}

/** POSTs the answer and calls onEvent for every Server-Sent Event the backend streams. */
export function streamVerify(text: string, onEvent: (ev: StreamEvent) => void, signal: AbortSignal): Promise<void> {
  return streamSSE(
    "/api/verify",
    { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ text }) },
    onEvent,
    signal,
  );
}

/** Checks an uploaded document; the stream also carries a "boxes" event with claim positions. */
export function streamDocumentVerify(id: string, onEvent: (ev: StreamEvent) => void, signal: AbortSignal): Promise<void> {
  return streamSSE(`/api/documents/${id}/verify`, { method: "POST" }, onEvent, signal);
}

async function errorMessage(res: Response): Promise<string> {
  try {
    const j = await res.json();
    return j?.error?.message ?? `HTTP ${res.status}`;
  } catch {
    return `HTTP ${res.status}`;
  }
}

export async function uploadDocument(file: File): Promise<DocInfo> {
  const form = new FormData();
  form.append("file", file);
  const res = await fetch("/api/documents", { method: "POST", body: form });
  if (!res.ok) throw new Error(await errorMessage(res));
  return res.json();
}

export async function sampleDocument(): Promise<DocInfo> {
  const res = await fetch("/api/documents/sample", { method: "POST" });
  if (!res.ok) throw new Error(await errorMessage(res));
  return res.json();
}

export const documentURL = {
  page: (id: string, n: number) => `/api/documents/${id}/pages/${n}`,
  original: (id: string) => `/api/documents/${id}/file?inline=1`,
  annotated: (id: string, inline = false) => `/api/documents/${id}/annotated${inline ? "?inline=1" : ""}`,
  corrected: (id: string, claims: string[], inline = false) => {
    const q = new URLSearchParams();
    if (claims.length) q.set("claims", claims.join(","));
    if (inline) q.set("inline", "1");
    const qs = q.toString();
    return `/api/documents/${id}/corrected${qs ? `?${qs}` : ""}`;
  },
};

/** A raw SSE event of the arena and audit streams; data is decoded JSON. */
export interface RawEvent {
  type: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  data: any;
}

export function streamArena(question: string, models: string[], onEvent: (ev: RawEvent) => void, signal: AbortSignal): Promise<void> {
  return streamSSE(
    "/api/arena",
    { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ question, models }) },
    onEvent as (ev: StreamEvent) => void,
    signal,
  );
}

export function streamAudit(url: string, limit: number, onEvent: (ev: RawEvent) => void, signal: AbortSignal): Promise<void> {
  return streamSSE(
    "/api/audit",
    { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ url, limit }) },
    onEvent as (ev: StreamEvent) => void,
    signal,
  );
}

export interface ArenaEntry {
  rank: number;
  model: string;
  score: number;
  label: Label | "error";
  claims: number;
  stale: number;
  current: number;
  unverifiable: number;
  answer_ms: number;
  check_ms: number;
  tokens: number;
  credits: number;
  answer?: string;
  error?: string;
}

export interface CompactClaim {
  id: string;
  text: string;
  category: Category;
  verdict: Verdict;
  label: Label;
  score: number;
  current_value?: string;
  corrected_text?: string;
  source?: string;
  explanation?: string;
}

export interface AuditPage {
  id: string;
  url: string;
  title: string;
  chars: number;
  score?: number;
  label?: Label;
  claims: number;
  stale: number;
  skipped?: string;
  error?: string;
  items?: CompactClaim[];
}

export interface AuditResult {
  url: string;
  score: number;
  label: Label;
  pages: AuditPage[];
  claims: number;
  stale: number;
  credits: number;
  duration_ms: number;
}

// ---- benchmark (backend/internal/eval) ----

export interface PRF {
  precision: number;
  recall: number;
  f1: number;
  support: number;
}

export interface EvalMetrics {
  n: number;
  accuracy: number;
  macro_f1: number;
  per_class: Record<string, PRF>;
  confusion: Record<string, Record<string, number>>;
  coverage: number;
  brier: number;
  ece: number;
  value_accuracy?: number;
  latency_p50_ms: number;
  latency_p95_ms: number;
  tokens_per_claim: number;
  credits_per_claim: number;
  llm_calls_per_claim: number;
  follow_up_rate: number;
  errors: number;
}

export interface EvalClaim {
  id: string;
  text: string;
  category: string;
  gold: string;
  predicted: string;
  verdict: string;
  score: number;
  current_value?: string;
  value_ok?: boolean;
  latency_ms: number;
  hops: number;
  contested?: boolean;
  error?: string;
}

export interface EvalRun {
  label: string;
  features: Features;
  demo: boolean;
  models: Record<string, string>;
  dataset: string;
  run_at: string;
  duration_ms: number;
  metrics: EvalMetrics;
  extraction?: { answers: number; gold: number; found: number; extracted: number; recall: number; precision: number; skip_violations: number; skip_total: number };
  claims: EvalClaim[];
}

export async function fetchEval(): Promise<{ runs: EvalRun[] } | null> {
  const res = await fetch("/api/eval");
  if (res.status === 404) return null;
  if (!res.ok) throw new Error(await errorMessage(res));
  return res.json();
}

async function streamSSE(url: string, init: RequestInit, onEvent: (ev: StreamEvent) => void, signal: AbortSignal): Promise<void> {
  const res = await fetch(url, { ...init, signal });
  if (!res.ok || !res.body) throw new Error(await errorMessage(res));
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += value;
    let sep: number;
    while ((sep = buf.indexOf("\n\n")) >= 0) {
      const block = buf.slice(0, sep);
      buf = buf.slice(sep + 2);
      let type = "message";
      const data: string[] = [];
      for (const line of block.split("\n")) {
        if (line.startsWith("event: ")) type = line.slice(7);
        else if (line.startsWith("data: ")) data.push(line.slice(6));
      }
      if (data.length) onEvent({ type, data: JSON.parse(data.join("\n")) } as StreamEvent);
    }
  }
}

export interface ChatTurn {
  role: "user" | "assistant";
  content: string;
}

export interface ChatResult {
  content: string;
  report: Report | null;
  error?: string;
  model: string;
  routing?: { provider?: string; model?: string; is_fallback?: boolean };
}

/** Calls the OpenAI-compatible connector, i.e. the same endpoint any external app would use. */
export async function connectorChat(messages: ChatTurn[], mode: string, model: string): Promise<ChatResult> {
  const res = await fetch("/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Freshgate-Mode": mode },
    body: JSON.stringify({ model, messages }),
  });
  const j = await res.json();
  if (!res.ok) throw new Error(j?.error?.message ?? `HTTP ${res.status}`);
  return {
    content: j.choices?.[0]?.message?.content ?? "",
    report: j.freshgate?.report ?? null,
    error: j.freshgate?.error,
    model: j.model,
    routing: j.freshgate?.routing,
  };
}

export const CATEGORY_LABEL: Record<Category, string> = {
  pricing: "Pricing",
  api_version: "API / version",
  regulation: "Regulation",
  leadership: "Leadership",
  company: "Company",
  statistic: "Statistic",
  other: "Other",
};

export const VERDICT_LABEL: Record<Verdict, string> = {
  current: "Current",
  outdated: "Outdated",
  incorrect: "Incorrect",
  unverifiable: "Unverified",
};

export const LABEL_TEXT: Record<Label, string> = {
  fresh: "Fresh",
  review: "Needs review",
  stale: "Stale",
};
