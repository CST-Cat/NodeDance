import { useSignals } from "@preact/signals-react/runtime";
import { locale, t } from "../i18n";
import type { Metric, MetricsView } from "../metrics-contract";
import { formatBytes } from "../metric-format";

export type DiagnosticDefinition = { name: string; nameZh: string; unit: string; interval: string };
type Props = {
  view: MetricsView;
  definitions: Record<string, DiagnosticDefinition>;
  entityDefinitions: Record<string, Record<string, DiagnosticDefinition>>;
  online: boolean;
  elapsedMs: number;
};
type Group = { id: string; accepts: (key: string) => boolean; entity?: string };
const prefix = (text: string) => (key: string) => key.startsWith(text);
const groups: Group[] = [
  { id: "scheduling", accepts: prefix("cpu.") },
  { id: "cores", accepts: () => false, entity: "cpu" },
  { id: "cpuPressure", accepts: prefix("pressure.cpu.") },
  { id: "memoryPressure", accepts: prefix("pressure.memory.") },
  { id: "ioPressure", accepts: prefix("pressure.io.") },
  { id: "kernelMemory", accepts: prefix("memory.") },
  { id: "swapActivity", accepts: prefix("swap.") },
  { id: "diskIO", accepts: prefix("disk.") },
  { id: "filesystems", accepts: prefix("filesystem."), entity: "filesystem" },
  { id: "tcp", accepts: key => key.startsWith("network.") && !key.includes("conntrack") },
  { id: "conntrack", accepts: key => key.includes("conntrack") },
  { id: "services", accepts: prefix("service."), entity: "service" },
  { id: "processes", accepts: prefix("process."), entity: "process" },
  { id: "containers", accepts: prefix("container."), entity: "container" },
  { id: "applications", accepts: prefix("application."), entity: "application" },
  { id: "collector", accepts: prefix("agent.") },
];

// Use the very same square card, heading and parameter rows as MetricsPanel.
// This component only groups measurements; it has no CSS or alternate layout.
export default function DiagnosticCards({ view, definitions, entityDefinitions, online, elapsedMs }: Props) {
  useSignals();
  const host = view.metrics.host;
  const observation = host?.observability;
  if (!observation) return null;
  const name = (d: DiagnosticDefinition) => locale.value === "zh-CN" ? d.nameZh : d.name;
  const number = (n: number) => new Intl.NumberFormat(locale.value, { maximumFractionDigits: 2 }).format(n);
  const value = (m: Metric<number> | undefined, unit: string, slow = false) => {
    const ttl = (slow ? observation.slowIntervalSeconds : host?.fastIntervalSeconds ?? 5) * 3000;
    if (!m || m.status !== "known" || m.value == null || !online || m.sampleAgeMillis + elapsedMs > ttl) return "—";
    const n = m.value;
    if (unit === "bytes") return formatBytes(n);
    if (unit === "bytes/s") return formatBytes(n) + "/s";
    if (unit === "%") return number(n) + "%";
    if (unit === "boolean") return t(n ? "diagnostics.yes" : "diagnostics.no");
    if (unit === "code") return number(n);
    if (unit === "cores") return number(n) + " " + t("overview.cores");
    if (unit === "1/s") return number(n) + "/s";
    return unit && unit !== "count" ? number(n) + " " + unit : number(n);
  };
  const row = (key: string, label: string, text: string, reason?: string) => (
    <p key={key} className="network-pair" data-diagnostic={key} data-reason={reason} data-v-64f045e3="">
      <span data-v-64f045e3="">{label}</span><strong data-v-64f045e3="">{text}</strong>
    </p>
  );
  return groups.map(group => {
    const keys = Object.keys(observation.values).filter(key => group.accepts(key) && definitions[key]).sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
    const entities = observation.entities.filter(entity => entity.kind === group.entity);
    return <article key={group.id} className="metric-card" data-diagnostic-card={group.id} data-v-64f045e3="">
      <div className="card-heading" data-v-64f045e3=""><h3 data-v-64f045e3="">{t(`diagnostics.card.${group.id}`)}</h3></div>
      {keys.map(key => {
        const d = definitions[key]!;
        return row(key, name(d), value(observation.values[key], d.unit, d.interval === "slow"), observation.values[key]?.reason);
      })}
      {entities.map(entity => {
        if (entity.kind === "cpu") return row("core:" + entity.id, entity.name, value(entity.values.usage_percent, "%"), entity.values.usage_percent?.reason);
        if (entity.kind === "process") return row("process:" + entity.id, entity.name, value(entity.values.cpu_percent, "%", true) + " · " + value(entity.values.rss_bytes, "bytes", true));
        return <div key={entity.kind + ":" + entity.id}>
          <p className="network-pair" data-v-64f045e3=""><strong data-v-64f045e3="">{entity.name}</strong></p>
          {Object.entries(entity.values).map(([key, metric]) => {
            const d = entityDefinitions[entity.kind]?.[key];
            return row(`${entity.kind}:${entity.id}:${key}`, d ? name(d) : key, value(metric, entity.units?.[key] ?? d?.unit ?? "", true), metric.reason);
          })}
        </div>;
      })}
      {!keys.length && !entities.length ? <p className="network-pair" data-v-64f045e3="">—</p> : null}
    </article>;
  });
}
