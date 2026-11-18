import { useEffect, useReducer, useRef, useState } from "react";
import { documentURL, sampleDocument, streamDocumentVerify, uploadDocument, type AppConfig, type DocInfo } from "../api";
import { initialRun, isFixable, reducer } from "../state";
import { ClaimCard } from "./ClaimCard";
import { PdfViewer } from "./PdfViewer";
import { SummaryPanel } from "./SummaryPanel";
import { TracePanel } from "./TracePanel";

type View = "check" | "annotated" | "corrected";

const OUTPUTS = [
  { tone: "review", title: "Live highlights", text: "Claims light up on the page as they are verified. Click one for its sources, current value and score." },
  { tone: "stale", title: "Annotated PDF", text: "Your original file with a colour highlight and a reviewer note on every claim, plus a report page." },
  { tone: "fresh", title: "Corrected PDF", text: "Outdated statements rewritten in place with current facts. Each change is marked and keeps its source." },
];

export function Documents({ config }: { config: AppConfig }) {
  const [doc, setDoc] = useState<DocInfo | null>(null);
  const [loading, setLoading] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [run, dispatch] = useReducer(reducer, initialRun);
  const [active, setActive] = useState<string | null>(null);
  const [hover, setHover] = useState<string | null>(null);
  const [view, setView] = useState<View>("check");
  const [dragging, setDragging] = useState(false);
  const abort = useRef<AbortController | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);

  const maxMB = config.documents?.max_mb ?? 25;
  const running = run.phase === "extracting" || run.phase === "verifying";
  const done = run.phase === "done";
  const fixable = run.claims.filter(isFixable);
  // The corrected copy uses the fixes the user accepted, or all of them if none were picked.
  const fixIds = run.applied.length ? run.applied : fixable.map((c) => c.id);
  const focus = hover ?? active;

  useEffect(() => () => abort.current?.abort(), []);

  async function check(d: DocInfo) {
    abort.current?.abort();
    const ctrl = new AbortController();
    abort.current = ctrl;
    dispatch({ type: "start", text: d.text });
    try {
      await streamDocumentVerify(d.id, (ev) => dispatch({ type: "event", ev }), ctrl.signal);
    } catch (e) {
      if (!ctrl.signal.aborted) dispatch({ type: "fail", message: e instanceof Error ? e.message : String(e) });
    }
  }

  async function open(load: () => Promise<DocInfo>, label: string) {
    abort.current?.abort();
    setError(null);
    setLoading(label);
    try {
      const d = await load();
      setDoc(d);
      setView("check");
      setActive(null);
      void check(d);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(null);
    }
  }

  function pick(files: FileList | null) {
    const f = files?.[0];
    if (!f) return;
    if (f.type !== "application/pdf" && !f.name.toLowerCase().endsWith(".pdf")) {
      setError("That is not a PDF file.");
      return;
    }
    if (f.size > maxMB * 1024 * 1024) {
      setError(`The file is larger than ${maxMB} MB.`);
      return;
    }
    void open(() => uploadDocument(f), `Reading ${f.name}…`);
  }

  function close() {
    abort.current?.abort();
    setDoc(null);
    setError(null);
    dispatch({ type: "reset" });
  }

  function select(id: string) {
    setActive(id);
    document.getElementById(`card-${id}`)?.scrollIntoView({ behavior: "smooth", block: "nearest" });
    document.getElementById(`box-${id}`)?.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }

  if (!doc) {
    return (
      <div className="docs-empty">
        <section
          className={`panel dropzone ${dragging ? "is-drag" : ""}`}
          onDragOver={(e) => {
            e.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={(e) => {
            e.preventDefault();
            setDragging(false);
            pick(e.dataTransfer.files);
          }}
        >
          <div className="doc-glyph" aria-hidden>
            <span className="g-stale" />
            <span />
            <span className="g-review" />
            <span />
            <span className="g-fresh" />
          </div>
          <h2>Check a PDF your agent produced</h2>
          <p className="muted">
            Reports, briefs, proposals: Freshgate reads the PDF's text, finds the time-sensitive claims, verifies each one against live
            sources (Tavily, with models routed through Bifrost) and gives the file back with highlights, or with the outdated statements
            rewritten.
          </p>
          <div className="dz-actions">
            <button className="btn btn-primary" disabled={!!loading} onClick={() => fileInput.current?.click()}>
              Choose PDF
            </button>
            <button className="btn" disabled={!!loading} onClick={() => open(sampleDocument, "Typesetting the sample report…")}>
              Try the sample PDF
            </button>
          </div>
          <p className="muted small">
            or drop it here · up to {maxMB} MB · PDFs with a text layer (scans need OCR, not supported yet) · the first{" "}
            {config.max_chars.toLocaleString()} characters are checked
          </p>
          {loading && (
            <p className="dz-loading">
              <span className="pulse-dot" />
              {loading}
            </p>
          )}
          {error && <div className="banner banner-err">{error}</div>}
          <input
            ref={fileInput}
            type="file"
            accept="application/pdf,.pdf"
            hidden
            onChange={(e) => {
              pick(e.target.files);
              e.target.value = "";
            }}
          />
        </section>

        <div className="doc-outputs">
          {OUTPUTS.map((o) => (
            <section key={o.title} className="panel output">
              <h3>
                <span className={`dot dot-${o.tone}`} />
                {o.title}
              </h3>
              <p className="muted small">{o.text}</p>
            </section>
          ))}
        </div>

        <section className="panel">
          <div className="panel-head">
            <h2>For agents</h2>
            <span className="muted small">one call: PDF in, checked PDF out</span>
          </div>
          <div className="pad">
            <pre className="code">{`curl -s --data-binary @report.pdf -H 'Content-Type: application/pdf' \\
  '${window.location.origin}/api/documents/check?output=corrected' -o report-checked.pdf
# output=annotated | corrected | json   score and label in X-Freshgate-Score / X-Freshgate-Label`}</pre>
          </div>
        </section>
      </div>
    );
  }

  const pages = doc.total_pages === 1 ? "1 page" : `${doc.total_pages} pages`;
  return (
    <div className="inspector docs">
      <section className="panel answer-panel doc-panel">
        <div className="panel-head">
          <span className="doc-icon" aria-hidden>
            PDF
          </span>
          <div className="doc-title">
            <strong title={doc.name}>{doc.name}</strong>
            <span className="muted small">
              {pages} · {doc.text.length.toLocaleString()} characters
            </span>
          </div>
          <span className="spacer" />
          <button className="btn btn-ghost btn-small" onClick={close}>
            New document
          </button>
        </div>

        <div className="doc-toolbar">
          <div className="segmented">
            <button className={view === "check" ? "is-on" : ""} onClick={() => setView("check")}>
              Live check
            </button>
            <button className={view === "annotated" ? "is-on" : ""} disabled={!done} onClick={() => setView("annotated")}>
              Annotated PDF
            </button>
            <button
              className={view === "corrected" ? "is-on" : ""}
              disabled={!done || fixIds.length === 0}
              onClick={() => setView("corrected")}
              title={done && fixIds.length === 0 ? "Nothing to correct" : undefined}
            >
              Corrected PDF
            </button>
          </div>
          <span className="spacer" />
          {view !== "check" && (
            <a className="btn btn-small" href={view === "annotated" ? documentURL.annotated(doc.id) : documentURL.corrected(doc.id, fixIds)}>
              Download
            </a>
          )}
        </div>

        {view === "check" ? (
          <PdfViewer
            doc={doc}
            claims={run.claims}
            boxes={run.boxes}
            applied={run.applied}
            activeId={focus}
            scanning={run.phase === "extracting"}
            onSelect={select}
            onHover={setHover}
          />
        ) : (
          <iframe
            key={view === "corrected" ? `c-${fixIds.join()}` : "a"}
            className="pdf-frame"
            title={view === "annotated" ? "Annotated PDF" : "Corrected PDF"}
            src={view === "annotated" ? documentURL.annotated(doc.id, true) : documentURL.corrected(doc.id, fixIds, true)}
          />
        )}

        {run.phase === "error" && <div className="banner banner-err">{run.error}</div>}
        <div className="answer-foot">
          <div className="legend">
            <span className="lg lg-fresh">current</span>
            <span className="lg lg-review">needs review</span>
            <span className="lg lg-stale">outdated</span>
          </div>
          <span className="spacer" />
          {done && (
            <a className="btn btn-ghost" href={documentURL.annotated(doc.id)}>
              Annotated PDF ↓
            </a>
          )}
          {done && fixIds.length > 0 && (
            <a className="btn btn-fix" href={documentURL.corrected(doc.id, fixIds)}>
              Corrected PDF ↓ · {fixIds.length} fix{fixIds.length === 1 ? "" : "es"}
            </a>
          )}
        </div>
      </section>

      <aside className="side">
        <SummaryPanel
          run={run}
          config={config}
          idle={
            <>
              <h3>Freshness score</h3>
              <p className="muted small">Waiting for the document…</p>
            </>
          }
        />
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
          {done && run.claims.length === 0 && <p className="muted small pad">Nothing in this document looks time-sensitive.</p>}
        </div>
      </aside>

      <TracePanel trace={run.trace} running={running} demo={config.demo} />
    </div>
  );
}
