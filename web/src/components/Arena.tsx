import { useEffect, useRef, useState } from "react";
import { LABEL_TEXT, streamArena, type AppConfig, type ArenaEntry, type RawEvent, type StreamEvent } from "../api";
import { initialRun, reducer, type RunState } from "../state";
import { AnnotatedText } from "./AnnotatedText";
import { ScoreGauge } from "./ScoreGauge";

interface Lane {
  model: string;
  phase: "waiting" | "answering" | "checking" | "done" | "error";
  run: RunState;
  answerMs?: number;
  entry?: ArenaEntry;
  error?: string;
}

const LIVE_QUESTIONS = [
  "What are the latest stable versions of Python, Node.js (LTS) and React?",
  "Who are the current CEOs of Intel, Starbucks, Boeing and Disney?",
  "How much do ChatGPT Plus, GitHub Copilot and Netflix's ad plan cost per month?",
  "What are the newest flagship models from OpenAI, Anthropic and Google?",
];

const DEMO_QUESTIONS = ["Is Nimbus Cloud a good choice for a small team? Cover price, SDK, leadership and compliance."];

export function Arena({ config }: { config: AppConfig }) {
  const questions = config.demo ? DEMO_QUESTIONS : LIVE_QUESTIONS;
  const [question, setQuestion] = useState(questions[0]);
  const [models, setModels] = useState<string[]>(config.arena?.models ?? []);
  const [adding, setAdding] = useState("");
  const [lanes, setLanes] = useState<Lane[]>([]);
  const [board, setBoard] = useState<ArenaEntry[] | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const abort = useRef<AbortController | null>(null);

  useEffect(() => () => abort.current?.abort(), []);

  function onEvent(ev: RawEvent) {
    const d = ev.data;
    switch (ev.type) {
      case "arena_answer":
        setLanes((ls) =>
          ls.map((l) =>
            l.model !== d.model
              ? l
              : d.error
                ? { ...l, phase: "error", error: d.error, answerMs: d.latency_ms }
                : { ...l, phase: "checking", answerMs: d.latency_ms, run: reducer(initialRun, { type: "start", text: d.text }) },
          ),
        );
        break;
      case "arena_event":
        setLanes((ls) => ls.map((l) => (l.model === d.model ? { ...l, run: reducer(l.run, { type: "event", ev: d.event as StreamEvent }) } : l)));
        break;
      case "arena_result":
        setLanes((ls) => ls.map((l) => (l.model === d.model ? { ...l, phase: d.error ? "error" : "done", entry: d, error: d.error || l.error } : l)));
        break;
      case "arena_done":
        setBoard(d.leaderboard);
        break;
      case "error":
        setError(d.message);
        break;
    }
  }

  async function start() {
    if (!question.trim() || models.length === 0) return;
    abort.current?.abort();
    const ctrl = new AbortController();
    abort.current = ctrl;
    setError(null);
    setBoard(null);
    setLanes(models.map((m) => ({ model: m, phase: "answering", run: initialRun })));
    setRunning(true);
    try {
      await streamArena(question.trim(), models, onEvent, ctrl.signal);
    } catch (e) {
      if (!ctrl.signal.aborted) setError(e instanceof Error ? e.message : String(e));
    } finally {
      setRunning(false);
    }
  }

  function addModel() {
    const m = adding.trim();
    if (m && m.includes("/") && !models.includes(m) && models.length < 6) setModels([...models, m]);
    setAdding("");
  }

  const ranked = board ?? [];
  const rankOf = (m: string) => ranked.find((e) => e.model === m)?.rank;
  const ordered = board ? [...lanes].sort((a, b) => (rankOf(a.model) ?? 99) - (rankOf(b.model) ?? 99)) : lanes;

  return (
    <div className="arena">
      <section className="panel arena-hero">
        <div className="arena-intro">
          <h2>Staleness Arena</h2>
          <p className="muted">
            One question goes to every model through <b className="t-bifrost">Bifrost</b>. Every answer is fact-checked live with{" "}
            <b className="t-tavily">Tavily</b>, and the models are ranked by how current their facts are.
          </p>
        </div>
        <div className="arena-form">
          <textarea className="arena-q" value={question} onChange={(e) => setQuestion(e.target.value)} rows={2} placeholder="Ask something time-sensitive…" />
          <div className="arena-suggest">
            {questions.map((q) => (
              <button key={q} className="chip chip-btn" onClick={() => setQuestion(q)} disabled={running}>
                {q.length > 60 ? q.slice(0, 58) + "…" : q}
              </button>
            ))}
          </div>
          <div className="arena-models">
            {models.map((m) => (
              <span key={m} className="model-chip mono">
                {m}
                {!running && (
                  <button aria-label={`remove ${m}`} onClick={() => setModels(models.filter((x) => x !== m))}>
                    ×
                  </button>
                )}
              </span>
            ))}
            {!config.demo && models.length < 6 && (
              <input
                className="model-input mono"
                value={adding}
                placeholder="+ provider/model"
                onChange={(e) => setAdding(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && addModel()}
                onBlur={addModel}
                disabled={running}
              />
            )}
            <span className="spacer" />
            <button className="btn btn-primary" onClick={start} disabled={running || !question.trim() || models.length === 0}>
              {running ? "Running…" : `Run ${models.length} model${models.length === 1 ? "" : "s"}`}
            </button>
          </div>
          {models.length < 2 && !config.demo && (
            <p className="muted small">Add a second model (another provider key, e.g. GEMINI_API_KEY + GROQ_API_KEY) to make it a contest.</p>
          )}
          {config.demo && <p className="muted small">Demo mode: three fictional models with different training cut-offs answer about the fictional Nimbus Cloud.</p>}
          {error && <div className="banner banner-err">{error}</div>}
        </div>
      </section>

      {board && board.length > 0 && <Leaderboard board={board} />}

      {lanes.length > 0 && (
        <div className="lanes">
          {ordered.map((l) => (
            <LaneCard key={l.model} lane={l} rank={rankOf(l.model)} />
          ))}
        </div>
      )}
    </div>
  );
}

function Leaderboard({ board }: { board: ArenaEntry[] }) {
  const best = Math.max(1, ...board.map((e) => e.score));
  return (
    <section className="panel leaderboard">
      <div className="panel-head">
        <h2>Leaderboard</h2>
        <span className="muted small">freshness score of each model's answer · higher is more current</span>
      </div>
      <ol className="lb-rows">
        {board.map((e) => (
          <li key={e.model} className={`lb-row ${e.rank === 1 && !e.error ? "is-winner" : ""}`}>
            <span className="lb-rank">{e.rank === 1 && !e.error ? "🏆" : `#${e.rank}`}</span>
            <span className="lb-model mono">{e.model}</span>
            <span className="lb-bar">
              {e.error ? (
                <span className="err small">{e.error}</span>
              ) : (
                <span className={`lb-fill fill-${e.label}`} style={{ width: `${Math.max(3, (e.score / best) * 100)}%` }}>
                  {e.score}
                </span>
              )}
            </span>
            <span className="lb-meta small">
              {e.error ? "" : (
                <>
                  <b className={e.stale ? "label-stale" : "label-fresh"}>{e.stale} stale</b> · {e.current} current · {e.unverifiable} unverified
                </>
              )}
            </span>
          </li>
        ))}
      </ol>
    </section>
  );
}

function LaneCard({ lane: l, rank }: { lane: Lane; rank?: number }) {
  const done = l.run.claims.filter((c) => c.status === "done").length;
  const score = l.entry && !l.entry.error ? l.entry.score : l.run.summary?.score ?? null;
  const label = l.entry && !l.entry.error ? (l.entry.label as "fresh" | "review" | "stale") : l.run.summary?.label;
  return (
    <section className={`panel lane ${rank === 1 ? "is-winner" : ""}`}>
      <div className="lane-head">
        <div>
          <div className="lane-model mono">{l.model}</div>
          <div className="muted small">
            {l.phase === "answering" && "answering via Bifrost…"}
            {l.phase === "checking" &&
              (l.run.claims.length ? `checking ${done}/${l.run.claims.length} claims with Tavily…` : "finding claims…")}
            {l.phase === "done" && l.entry && `${l.entry.claims} claims · answered in ${(l.answerMs! / 1000).toFixed(1)} s · checked in ${(l.entry.check_ms / 1000).toFixed(1)} s`}
            {l.phase === "error" && <span className="err">{l.error}</span>}
          </div>
        </div>
        <span className="spacer" />
        {rank && <span className="lane-rank">#{rank}</span>}
        <ScoreGauge score={l.phase === "error" ? null : score} label={label} size={58} stroke={6} />
      </div>
      {l.phase === "answering" && (
        <div className="lane-skeleton">
          <span />
          <span />
          <span />
        </div>
      )}
      {l.run.text && (
        <div className="lane-answer">
          <AnnotatedText text={l.run.text} claims={l.run.claims} applied={[]} activeId={null} onSelect={() => {}} onHover={() => {}} />
        </div>
      )}
      {l.phase === "done" && l.entry && !l.entry.error && (
        <div className={`lane-foot label-bg-${l.entry.label}`}>
          {LABEL_TEXT[l.entry.label as "fresh"]} · {l.entry.stale} stale fact{l.entry.stale === 1 ? "" : "s"}
        </div>
      )}
    </section>
  );
}
