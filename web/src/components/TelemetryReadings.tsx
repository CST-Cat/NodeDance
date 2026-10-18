type Props = { labels: string[]; values: string[] };
export default function TelemetryReadings({labels, values}: Props) {
  return <div className="monitor-values">{labels.map((label, index) => <span key={label} data-series={index}>{label} <strong>{values[index] ?? "—"}</strong></span>)}</div>;
}
