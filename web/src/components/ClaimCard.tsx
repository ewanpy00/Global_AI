import { useState } from "react";
import { CATEGORY_LABEL, VERDICT_LABEL, type Source, type Verification } from "../api";
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
  fetching: "Reading the official page with Tavily Extract…",
  judging: "Comparing with sources via Bifrost…",
  following_up: "Evidence inconclusive: running a follow-up search…",
};

const ORIGIN_TEXT: Record<string, string> = {
  official: "official site search",
  news: "news, past year",
  followup: "follow-up search",
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

          <Signals r={r} />

          {r.sources && r.sources.some((s) => s.stance !== "neutral" || s.grounded === false) && (
            <ul className="sources">
              {r.sources.filter((s) => s.stance !== "neutral" || s.grounded === false).map((s) => (
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
              <div className="mono small muted">
                tavily {r.search_queries && r.search_queries.length > 1 ? "queries" : "query"}: {(r.search_queries ?? [r.search_query]).join("  ·  ")}
              </div>
              {r.jury && (
                <div className="small muted">
                  Second judge <span className="mono">{r.jury.model}</span>: {VERDICT_LABEL[r.jury.verdict]} ({Math.round(r.jury.confidence * 100)}%)
                  {r.jury.explanation ? `. ${r.jury.explanation}` : ""}
                </div>
              )}
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

/** One-line badges for the accuracy features that acted on this claim. */
function Signals({ r }: { r: Verification }) {
  const items: { cls: string; text: string; title: string }[] = [];
  if (r.jury) {
    const a = r.jury.agreement;
    items.push({
      cls: a === "agree" ? "sig-ok" : a === "contested" ? "sig-bad" : "sig-warn",
      text: a === "agree" ? "2 judges agree" : a === "contested" ? "judges disagree" : "2nd judge unsure",
      title: `Independent second judge via Bifrost: ${r.jury.model} said ${r.jury.verdict}`,
    });
  }
  if (r.grounding && r.grounding.cited > 0) {
    const g = r.grounding;
    items.push({
      cls: g.verified === g.cited ? "sig-ok" : g.verified === 0 ? "sig-bad" : "sig-warn",
      text: `quotes ${g.verified}/${g.cited} verified`,
      title: "Every stance must quote its source; quotes not found verbatim in the page are discarded",
    });
  }
  if ((r.hops ?? 1) > 1) {
    items.push({ cls: "sig-info", text: "follow-up search", title: `First evidence was inconclusive, so the judge asked for: ${r.follow_up_query ?? ""}` });
  }
  if (r.sources?.some((s) => s.fetched)) {
    items.push({ cls: "sig-info", text: "read full official page", title: "The snippet was too thin, so the page was fetched with Tavily Extract" });
  }
  if (items.length === 0) return null;
  return (
    <div className="signals">
      {items.map((i) => (
        <span key={i.text} className={`signal ${i.cls}`} title={i.title}>
          {i.text}
        </span>
      ))}
    </div>
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
      {s.origin && ORIGIN_TEXT[s.origin] && <span className="chip">{ORIGIN_TEXT[s.origin]}</span>}
      {s.fetched && <span className="chip chip-official">full page</span>}
      <span className={`stance stance-${s.stance}`}>{s.stance}</span>
      {s.quote && (
        <div className={`src-quote ${s.grounded === false ? "is-ungrounded" : ""}`}>
          {s.grounded === true ? "✓ " : s.grounded === false ? "✗ not in source: " : ""}“{s.quote}”
        </div>
      )}
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
