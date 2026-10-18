import type { CSSProperties, ReactNode } from "react";
import ResourceGauge from "./ResourceGauge";
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
  collectorDetails?: ReactNode;
};
type Group = { id: string; accepts: (key: string) => boolean; entity?: string };
const prefix = (text: string) => (key: string) => key.startsWith(text);
const schedulingKeys = ["cpu.steal_percent", "cpu.iowait_percent", "cpu.user_percent", "cpu.system_percent"];
const groups: Group[] = [
  { id: "scheduling", accepts: key => schedulingKeys.includes(key) },
  { id: "cores", accepts: () => false, entity: "cpu" },
  { id: "diskIO", accepts: prefix("disk.") },
  { id: "processes", accepts: prefix("process."), entity: "process" },
  { id: "collector", accepts: prefix("agent.") },
];

// Use the very same square card, heading and parameter rows as MetricsPanel.
// Per-core readings reuse the same gauge inside the shared square card.
export default function DiagnosticCards({ view, definitions, entityDefinitions, online, elapsedMs, collectorDetails }: Props) {
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
    if (unit === "1/s") return number(n) + "/s";
    return unit && unit !== "count" ? number(n) + " " + unit : number(n);
  };
  const row = (key: string, label: string, text: string, reason?: string) => (
    <p key={key} className="network-pair" data-diagnostic={key} data-reason={reason} data-v-64f045e3="">
      <span data-v-64f045e3="">{label}</span><strong data-v-64f045e3="">{text}</strong>
    </p>
  );
  return groups.map(group => {
    const keys = Object.keys(observation.values).filter(key => group.accepts(key) && definitions[key]).sort((a, b) => group.id === "scheduling" ? schedulingKeys.indexOf(a) - schedulingKeys.indexOf(b) : a.localeCompare(b, undefined, { numeric: true }));
    const entities = observation.entities.filter(entity => entity.kind === group.entity);
    if (group.id === "cores") {
      if (view.metrics.cpu.logicalCores.value === 1 || entities.length <= 1) return null;
      const columns = Math.min(4, Math.ceil(Math.sqrt(entities.length)));
      const size = Math.max(44, 128 / Math.sqrt(entities.length));
      return <article key={group.id} className="metric-card" data-diagnostic-card={group.id} data-v-64f045e3="">
        <div className="card-heading" data-v-64f045e3=""><h3 data-v-64f045e3="">{t("diagnostics.card.cores")}</h3></div>
        <div className="core-gauges" style={{gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`, "--core-gauge-size": `${size}px`, "--core-rows": Math.ceil(entities.length / columns), "--core-mobile-rows": Math.ceil(entities.length / 2)} as CSSProperties}>
          {entities.map(entity => {
            const metric = entity.values.usage_percent;
            const current = value(metric, "%") !== "—" ? metric?.value ?? null : null;
            return <div key={entity.id} className="core-reading" data-diagnostic={`core:${entity.id}`}>
              <ResourceGauge label={entity.name} value={current} />
              <span>{entity.name}</span>
            </div>;
          })}
        </div>
      </article>;
    }
    return <article key={group.id} className="metric-card" data-diagnostic-card={group.id} data-v-64f045e3="">
      <div className="card-heading" data-v-64f045e3=""><h3 data-v-64f045e3="">{t(`diagnostics.card.${group.id}`)}</h3></div>
      {keys.map(key => {
        const d = definitions[key]!;
        return row(key, name(d), value(observation.values[key], d.unit, d.interval === "slow"), observation.values[key]?.reason);
      })}
      {entities.map(entity => {
        if (entity.kind === "process") return row("process:" + entity.id, entity.name, value(entity.values.cpu_percent, "%", true) + " · " + value(entity.values.rss_bytes, "bytes", true));
        return <div key={entity.kind + ":" + entity.id}>
          <p className="network-pair" data-v-64f045e3=""><strong data-v-64f045e3="">{entity.name}</strong></p>
          {Object.entries(entity.values).map(([key, metric]) => {
            const d = entityDefinitions[entity.kind]?.[key];
            return row(`${entity.kind}:${entity.id}:${key}`, d ? name(d) : key, value(metric, d?.unit ?? "", true), metric.reason);
          })}
        </div>;
      })}
      {group.id === "collector" ? collectorDetails : null}
      {!keys.length && !entities.length ? <p className="network-pair" data-v-64f045e3="">—</p> : null}
    </article>;
  });
}
