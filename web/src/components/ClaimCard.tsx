import { useState } from "react";
import { CATEGORY_LABEL, VERDICT_LABEL, type Source } from "../api";
import { isFixable, type ClaimState } from "../state";
import { ScoreGauge } from "./ScoreGauge";
import { tone } from "./AnnotatedText";

interface Props {
  claim: ClaimState;
  active: boolean;
  applied: boolean;
  judgeModel?: string;
  onSelect: () => void;
  onHover: (on: boolean) => void;
  onToggleFix: () => void;
}

const STATUS_TEXT: Record<string, string> = {
  queued: "Queued",
  searching: "Searching the web with Tavily…",
  judging: "Comparing with sources via Bifrost…",
};

export function ClaimCard({ claim: c, active, applied, judgeModel, onSelect, onHover, onToggleFix }: Props) {
  const [open, setOpen] = useState(false);
  const r = c.result;
  const t = tone(c);
  return (
    <article
      id={`card-${c.id}`}
      className={`card card-${t} ${active ? "is-active" : ""}`}
      onClick={onSelect}
      onMouseEnter={() => onHover(true)}
      onMouseLeave={() => onHover(false)}
    >
      <header className="card-head">
        <span className="card-num">{c.id.slice(1)}</span>
        {r ? <span className={`verdict verdict-${r.verdict}`}>{VERDICT_LABEL[r.verdict]}</span> : <span className="verdict verdict-pending">Checking</span>}
        <span className="chip">{CATEGORY_LABEL[c.category]}</span>
        {c.time_sensitivity === "high" && <span className="chip chip-hot">volatile</span>}
        <span className="spacer" />
        {r ? <ScoreGauge score={r.score} label={r.label} size={44} stroke={5} /> : <ScoreGauge score={null} size={44} stroke={5} />}
      </header>

      <blockquote className="card-quote">“{c.text}”</blockquote>
      {!c.located && <p className="muted small">Not found verbatim in the answer, so it is not highlighted.</p>}

      {!r && (
        <div className="card-working">
          <span className="pulse-dot" /> {c.status === "judging" && judgeModel ? `Judging with ${judgeModel}…` : STATUS_TEXT[c.status]}
          <div className="muted small mono">query: {c.search_query}</div>
        </div>
      )}

      {r && (
        <>
          {r.current_value && r.verdict !== "current" && r.verdict !== "unverifiable" && (
            <div className="now-value">
              <span className="now-label">Now</span>
              <span>{r.current_value}</span>
            </div>
          )}
          <p className="card-explain">{r.explanation}</p>

          {r.sources && r.sources.some((s) => s.stance !== "neutral") && (
            <ul className="sources">
              {r.sources.filter((s) => s.stance !== "neutral").map((s) => (
                <SourceRow key={s.index} s={s} />
              ))}
            </ul>
          )}

          <div className="card-actions">
            {isFixable(c) && (
              <button
                className={`btn btn-small ${applied ? "btn-ghost" : "btn-fix"}`}
                onClick={(e) => {
                  e.stopPropagation();
                  onToggleFix();
                }}
              >
                {applied ? "Undo fix" : "Apply fix"}
              </button>
            )}
            <button
              className="btn btn-small btn-ghost"
              onClick={(e) => {
                e.stopPropagation();
                setOpen(!open);
              }}
            >
              {open ? "Hide details" : "Why this score?"}
            </button>
          </div>

          {open && (
            <div className="breakdown" onClick={(e) => e.stopPropagation()}>
              <div className="bd-grid">
                <Meter label="Evidence" value={r.breakdown.evidence} hint="confidence × sources that took a side" />
                <Meter label="Recency" value={r.breakdown.recency} hint={`half-life ${r.breakdown.half_life_days}d${r.breakdown.newest_age_days != null ? `, newest source ${r.breakdown.newest_age_days}d old` : ", undated"}`} />
                <Meter label="Authority" value={r.breakdown.authority} hint="official sources weigh more" />
              </div>
              <div className="mono small muted">score = {r.breakdown.formula} = {r.score}</div>
              <div className="mono small muted">tavily query: {r.search_query}</div>
              {r.search_summary && <div className="small muted">Tavily summary: {r.search_summary}</div>}
              {r.sources && r.sources.length > 0 && (
                <ul className="sources sources-all">
                  {r.sources.map((s) => (
                    <SourceRow key={s.index} s={s} withSnippet />
                  ))}
                </ul>
              )}
              {r.error && <div className="small err">{r.error}</div>}
            </div>
          )}
        </>
      )}
    </article>
  );
}

function SourceRow({ s, withSnippet }: { s: Source; withSnippet?: boolean }) {
  return (
    <li className={`source stance-${s.stance}`}>
      <span className="src-idx">[{s.index}]</span>
      <a href={s.url} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()}>
        {s.domain || s.url}
      </a>
      {s.published_date && <span className="src-date">{formatDate(s.published_date)}</span>}
      {s.official && <span className="chip chip-official">official</span>}
      <span className={`stance stance-${s.stance}`}>{s.stance}</span>
      {withSnippet && <div className="src-snippet">{s.title ? <b>{s.title}. </b> : null}{s.snippet}</div>}
    </li>
  );
}

function Meter({ label, value, hint }: { label: string; value: number; hint: string }) {
  return (
    <div className="meter" title={hint}>
      <div className="meter-top">
        <span>{label}</span>
        <span className="mono">{value.toFixed(2)}</span>
      </div>
      <div className="meter-bar">
        <div style={{ width: `${Math.round(value * 100)}%` }} />
      </div>
      <div className="meter-hint">{hint}</div>
    </div>
  );
}

function formatDate(s: string): string {
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  return d.toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" });
}
