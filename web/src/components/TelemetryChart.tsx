import { t } from "../i18n";
import "./TelemetryChart.css";
export type TelemetryPoint = { at: number; values: (number | null)[] };
type Props = { points: TelemetryPoint[]; labels: string[]; label: string; legend?: boolean };

export default function TelemetryChart({points, labels, label}: Props) {
  const maximum = Math.max(1, ...points.flatMap(point => point.values.filter((value): value is number => value !== null && Number.isFinite(value) && value >= 0)));
  const first = points[0]?.at ?? 0, last = points.at(-1)?.at ?? 0;
  const position = (at: number, value: number) => [last > first ? 8 + (at - first) / (last - first) * 484 : 8, 88 - value / maximum * 76];
  const series = labels.map((name, index) => {
    let d = "", active = false;
    const dots: {x: number; y: number}[] = [];
    points.forEach((point, pointIndex) => {
      const value = point.values[index];
      if (value == null || !Number.isFinite(value) || value < 0) { active = false; return; }
      const [x, y] = position(point.at, value);
      d += `${active ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)} `;
      // Mark isolated samples so a single real reading never becomes a fake curve.
      if (!active && (points[pointIndex + 1]?.values[index] == null)) dots.push({x, y});
      active = true;
    });
    return {name, d, dots};
  });
  const available = series.some(item => item.d);
  return <div className="telemetry-plot" data-state={available ? "sampled" : "empty"}>
    <svg viewBox="0 0 500 100" preserveAspectRatio="none" role="img" aria-label={`${label} · ${labels.join(" / ")}`} data-points={points.length} className="rate-chart">
      <title>{t("overview.liveOnly")}</title>
      <path d="M8 12H492 M8 50H492 M8 88H492" className="chart-grid" />
      {series.map((item, index) => <g key={item.name} data-series={index}>
        <path d={item.d} data-series={index} className="chart-line"><title>{item.name}</title></path>
        {item.dots.map((dot, i) => <circle key={i} cx={dot.x} cy={dot.y} r="2.5" data-series={index} className="chart-dot" />)}
      </g>)}
    </svg>
    {!available ? <span className="telemetry-empty">{t("overview.waitSamples")}</span> : null}
  </div>;
}
