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
}

export interface Summary {
  score: number;
  label: Label;
  total: number;
  by_verdict: Partial<Record<Verdict, number>>;
  by_label: Partial<Record<Label, number>>;
  duration_ms: number;
}

export interface TraceEntry {
  kind: "llm" | "search";
  step: "extract" | "judge" | "search";
  claim_id?: string;
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
  note: string;
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
}

export type StreamEvent =
  | { type: "stage"; data: { stage: "extracting" | "verifying" | "done" } }
  | { type: "claims"; data: { claims: Claim[] } }
  | { type: "claim_status"; data: { claim_id: string; status: "searching" | "judging" } }
  | { type: "claim_result"; data: { claim_id: string; result: Verification } }
  | { type: "trace"; data: TraceEntry }
  | { type: "summary"; data: Summary }
  | { type: "report"; data: Report }
  | { type: "error"; data: { message: string } };

export async function fetchConfig(): Promise<AppConfig> {
  const res = await fetch("/api/config");
  if (!res.ok) throw new Error(`config: HTTP ${res.status}`);
  return res.json();
}

/** POSTs the answer and calls onEvent for every Server-Sent Event the backend streams. */
export async function streamVerify(text: string, onEvent: (ev: StreamEvent) => void, signal: AbortSignal): Promise<void> {
  const res = await fetch("/api/verify", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ text }),
    signal,
  });
  if (!res.ok || !res.body) {
    let msg = `HTTP ${res.status}`;
    try {
      const j = await res.json();
      msg = j?.error?.message ?? msg;
    } catch {
      /* keep status text */
    }
    throw new Error(msg);
  }
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
