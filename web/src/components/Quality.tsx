import { useEffect, useState } from "react";
import { fetchEval, type EvalRun } from "../api";

type Fmt = "pct" | "num3" | "sec" | "num0" | "num2";

interface Row {
  name: string;
  hint?: string;
  get: (r: EvalRun) => number | undefined;
  fmt: Fmt;
  better: "up" | "down" | "none";
}

const ROWS: Row[] = [
  { name: "Verdict accuracy", get: (r) => r.metrics.accuracy, fmt: "pct", better: "up", hint: "current / stale / unverifiable, against the gold label" },
  { name: "Macro-F1", get: (r) => r.metrics.macro_f1, fmt: "num3", better: "up" },
  { name: "Stale precision", get: (r) => r.metrics.per_class.stale?.precision, fmt: "pct", better: "up", hint: "flagged as stale and really stale" },
  { name: "Stale recall", get: (r) => r.metrics.per_class.stale?.recall, fmt: "pct", better: "up", hint: "stale claims that were caught" },
  { name: "Coverage", get: (r) => r.metrics.coverage, fmt: "pct", better: "up", hint: "knowable claims that got a definite verdict" },
  { name: "Current-value accuracy", get: (r) => r.metrics.value_accuracy, fmt: "pct", better: "up", hint: "caught stale claims whose new value is right" },
  { name: "Brier score", get: (r) => r.metrics.brier, fmt: "num3", better: "down", hint: "freshness score as a probability; lower is better" },
  { name: "Calibration error (ECE)", get: (r) => r.metrics.ece, fmt: "num3", better: "down" },
  { name: "Extraction recall", get: (r) => r.extraction?.recall, fmt: "pct", better: "up", hint: "labelled claims the extractor found in full answers" },
  { name: "Extraction precision", get: (r) => r.extraction?.precision, fmt: "pct", better: "up" },
  { name: "Latency p50", get: (r) => r.metrics.latency_p50_ms / 1000, fmt: "sec", better: "down" },
  { name: "Latency p95", get: (r) => r.metrics.latency_p95_ms / 1000, fmt: "sec", better: "down" },
  { name: "Model calls per claim", get: (r) => r.metrics.llm_calls_per_claim, fmt: "num2", better: "none" },
  { name: "Tokens per claim", get: (r) => r.metrics.tokens_per_claim, fmt: "num0", better: "none" },
  { name: "Tavily credits per claim", get: (r) => r.metrics.credits_per_claim, fmt: "num2", better: "none" },
];

function fmt(v: number | undefined, f: Fmt): string {
  if (v == null || Number.isNaN(v)) return "–";
  switch (f) {
    case "pct":
      return `${(v * 100).toFixed(1)}%`;
    case "num3":
      return v.toFixed(3);
    case "num2":
      return v.toFixed(2);
    case "num0":
      return Math.round(v).toLocaleString();
    case "sec":
      return `${v.toFixed(1)} s`;
  }
}

function delta(row: Row, base: EvalRun, full: EvalRun) {
  const a = row.get(base);
  const b = row.get(full);
  if (a == null || b == null || row.better === "none") return null;
  const d = b - a;
  if (Math.abs(d) < 1e-9) return { text: "=", cls: "d-same" };
  const good = row.better === "up" ? d > 0 : d < 0;
  const text = row.fmt === "pct" ? `${d > 0 ? "+" : ""}${(d * 100).toFixed(1)} pp` : `${d > 0 ? "+" : ""}${row.fmt === "sec" ? d.toFixed(1) : d.toFixed(3)}`;
  return { text, cls: good ? "d-good" : "d-bad" };
}

const CLASSES = ["current", "stale", "unverifiable"];

export function Quality() {
  const [data, setData] = useState<{ runs: EvalRun[] } | null | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);

  function load() {
    fetchEval().then(setData, (e) => setError(String(e)));
  }
  useEffect(load, []);

  if (error) return <div className="banner banner-err">{error}</div>;
  if (data === undefined) return <p className="muted pad">Loading benchmark…</p>;
  if (data === null || data.runs.length === 0) {
    return (
      <section className="panel quality-empty">
        <h2>Benchmark</h2>
        <p className="muted">No results yet. Run the benchmark from the repo root:</p>
        <pre className="code">{`make eval        # live: ~48 labelled claims + 5 answers, plain pipeline vs Freshgate
make eval-demo   # offline, on the demo fixtures (checks the harness only)`}</pre>
        <button className="btn" onClick={load}>
          Reload
        </button>
      </section>
    );
  }

  const full = data.runs[data.runs.length - 1];
  const base = data.runs.length > 1 ? data.runs[0] : null;
  const misses = full.claims.filter((c) => c.gold !== c.predicted);
  const tiles = [ROWS[0], ROWS[1], ROWS[3], ROWS[4]];

  return (
    <div className="quality">
      <section className="panel quality-head">
        <div>
          <h2>Benchmark</h2>
          <p className="muted small">
            dataset <span className="mono">{full.dataset}</span> · {full.metrics.n} labelled claims
            {full.extraction ? ` + ${full.extraction.answers} answers` : ""} · run {new Date(full.run_at).toLocaleString()} · models{" "}
            {Object.entries(full.models)
              .map(([k, v]) => `${k}: ${v}`)
              .join(", ")}
          </p>
        </div>
        <span className="spacer" />
        <button className="btn btn-ghost" onClick={load}>
          Reload
        </button>
      </section>
      {full.demo && (
        <div className="banner banner-top">
          These numbers come from the offline demo fixtures (fictional company). They show that the benchmark works, not how accurate Freshgate is
          on the real web. Run <code>make eval</code> with API keys for real numbers.
        </div>
      )}

      <div className="q-tiles">
        {tiles.map((r) => {
          const d = base ? delta(r, base, full) : null;
          return (
            <div key={r.name} className="panel q-tile" title={r.hint}>
              <div className="q-tile-name">{r.name}</div>
              <div className="q-tile-value">{fmt(r.get(full), r.fmt)}</div>
              {d && <div className={`q-delta ${d.cls}`}>{d.text} vs plain pipeline</div>}
            </div>
          );
        })}
      </div>

      <div className="q-grid">
        <section className="panel">
          <div className="panel-head">
            <h3>{base ? "Plain pipeline vs Freshgate" : "Metrics"}</h3>
          </div>
          <table className="q-table">
            <thead>
              <tr>
                <th>Metric</th>
                {data.runs.map((r) => (
                  <th key={r.label}>{r.label === "baseline" ? "plain" : r.label}</th>
                ))}
                {base && <th>Δ</th>}
              </tr>
            </thead>
            <tbody>
              {ROWS.map((row) => {
                const d = base ? delta(row, base, full) : null;
                return (
                  <tr key={row.name} title={row.hint}>
                    <td>{row.name}</td>
                    {data.runs.map((r) => (
                      <td key={r.label} className="mono">
                        {fmt(row.get(r), row.fmt)}
                      </td>
                    ))}
                    {base && <td className={`mono ${d?.cls ?? ""}`}>{d?.text ?? ""}</td>}
                  </tr>
                );
              })}
            </tbody>
          </table>
          {base && (
            <p className="muted small pad">
              Plain = one search and one judge per claim. Freshgate adds the official-site search, full-page reading, a follow-up search when the
              evidence is inconclusive, quote grounding{full.features.jury ? ", a second judge on another provider" : ""} and shared searches.
            </p>
          )}
        </section>

        <section className="panel">
          <div className="panel-head">
            <h3>Confusion matrix</h3>
            <span className="muted small">rows: gold · columns: Freshgate</span>
          </div>
          <table className="q-table q-confusion">
            <thead>
              <tr>
                <th />
                {CLASSES.map((c) => (
                  <th key={c}>{c}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {CLASSES.map((g) => (
                <tr key={g}>
                  <th>{g}</th>
                  {CLASSES.map((p) => {
                    const n = full.metrics.confusion[g]?.[p] ?? 0;
                    return (
                      <td key={p} className={`mono ${n ? (g === p ? "cm-hit" : "cm-miss") : "muted"}`}>
                        {n}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>

          <h4 className="pad-x">Misses ({misses.length})</h4>
          {misses.length === 0 ? (
            <p className="muted small pad">Every claim got its gold label.</p>
          ) : (
            <ul className="q-misses">
              {misses.map((m) => (
                <li key={m.id}>
                  <span className="mono small">{m.id}</span> gold <b>{m.gold}</b> → got <b>{m.predicted}</b>
                  <div className="muted small">
                    “{m.text}”{m.current_value ? ` · judge said: ${m.current_value}` : ""}
                    {m.error ? ` · ${m.error}` : ""}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
    </div>
  );
}
