import { t } from "../i18n";
import "./ResourceGauge.css";

type Props = { label: string; value: number | null; highAt?: number; valueTestId?: string };
export default function ResourceGauge({label, value, highAt = 90, valueTestId}: Props) {
  const valid = value !== null && Number.isFinite(value) && value >= 0;
  const percent = valid ? Math.min(100, value) : 0;
  const text = valid ? value.toFixed(1) + "%" : "—";
  return <div className="resource-gauge ring" role="img" aria-label={`${label} ${valid ? text : t("overview.noData")}`} data-state={valid ? "known" : "unknown"}>
    <svg viewBox="0 0 80 80" aria-hidden="true">
      <circle cx="40" cy="40" r="34" className="ring-track" />
      {valid ? <circle cx="40" cy="40" r="34" pathLength="100" strokeDasharray={`${percent} 100`} className={`ring-value${value >= highAt ? " is-high" : ""}`} /> : null}
    </svg>
    <strong data-testid={valueTestId}>{text}</strong>
  </div>;
}
