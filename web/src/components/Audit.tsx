import { useEffect, useRef, useState } from "react";
import { LABEL_TEXT, VERDICT_LABEL, streamAudit, type AppConfig, type AuditPage, type AuditResult, type RawEvent, type StreamEvent } from "../api";
import { initialRun, reducer, type RunState } from "../state";
import { ScoreGauge } from "./ScoreGauge";

interface PageState extends AuditPage {
  run: RunState;
  finished: boolean;
}

export function Audit({ config }: { config: AppConfig }) {
  const [url, setUrl] = useState(config.demo ? "https://nimbuscloud.example" : "");
  const [limit, setLimit] = useState(config.audit?.default_pages ?? 6);
  const [stage, setStage] = useState<"idle" | "crawling" | "checking" | "done">("idle");
  const [pages, setPages] = useState<PageState[]>([]);
  const [result, setResult] = useState<AuditResult | null>(null);
  const [crawlInfo, setCrawlInfo] = useState<{ credits: number; ms: number } | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const abort = useRef<AbortController | null>(null);

  useEffect(() => () => abort.current?.abort(), []);

  function onEvent(ev: RawEvent) {
    const d = ev.data;
    switch (ev.type) {
      case "stage":
        setStage(d.stage);
        break;
      case "audit_pages":
        setPages((d.pages as AuditPage[]).map((p) => ({ ...p, run: reducer(initialRun, { type: "start", text: "" }), finished: false })));
        setCrawlInfo({ credits: d.credits, ms: d.crawl_ms });
        break;
      case "page_event":
        setPages((ps) => ps.map((p) => (p.id === d.page_id ? { ...p, run: reducer(p.run, { type: "event", ev: d.event as StreamEvent }) } : p)));
        break;
      case "audit_page":
        setPages((ps) => ps.map((p) => (p.id === d.id ? { ...p, ...(d as AuditPage), finished: true } : p)));
        break;
      case "audit_done":
        setResult(d);
        setStage("done");
        break;
      case "error":
        setError(d.message);
        break;
    }
  }

  async function start() {
    if (!url.trim()) return;
    abort.current?.abort();
    const ctrl = new AbortController();
    abort.current = ctrl;
    setError(null);
    setResult(null);
    setPages([]);
    setOpen(null);
    setStage("crawling");
    try {
      await streamAudit(url.trim(), limit, onEvent, ctrl.signal);
    } catch (e) {
      if (!ctrl.signal.aborted) setError(e instanceof Error ? e.message : String(e));
    }
  }

  const running = stage === "crawling" || stage === "checking";
  // While running keep crawl order; when done, show the stalest pages first (the server's order).
  const shown = result ? result.pages.map((rp) => pages.find((p) => p.id === rp.id) ?? { ...rp, run: initialRun, finished: true }) : pages;

  return (
    <div className="audit">
      <section className="panel audit-hero">
        <div className="audit-intro">
          <h2>Site audit</h2>
          <p className="muted">
            Point it at a docs site, help centre or pricing pages. <b className="t-tavily">Tavily Crawl</b> walks the site, every page goes through
            the freshness pipeline, and you get the pages that tell customers something that is no longer true.
          </p>
        </div>
        <form
          className="audit-form"
          onSubmit={(e) => {
            e.preventDefault();
            void start();
          }}
        >
          <input className="audit-url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://docs.example.com" disabled={running} />
          <select className="select" value={limit} onChange={(e) => setLimit(Number(e.target.value))} disabled={running}>
            {[3, 6, 10, config.audit?.max_pages ?? 15].map((n) => (
              <option key={n} value={n}>
                {n} pages
              </option>
            ))}
          </select>
          <button className="btn btn-primary" disabled={running || !url.trim() || config.audit?.available === false}>
            {running ? (stage === "crawling" ? "Crawling…" : "Checking…") : "Audit site"}
          </button>
        </form>
        {config.audit?.available === false && <div className="banner">Site audit needs TAVILY_API_KEY.</div>}
        {!config.demo && <p className="muted small">Each page costs about as much as one Inspector check (up to 6 claims per page) plus the crawl.</p>}
        {error && <div className="banner banner-err">{error}</div>}
      </section>

      {(stage !== "idle" || result) && (
        <section className="panel audit-summary">
          <ScoreGauge score={result ? result.score : null} label={result?.label} caption={result ? "site" : undefined} size={110} stroke={9} />
          <div>
            {stage === "crawling" && <h3>Crawling {url}…</h3>}
            {stage === "checking" && (
              <h3>
                Checking {pages.length} pages · {pages.filter((p) => p.finished).length} done
              </h3>
            )}
            {result && (
              <>
                <h3 className={`label-${result.label}`}>
                  {result.stale === 0 ? "No stale facts found" : `${result.stale} stale fact${result.stale === 1 ? "" : "s"} on ${result.pages.filter((p) => p.stale > 0).length} page${result.pages.filter((p) => p.stale > 0).length === 1 ? "" : "s"}`}
                </h3>
                <p className="muted small">
                  {result.claims} claims on {result.pages.length} pages · {(result.duration_ms / 1000).toFixed(1)} s
                  {result.credits ? ` · ${result.credits} Tavily credits` : ""}
                </p>
              </>
            )}
            {crawlInfo && !result && <p className="muted small">crawl took {(crawlInfo.ms / 1000).toFixed(1)} s</p>}
          </div>
        </section>
      )}

      {shown.length > 0 && (
        <section className="panel audit-pages">
          {shown.map((p) => {
            const done = p.run.claims.filter((c) => c.status === "done").length;
            const isOpen = open === p.id;
            const stale = (p.items ?? []).filter((c) => c.verdict === "outdated" || c.verdict === "incorrect");
            return (
              <div key={p.id} className={`ap-row ${p.stale > 0 ? "has-stale" : ""}`}>
                <button className="ap-head" onClick={() => setOpen(isOpen ? null : p.id)} disabled={!p.finished || !p.items?.length}>
                  <span className={`ap-score ${p.label ? `label-bg-${p.label}` : ""}`}>{p.finished ? (p.score ?? "–") : <span className="pulse-dot" />}</span>
                  <span className="ap-title">
                    <b>{p.title}</b>
                    <span className="muted small mono">{p.url}</span>
                  </span>
                  <span className="ap-status small">
                    {!p.finished && (p.run.claims.length ? `${done}/${p.run.claims.length} claims` : "reading…")}
                    {p.finished && p.skipped && <span className="muted">skipped: {p.skipped}</span>}
                    {p.finished && p.error && <span className="err">{p.error}</span>}
                    {p.finished && !p.skipped && !p.error && (
                      <>
                        {p.stale > 0 ? <b className="label-stale">{p.stale} stale</b> : <span className="label-fresh">ok</span>} · {p.claims} claims
                      </>
                    )}
                  </span>
                  <span className="ap-caret">{p.items?.length ? (isOpen ? "▾" : "▸") : ""}</span>
                </button>
                {(isOpen || (result && stale.length > 0 && open === null && p === shown[0])) && p.items && (
                  <ul className="ap-items">
                    {p.items.map((c) => (
                      <li key={c.id} className={`ap-item tone-${c.label}`}>
                        <span className={`verdict verdict-${c.verdict}`}>{VERDICT_LABEL[c.verdict]}</span>
                        <span className="ap-claim">“{c.text}”</span>
                        {c.current_value && c.verdict !== "current" && c.verdict !== "unverifiable" && (
                          <span className="now-value">
                            <span className="now-label">Now</span>
                            {c.current_value}
                          </span>
                        )}
                        {c.source && (
                          <a className="small" href={c.source} target="_blank" rel="noreferrer">
                            source
                          </a>
                        )}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            );
          })}
          {result && <p className="muted small pad">Pages are ranked by the number of stale facts. {LABEL_TEXT[result.label]} overall.</p>}
        </section>
      )}
    </div>
  );
}
