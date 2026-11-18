import type { Label } from "../api";

interface Props {
  score: number | null;
  label?: Label;
  size?: number;
  stroke?: number;
  caption?: string;
}

/** Ring gauge; null score renders an indeterminate spinner ring. */
export function ScoreGauge({ score, label, size = 132, stroke = 10, caption }: Props) {
  const r = (size - stroke) / 2;
  const circ = 2 * Math.PI * r;
  const pct = score == null ? 0.25 : Math.max(0, Math.min(100, score)) / 100;
  return (
    <div className={`gauge gauge-${label ?? "pending"} ${score == null ? "is-spinning" : ""}`} style={{ width: size, height: size }}>
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} aria-hidden>
        <circle className="gauge-track" cx={size / 2} cy={size / 2} r={r} strokeWidth={stroke} fill="none" />
        <circle
          className="gauge-value"
          cx={size / 2}
          cy={size / 2}
          r={r}
          strokeWidth={stroke}
          fill="none"
          strokeLinecap="round"
          strokeDasharray={`${circ * pct} ${circ}`}
          transform={`rotate(-90 ${size / 2} ${size / 2})`}
        />
      </svg>
      <div className="gauge-center">
        <span className="gauge-number" style={{ fontSize: size * 0.3 }}>
          {score == null ? "…" : score}
        </span>
        {caption && <span className="gauge-caption">{caption}</span>}
      </div>
    </div>
  );
}
