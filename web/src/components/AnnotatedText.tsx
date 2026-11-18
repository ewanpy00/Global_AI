import type { ReactNode } from "react";
import { isFixable, type ClaimState } from "../state";
import { renderInline } from "./Markdown";

interface Props {
  text: string;
  claims: ClaimState[];
  applied: string[];
  activeId: string | null;
  onSelect: (id: string) => void;
  onHover: (id: string | null) => void;
}

export function tone(c: ClaimState): string {
  if (c.status !== "done" || !c.result) return "pending";
  return c.result.label;
}

/** The answer with every claim span highlighted by its freshness, and accepted fixes shown as a diff. */
export function AnnotatedText({ text, claims, applied, activeId, onSelect, onHover }: Props) {
  const spans = claims.filter((c) => c.located).sort((a, b) => a.start - b.start);
  const nodes: ReactNode[] = [];
  let pos = 0;
  for (const c of spans) {
    if (c.start < pos) continue; // backend removes overlaps; guard anyway
    if (c.start > pos) nodes.push(<span key={`t-${pos}`}>{renderInline(text.slice(pos, c.start), `t-${pos}`)}</span>);
    const original = text.slice(c.start, c.end);
    const fixed = applied.includes(c.id) && isFixable(c);
    nodes.push(
      <mark
        key={c.id}
        id={`hl-${c.id}`}
        className={`hl hl-${fixed ? "fixed" : tone(c)} ${activeId === c.id ? "is-active" : ""} ${c.status === "searching" || c.status === "judging" ? "is-working" : ""}`}
        onClick={() => onSelect(c.id)}
        onMouseEnter={() => onHover(c.id)}
        onMouseLeave={() => onHover(null)}
        title={c.result ? `${c.result.score}/100 · ${c.result.explanation}` : "Checking…"}
      >
        {fixed ? (
          <>
            <del className="diff-old">{renderInline(original, `o-${c.id}`)}</del>
            <ins className="diff-new">{c.result!.corrected_text}</ins>
          </>
        ) : (
          renderInline(original, `m-${c.id}`)
        )}
        <sup className="hl-tag">{c.id.slice(1)}</sup>
      </mark>,
    );
    pos = c.end;
  }
  if (pos < text.length) nodes.push(<span key={`t-${pos}`}>{renderInline(text.slice(pos), `t-${pos}`)}</span>);
  return <div className="answer-text">{nodes}</div>;
}
