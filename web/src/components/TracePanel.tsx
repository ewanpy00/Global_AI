import type { TraceEntry } from "../api";

interface Props {
  trace: TraceEntry[];
  running: boolean;
  demo?: boolean;
}

/**
 * Waterfall of every external call in the run. This is where the demo shows what each
 * sponsor technology does: Bifrost serves every model call (which provider/model, latency,
 * tokens, fallbacks); Tavily serves every live search.
 */
export function TracePanel({ trace, running, demo }: Props) {
  const llm = trace.filter((t) => t.kind === "llm");
  const search = trace.filter((t) => t.kind === "search");
  const span = Math.max(1, ...trace.map((t) => t.at_ms));
  const tokens = llm.reduce((n, t) => n + (t.prompt_tokens ?? 0) + (t.completion_tokens ?? 0), 0);
  const models = new Set(llm.map((t) => `${t.provider}/${t.model}`));
  const fallbacks = llm.filter((t) => t.fallback).length;
  const sources = search.reduce((n, t) => n + (t.results ?? 0), 0);
  const cached = search.filter((t) => t.cached).length;
  const credits = search.reduce((n, t) => n + (t.credits ?? 0), 0);
  const llmCached = llm.filter((t) => t.cached).length;
  const fetches = search.filter((t) => t.step === "fetch").length;
  const rows = [...trace].sort((a, b) => a.at_ms - a.latency_ms - (b.at_ms - b.latency_ms));

  return (
    <section className="trace panel">
      <div className="trace-head">
        <h3>Pipeline trace</h3>
        <div className="trace-stats">
          <span className="lane-pill lane-bifrost">
            <b>Bifrost</b> {llm.length} model calls · {models.size} model{models.size === 1 ? "" : "s"} · {fallbacks} fallback{fallbacks === 1 ? "" : "s"} · {tokens.toLocaleString()} tokens
            {llmCached ? ` · ${llmCached} from ledger cache` : ""}
          </span>
          <span className="lane-pill lane-tavily">
            <b>Tavily</b> {search.length - fetches} searches{fetches ? ` · ${fetches} page fetch${fetches === 1 ? "" : "es"}` : ""} · {sources} sources
            {cached ? ` · ${cached} cached` : ""}
            {credits ? ` · ${credits} credits` : ""}
          </span>
        </div>
        {demo && <span className="muted small">demo mode: fixture calls, nothing leaves this machine</span>}
      </div>
      {trace.length === 0 ? (
        <p className="muted small">{running ? "Waiting for the first call…" : "Run a check to see every Bifrost and Tavily call here."}</p>
      ) : (
        <div className="waterfall">
          {rows.map((t, i) => {
            const start = Math.max(0, t.at_ms - t.latency_ms);
            const left = (start / span) * 100;
            const width = Math.max(0.8, (Math.min(t.latency_ms, t.at_ms) / span) * 100);
            const label = stepLabel(t);
            const detail =
              t.kind === "search"
                ? t.step === "fetch"
                  ? `${t.query} → full page${t.cached ? " (cached)" : ""}`
                  : `“${t.query}” → ${t.results ?? 0} results${t.shared ? " (shared with another claim)" : t.cached ? " (cached)" : ""}`
                : `${t.provider}/${t.model}${t.fallback ? " (fallback)" : ""}${t.cached ? " · ledger cache" : ` · ${(t.prompt_tokens ?? 0) + (t.completion_tokens ?? 0)} tok`}`;
            return (
              <div className={`wf-row ${t.error ? "wf-err" : ""}`} key={i} title={t.error ?? detail}>
                <span className={`wf-lane lane-${t.kind === "llm" ? "bifrost" : "tavily"}`}>{t.kind === "llm" ? "Bifrost" : "Tavily"}</span>
                <span className="wf-label">{label}</span>
                <span className="wf-detail">{t.error ? `error: ${t.error}` : detail}</span>
                <span className="wf-track">
                  <span className={`wf-bar bar-${t.kind}`} style={{ left: `${left}%`, width: `${width}%` }} />
                </span>
                <span className="wf-ms mono">{t.latency_ms} ms</span>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

const STEP: Record<string, string> = {
  search: "search",
  official: "official site",
  news: "news search",
  followup: "follow-up",
  fetch: "fetch page",
  judge: "judge",
  jury: "2nd judge",
  answer: "answer",
  crawl: "crawl",
};

function stepLabel(t: TraceEntry): string {
  if (t.step === "extract") return "extract claims";
  const base = `${STEP[t.step] ?? t.step} ${t.claim_id ?? ""}`;
  return t.hop && t.hop > 1 ? `${base} · hop ${t.hop}` : base;
}
