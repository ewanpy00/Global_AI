import { useMemo } from "react";
import { VERDICT_LABEL, documentURL, type Box, type DocInfo } from "../api";
import { isFixable, type ClaimState } from "../state";
import { tone } from "./AnnotatedText";

interface Props {
  doc: DocInfo;
  claims: ClaimState[];
  boxes: Record<string, Box[]>;
  applied: string[];
  activeId: string | null;
  scanning: boolean;
  onSelect: (id: string) => void;
  onHover: (id: string | null) => void;
}

const pct = (v: number) => `${(v * 100).toFixed(3)}%`;

function tip(c: ClaimState): string {
  if (!c.result) return `Claim ${c.id.slice(1)} · checking…`;
  const r = c.result;
  return `${VERDICT_LABEL[r.verdict]} · freshness ${r.score}/100${r.current_value ? ` · now: ${r.current_value}` : ""}`;
}

/** The PDF's pages, rendered by the backend, with each claim drawn over its text like a highlighter. */
export function PdfViewer({ doc, claims, boxes, applied, activeId, scanning, onSelect, onHover }: Props) {
  const byPage = useMemo(() => {
    const m = new Map<number, { claim: ClaimState; box: Box; first: boolean }[]>();
    for (const c of claims) {
      (boxes[c.id] ?? []).forEach((box, i) => {
        const list = m.get(box.page) ?? [];
        list.push({ claim: c, box, first: i === 0 });
        m.set(box.page, list);
      });
    }
    return m;
  }, [claims, boxes]);

  return (
    <div className="pdf-scroll">
      {doc.pages.map((p) => (
        <div key={p.index} className="pdf-page" style={{ aspectRatio: `${p.width} / ${p.height}` }}>
          <img src={documentURL.page(doc.id, p.index + 1)} alt={`Page ${p.index + 1}`} loading={p.index < 2 ? "eager" : "lazy"} draggable={false} />
          {scanning && p.index < doc.checked_pages && <div className="pdf-scan" />}
          {(byPage.get(p.index) ?? []).map(({ claim: c, box: b, first }, k) => {
            const fixed = applied.includes(c.id) && isFixable(c);
            const working = c.status === "searching" || c.status === "judging";
            return (
              <button
                key={`${c.id}-${k}`}
                id={first ? `box-${c.id}` : undefined}
                type="button"
                className={`pdf-box box-${fixed ? "fixed" : tone(c)} ${activeId === c.id ? "is-active" : ""} ${working ? "is-working" : ""}`}
                style={{
                  left: `calc(${pct(b.x)} - 2px)`,
                  top: `calc(${pct(b.y)} - 1px)`,
                  width: `calc(${pct(b.w)} + 4px)`,
                  height: `calc(${pct(b.h)} + 2px)`,
                }}
                title={tip(c)}
                aria-label={tip(c)}
                onClick={() => onSelect(c.id)}
                onMouseEnter={() => onHover(c.id)}
                onMouseLeave={() => onHover(null)}
              >
                {first && <span className="pdf-tag">{c.id.slice(1)}</span>}
              </button>
            );
          })}
          <span className="pdf-page-num">
            {p.index + 1} / {doc.total_pages}
          </span>
        </div>
      ))}
      {doc.checked_pages < doc.total_pages && (
        <p className="muted small pdf-note">
          Text from pages 1–{doc.checked_pages} was checked; one run covers up to the per-run character limit
          {doc.pages.length < doc.total_pages ? `, and the first ${doc.pages.length} pages are shown` : ""}.
        </p>
      )}
    </div>
  );
}
