import { useEffect, useReducer, useRef, useState } from "react";
import { LABEL_TEXT, VERDICT_LABEL, streamVerify, type AppConfig, type Verdict } from "../api";
import { correctedText, initialRun, isFixable, reducer } from "../state";
import { AnnotatedText } from "./AnnotatedText";
import { ClaimCard } from "./ClaimCard";
import { ScoreGauge } from "./ScoreGauge";
import { TracePanel } from "./TracePanel";

interface Props {
  config: AppConfig;
  /** Text pushed from another view (e.g. a connector chat reply) to check right away. */
  incoming: { text: string; nonce: number } | null;
}

const VERDICT_ORDER: Verdict[] = ["outdated", "incorrect", "unverifiable", "current"];

export function Inspector({ config, incoming }: Props) {
  const [run, dispatch] = useReducer(reducer, initialRun);
  const [draft, setDraft] = useState(config.samples[0]?.text ?? "");
  const [active, setActive] = useState<string | null>(null);
  const [hover, setHover] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const abort = useRef<AbortController | null>(null);

  const running = run.phase === "extracting" || run.phase === "verifying";
  const showResult = run.phase !== "idle" && !(run.phase === "error" && run.claims.length === 0);

  async function verify(text: string) {
    text = text.trim();
    if (!text) return;
    abort.current?.abort();
    const ctrl = new AbortController();
    abort.current = ctrl;
    setActive(null);
    dispatch({ type: "start", text });
    try {
      await streamVerify(text, (ev) => dispatch({ type: "event", ev }), ctrl.signal);
    } catch (e) {
      if (!ctrl.signal.aborted) dispatch({ type: "fail", message: e instanceof Error ? e.message : String(e) });
    }
  }

  useEffect(() => {
    if (incoming) {
      setDraft(incoming.text);
      void verify(incoming.text);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [incoming?.nonce]);

  useEffect(() => () => abort.current?.abort(), []);

  function select(id: string) {
    setActive(id);
    document.getElementById(`card-${id}`)?.scrollIntoView({ behavior: "smooth", block: "nearest" });
    document.getElementById(`hl-${id}`)?.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }

  function edit() {
    abort.current?.abort();
    setDraft(run.text || draft);
    dispatch({ type: "reset" });
  }

  async function copyCorrected() {
    await navigator.clipboard.writeText(correctedText(run.text, run.claims, run.applied));
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }

  function downloadReport() {
    if (!run.report) return;
    const blob = new Blob([JSON.stringify(run.report, null, 2)], { type: "application/json" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `freshgate-report-${run.report.id}.json`;
    a.click();
    URL.revokeObjectURL(a.href);
  }

  const done = run.claims.filter((c) => c.status === "done").length;
  const fixable = run.claims.filter(isFixable);
  const focus = hover ?? active;

  return (
    <div className="inspector">
      <section className="panel answer-panel">
        <div className="panel-head">
          <h2>AI answer</h2>
          <span className="spacer" />
          {!showResult && config.samples.length > 0 && (
            <select
              className="select"
              value=""
              onChange={(e) => {
                const s = config.samples.find((x) => x.id === e.target.value);
                if (s) setDraft(s.text);
              }}
            >
              <option value="" disabled>
                Load a sample…
              </option>
              {config.samples.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.title}
                </option>
              ))}
            </select>
          )}
          {showResult && (
            <button className="btn btn-ghost" onClick={edit}>
              Edit text
            </button>
          )}
        </div>

        {!showResult ? (
          <>
            <textarea
              className="answer-input"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder="Paste an answer from ChatGPT, Claude, Gemini or your own agent…"
              spellCheck={false}
            />
            {run.phase === "error" && <div className="banner banner-err">{run.error}</div>}
            <div className="answer-foot">
              <span className="muted small">
                {draft.length.toLocaleString()} / {config.max_chars.toLocaleString()} characters · up to {config.max_claims} claims
              </span>
              <span className="spacer" />
              <button className="btn btn-primary" disabled={!draft.trim()} onClick={() => verify(draft)}>
                Check freshness
              </button>
            </div>
          </>
        ) : (
          <>
            <AnnotatedText
              text={run.text}
              claims={run.claims}
              applied={run.applied}
              activeId={focus}
              onSelect={select}
              onHover={setHover}
            />
            {run.phase === "extracting" && <div className="scan-line" />}
            {run.phase === "error" && <div className="banner banner-err">{run.error}</div>}
            <div className="answer-foot">
              <div className="legend">
                <span className="lg lg-fresh">current</span>
                <span className="lg lg-review">needs review</span>
                <span className="lg lg-stale">outdated</span>
              </div>
              <span className="spacer" />
              {fixable.length > 0 &&
                (run.applied.length < fixable.length ? (
                  <button className="btn btn-fix" onClick={() => dispatch({ type: "applyAll" })}>
                    Apply {fixable.length} fix{fixable.length === 1 ? "" : "es"}
                  </button>
                ) : (
                  <button className="btn btn-ghost" onClick={() => dispatch({ type: "clearFixes" })}>
                    Undo fixes
                  </button>
                ))}
              {run.applied.length > 0 && (
                <button className="btn btn-ghost" onClick={copyCorrected}>
                  {copied ? "Copied ✓" : "Copy corrected"}
                </button>
              )}
              {run.report && (
                <button className="btn btn-ghost" onClick={downloadReport}>
                  Report JSON
                </button>
              )}
            </div>
          </>
        )}
      </section>

      <aside className="side">
        <section className="panel summary">
          <ScoreGauge
            score={run.summary ? run.summary.score : null}
            label={run.summary?.label}
            caption={run.summary ? "freshness" : undefined}
          />
          <div className="summary-body">
            {run.phase === "idle" && (
              <>
                <h3>Freshness score</h3>
                <p className="muted small">
                  Finds time-sensitive claims (prices, versions, laws, leadership), re-checks each one against live web sources and scores
                  how current it is.
                </p>
              </>
            )}
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
              </>
            )}
            {run.phase === "error" && run.claims.length === 0 && <h3 className="label-stale">Check failed</h3>}
          </div>
        </section>

        <div className="cards">
          {run.claims.map((c) => (
            <ClaimCard
              key={c.id}
              claim={c}
              active={focus === c.id}
              applied={run.applied.includes(c.id)}
              judgeModel={config.models.judge}
              onSelect={() => select(c.id)}
              onHover={(on) => setHover(on ? c.id : null)}
              onToggleFix={() => dispatch({ type: "toggleFix", id: c.id })}
            />
          ))}
          {run.phase === "done" && run.claims.length === 0 && (
            <p className="muted small pad">Nothing in this answer looks time-sensitive.</p>
          )}
        </div>
      </aside>

      <TracePanel trace={run.trace} running={running} demo={config.demo} />
    </div>
  );
}
