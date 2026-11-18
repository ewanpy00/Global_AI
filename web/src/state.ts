import type { Box, Claim, Report, StreamEvent, Summary, TraceEntry, Verification } from "./api";

export type ClaimStatus = "queued" | "searching" | "judging" | "done";

export interface ClaimState extends Claim {
  status: ClaimStatus;
  result?: Verification;
}

export type Phase = "idle" | "extracting" | "verifying" | "done" | "error";

export interface RunState {
  phase: Phase;
  text: string;
  claims: ClaimState[];
  trace: TraceEntry[];
  summary?: Summary;
  report?: Report;
  error?: string;
  /** Claim ids whose correction the user accepted. */
  applied: string[];
  /** Where each claim sits on the pages (documents only). */
  boxes: Record<string, Box[]>;
}

export const initialRun: RunState = { phase: "idle", text: "", claims: [], trace: [], applied: [], boxes: {} };

export type Action =
  | { type: "start"; text: string }
  | { type: "event"; ev: StreamEvent }
  | { type: "fail"; message: string }
  | { type: "reset" }
  | { type: "toggleFix"; id: string }
  | { type: "applyAll" }
  | { type: "clearFixes" };

export function reducer(s: RunState, a: Action): RunState {
  switch (a.type) {
    case "start":
      return { ...initialRun, phase: "extracting", text: a.text };
    case "fail":
      return { ...s, phase: "error", error: a.message };
    case "reset":
      return { ...initialRun, text: s.text };
    case "toggleFix":
      return {
        ...s,
        applied: s.applied.includes(a.id) ? s.applied.filter((x) => x !== a.id) : [...s.applied, a.id],
      };
    case "applyAll":
      return { ...s, applied: s.claims.filter(isFixable).map((c) => c.id) };
    case "clearFixes":
      return { ...s, applied: [] };
    case "event":
      return applyEvent(s, a.ev);
  }
}

function applyEvent(s: RunState, ev: StreamEvent): RunState {
  switch (ev.type) {
    case "stage":
      if (ev.data.stage === "done") return s;
      return { ...s, phase: ev.data.stage };
    case "claims":
      return { ...s, claims: ev.data.claims.map((c) => ({ ...c, status: "queued" })) };
    case "claim_status":
      return {
        ...s,
        claims: s.claims.map((c) => (c.id === ev.data.claim_id ? { ...c, status: ev.data.status } : c)),
      };
    case "claim_result":
      return {
        ...s,
        claims: s.claims.map((c) =>
          c.id === ev.data.claim_id ? { ...c, status: "done", result: ev.data.result } : c,
        ),
      };
    case "trace":
      return { ...s, trace: [...s.trace, ev.data] };
    case "summary":
      return { ...s, summary: ev.data };
    case "boxes":
      return { ...s, boxes: ev.data.boxes ?? {} };
    case "report":
      return { ...s, phase: "done", report: ev.data, summary: ev.data.summary };
    case "error":
      return { ...s, phase: "error", error: ev.data.message };
  }
}

export function isFixable(c: ClaimState): boolean {
  return (
    c.located &&
    !!c.result?.corrected_text &&
    (c.result.verdict === "outdated" || c.result.verdict === "incorrect")
  );
}

/** Builds the answer with accepted corrections applied (offsets are UTF-16, like JS strings). */
export function correctedText(text: string, claims: ClaimState[], applied: string[]): string {
  const fixes = claims
    .filter((c) => applied.includes(c.id) && isFixable(c))
    .sort((a, b) => b.start - a.start);
  let out = text;
  for (const c of fixes) out = out.slice(0, c.start) + c.result!.corrected_text + out.slice(c.end);
  return out;
}
