import type { ReactNode } from "react";
import { LABEL_TEXT, VERDICT_LABEL, type AppConfig, type Usage, type Verdict } from "../api";
import type { RunState } from "../state";
import { ScoreGauge } from "./ScoreGauge";

const VERDICT_ORDER: Verdict[] = ["outdated", "incorrect", "unverifiable", "current"];

interface Props {
  run: RunState;
  config: AppConfig;
  /** Shown before the first run. */
  idle: ReactNode;
}

/** Score gauge plus the run's progress or outcome. */
export function SummaryPanel({ run, config, idle }: Props) {
  const done = run.claims.filter((c) => c.status === "done").length;
  return (
    <section className="panel summary">
      <ScoreGauge score={run.summary ? run.summary.score : null} label={run.summary?.label} caption={run.summary ? "freshness" : undefined} />
      <div className="summary-body">
        {run.phase === "idle" && idle}
        {run.phase === "extracting" && (
          <>
            <h3>Finding time-sensitive claims…</h3>
            <p className="muted small mono">{config.models.extract} via Bifrost</p>
          </>
        )}
        {run.phase === "verifying" && (
          <>
            <h3>
              Verifying {run.claims.length} claims · {done} done
            </h3>
            <div className="progress">
              <div style={{ width: `${run.claims.length ? (done / run.claims.length) * 100 : 0}%` }} />
            </div>
          </>
        )}
        {run.summary && (
          <>
            <h3 className={`label-${run.summary.label}`}>
              {run.summary.total === 0 ? "No time-sensitive claims" : LABEL_TEXT[run.summary.label]}
            </h3>
            <div className="verdict-chips">
              {VERDICT_ORDER.filter((v) => run.summary!.by_verdict[v]).map((v) => (
                <span key={v} className={`vchip vchip-${v}`}>
                  {run.summary!.by_verdict[v]} {VERDICT_LABEL[v].toLowerCase()}
                </span>
              ))}
            </div>
            <p className="muted small">
              {run.summary.total} claims checked in {(run.summary.duration_ms / 1000).toFixed(1)} s
            </p>
            {run.summary.usage && run.summary.total > 0 && <UsageLine u={run.summary.usage} />}
          </>
        )}
        {run.phase === "error" && run.claims.length === 0 && <h3 className="label-stale">Check failed</h3>}
      </div>
    </section>
  );
}

function UsageLine({ u }: { u: Usage }) {
  const parts: string[] = [];
  if (u.quotes_cited) parts.push(`${u.quotes_verified}/${u.quotes_cited} quotes verified`);
  if (u.follow_ups) parts.push(`${u.follow_ups} follow-up search${u.follow_ups === 1 ? "" : "es"}`);
  if (u.contested) parts.push(`${u.contested} contested`);
  if (u.page_fetches) parts.push(`${u.page_fetches} official page${u.page_fetches === 1 ? "" : "s"} read`);
  if (u.cached_llm_calls) parts.push(`${u.cached_llm_calls} cached model calls`);
  if (u.tavily_credits) parts.push(`${u.tavily_credits} Tavily credits`);
  if (parts.length === 0) return null;
  return <p className="muted small usage-line">{parts.join(" · ")}</p>;
}
