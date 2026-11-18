import type { ReactNode } from "react";

// A deliberately tiny Markdown renderer: LLM answers use a handful of inline marks, and the
// annotated view must render arbitrary slices of the answer (claim spans can start mid-line),
// which a full Markdown parser cannot do.

const INLINE = /(\*\*[^*\n]+?\*\*|~~[^~\n]+?~~|`[^`\n]+?`|\[[^\]\n]+?\]\([^)\s]+?\)|\*[^*\n]+?\*|_[^_\n]+?_)/g;

export function renderInline(text: string, key = "i"): ReactNode[] {
  // Heading markers at line starts and stray emphasis markers (left over when a claim span
  // cuts through **bold**) are display noise.
  const clean = text.replace(/(^|\n)#{1,6} /g, "$1");
  const out: ReactNode[] = [];
  let last = 0;
  let n = 0;
  for (const m of clean.matchAll(INLINE)) {
    const idx = m.index ?? 0;
    if (idx > last) out.push(stripStray(clean.slice(last, idx)));
    const tok = m[0];
    const k = `${key}-${n++}`;
    if (tok.startsWith("**")) out.push(<strong key={k}>{tok.slice(2, -2)}</strong>);
    else if (tok.startsWith("~~")) out.push(<del key={k}>{tok.slice(2, -2)}</del>);
    else if (tok.startsWith("`")) out.push(<code key={k}>{tok.slice(1, -1)}</code>);
    else if (tok.startsWith("[")) {
      const [, label, href] = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(tok) ?? [];
      out.push(
        <a key={k} href={href} target="_blank" rel="noreferrer">
          {label}
        </a>,
      );
    } else out.push(<em key={k}>{tok.slice(1, -1)}</em>);
    last = idx + tok.length;
  }
  if (last < clean.length) out.push(stripStray(clean.slice(last)));
  return out;
}

function stripStray(s: string): string {
  return s.replace(/\*\*/g, "");
}

/** Block-level rendering for chat replies: paragraphs, bullets and horizontal rules. */
export function Markdown({ text }: { text: string }) {
  const blocks: ReactNode[] = [];
  let list: string[] = [];
  const flush = () => {
    if (!list.length) return;
    const items = list;
    list = [];
    blocks.push(
      <ul key={`ul-${blocks.length}`}>
        {items.map((it, i) => (
          <li key={i}>{renderInline(it, `li-${blocks.length}-${i}`)}</li>
        ))}
      </ul>,
    );
  };
  text.split("\n").forEach((line, i) => {
    const t = line.trim();
    if (/^[-*] /.test(t)) {
      list.push(t.slice(2));
      return;
    }
    flush();
    if (t === "") return;
    if (/^-{3,}$/.test(t)) blocks.push(<hr key={`hr-${i}`} />);
    else blocks.push(<p key={`p-${i}`}>{renderInline(t, `p-${i}`)}</p>);
  });
  flush();
  return <div className="md">{blocks}</div>;
}
