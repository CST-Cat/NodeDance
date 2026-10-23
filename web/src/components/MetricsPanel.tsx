import { recentHistory } from "../recent-history";
import { locale, t } from "../i18n";
import { operationRequest } from "../api";
import DiagnosticCards, { type DiagnosticDefinition } from "./DiagnosticCards";
import { formatBytes } from "../metric-format";
import type { Metric, MetricStatus, MetricsView } from "../metrics-contract";
import { afterMount, beforeDispose, cell, derived, watchState } from "../state";

import { Fragment } from "react";
import { __classes, __each, useController } from "../state";
import "./MetricsPanel.css";
import RateHistoryChart from "./RateHistoryChart";
import ResourceReading from "./ResourceReading";
import TelemetryReadings from "./TelemetryReadings";
import { loadPercentage, loadLevel } from "../resource-readings";
import TelemetryChart, { type TelemetryPoint } from "./TelemetryChart";
type Props = {
  view: MetricsView;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function MetricsPanel(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const rateHistory = recentHistory(() => props.view.nodeId);
      const diagnosticDefinitions=cell<Record<string,DiagnosticDefinition>>({});
      const entityDefinitions=cell<Record<string,Record<string,DiagnosticDefinition>>>({});
      let definitionsRequested=false,disposed=false;
      beforeDispose(()=>{disposed=true});
      watchState(()=>!!props.view.metrics.host?.observability,available=>{if(!available||definitionsRequested)return;definitionsRequested=true;void operationRequest<{definitions:Record<string,DiagnosticDefinition>;entityDefinitions:Record<string,Record<string,DiagnosticDefinition>>}>('metrics.catalog').then(result=>{if(!disposed){diagnosticDefinitions.value=result.definitions??{};entityDefinitions.value=result.entityDefinitions??{}}}).catch(()=>{definitionsRequested=false})},{immediate:true});


      const trafficAsset = cell<{trafficLimitBytes: number | bigint; trafficAccounting: string; trafficWarningPercent: number} | null>(null);
      let assetEpoch = 0;
      watchState(() => props.view.nodeId, nodeId => {
        const epoch = ++assetEpoch;
        trafficAsset.value = null;
        void operationRequest<{trafficLimitBytes: number | bigint; trafficAccounting: string; trafficWarningPercent: number}>("nodes.asset", {params: {nodeId}}).then(asset => {
          if (!disposed && epoch === assetEpoch) trafficAsset.value = asset;
        }).catch(() => {});
      }, {immediate: true});
      const FAST_TTL_MS = derived(
        () => (props.view.metrics.host?.fastIntervalSeconds ?? 5) * 3_000,
      );
      const DISK_TTL_MS = derived(
        () => (props.view.metrics.host?.diskIntervalSeconds ?? 30) * 3_000,
      );
      const elapsedSinceView = cell(0);
      let viewStartedAt = 0;
      let ticker: ReturnType<typeof setInterval> | undefined;
      watchState(
        () => props.view,
        () => {
          viewStartedAt = performance.now();
          elapsedSinceView.value = 0;
        },
        {
          immediate: true,
        },
      );
      afterMount(() => {
        ticker = setInterval(() => {
          elapsedSinceView.value = Math.max(
            0,
            performance.now() - viewStartedAt,
          );
        }, 250);
      });
      beforeDispose(() => {
        if (ticker !== undefined) clearInterval(ticker);
      });
      const leaseRemainingMs = derived(() => {
        if (props.view.nodeStatus !== "online") return 0;
        const serverTimeMs = Date.parse(props.view.serverTime);
        const leaseUntilMs = Date.parse(props.view.leaseValidUntil);
        if (!Number.isFinite(serverTimeMs) || !Number.isFinite(leaseUntilMs))
          return 0;
        return Math.max(0, leaseUntilMs - serverTimeMs);
      });
      const nodeOnline = derived(
        () =>
          props.view.nodeStatus === "online" &&
          elapsedSinceView.value < leaseRemainingMs.value,
      );
      const leaseReason = derived(() =>
        props.view.nodeStatus === "online" ? "lease_expired" : "lease_inactive",
      );
      function metricState<T>(
        metric: Metric<T>,
        ttlMs = FAST_TTL_MS.value,
      ): MetricStatus {
        if (metric.status !== "known" && metric.status !== "stale")
          return metric.status;
        if (
          !nodeOnline.value ||
          metric.sampleAgeMillis + elapsedSinceView.value > ttlMs
        )
          return "stale";
        return metric.status;
      }
      const loadPercent = derived(() => loadPercentage(
        reading(props.view.metrics.host?.load, props.view.metrics.host?.load.value?.one),
        reading(props.view.metrics.cpu.logicalCores, props.view.metrics.cpu.logicalCores.value),
      ));
      const chartSamples = cell<{at: number; processes: (number | null)[]}[]>([]);
      let chartIdentity = "", lastSample = -Infinity;
      function reading<T>(metric: Metric<T> | undefined, value: unknown): number | null {
        return metric && metricState(metric) === "known" && typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : null;
      }
      function percentage<T>(metric: Metric<T>, value: unknown, ttl = FAST_TTL_MS.value): number | null {
        return metricState(metric, ttl) === "known" && typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : null;
      }
      const trafficUsed = derived(() => {
        const traffic = props.view.metrics.host?.traffic;
        const data = traffic?.value;
        if (!traffic || !data || metricState(traffic) !== "known") return null;
        const rx = Number(data.periodReceivedBytes), tx = Number(data.periodSentBytes);
        switch (trafficAsset.value?.trafficAccounting) {
          case "up": return tx;
          case "down": return rx;
          case "max": return Math.max(rx, tx);
          case "min": return Math.min(rx, tx);
          default: return rx + tx;
        }
      });
      const trafficPercent = derived(() => {
        const limit = trafficAsset.value?.trafficLimitBytes ?? 0;
        return limit > 0 && trafficUsed.value !== null ? trafficUsed.value / Number(limit) * 100 : null;
      });

      watchState(() => props.view, view => {
        const identity = [view.nodeId, view.generation, view.metrics.uptime.value?.bootId ?? view.bootId ?? ""].join(":");
        if (identity !== chartIdentity) { chartIdentity = identity; chartSamples.value = []; lastSample = -Infinity; }
        const at = Date.parse(view.collectedAt);
        if (!Number.isFinite(at) || at <= lastSample) return;
        lastSample = at;
        const host = view.metrics.host;
        chartSamples.value = [...chartSamples.value, {
          at,
          processes: [reading(host?.processes, host?.processes.value), reading(host?.sockets, host?.sockets.value?.tcp), reading(host?.sockets, host?.sockets.value?.udp)],
        }].slice(-60);
      }, {immediate: true});
      function chartPoints(kind: "processes"): TelemetryPoint[] {
        return chartSamples.value.map(sample => ({at: sample.at, values: sample[kind]}));
      }
      function metricReason<T>(
        metric: Metric<T>,
        ttlMs = FAST_TTL_MS.value,
      ): string {
        const state = metricState(metric, ttlMs);
        if (state !== "stale") return metric.reason ?? "";
        if (!nodeOnline.value)
          return appendReason(metric.reason, leaseReason.value);
        if (metric.sampleAgeMillis + elapsedSinceView.value > ttlMs)
          return appendReason(metric.reason, "sample_expired");
        return metric.reason ?? "stale";
      }
      function diskState(): MetricStatus {
        const status = props.view.metrics.disk.status;
        if (status !== "known" && status !== "stale") return status;
        if (
          !nodeOnline.value ||
          props.view.metrics.disk.sampleAgeMillis + elapsedSinceView.value >
            DISK_TTL_MS.value
        )
          return "stale";
        return status;
      }
      function diskReason(): string {
        if (diskState() !== "stale")
          return props.view.metrics.disk.reason ?? "";
        if (!nodeOnline.value)
          return appendReason(
            props.view.metrics.disk.reason,
            leaseReason.value,
          );
        if (
          props.view.metrics.disk.sampleAgeMillis + elapsedSinceView.value >
          DISK_TTL_MS.value
        )
          return appendReason(props.view.metrics.disk.reason, "sample_expired");
        return props.view.metrics.disk.reason ?? "stale";
      }
      function appendReason(
        existing: string | undefined,
        added: string,
      ): string {
        if (!existing) return added;
        if (existing.split("; ").includes(added)) return existing;
        return `${existing}; ${added}`;
      }
      function statusLabel(status: MetricStatus): string {
        return {
          known: t("m.296de0e31f8c"),
          unknown: t("m.4d8c1c5b4283"),
          error: t("m.6aa27f14bff1"),
          stale: t("m.2fe0e3339ac4"),
        }[status];
      }
      const hardwareRows = derived(() => {
        const host = props.view.metrics.host;
        if (!host) return [];
        const rows: {id: string; label: string; text: string}[] = [
          {id: "cpu", label: "CPU", text: `${host.cpu.value?.model ?? "—"} · ${host.cpu.value?.physicalCores ?? "—"} ${t("host.physicalCores")}`},
          {id: "system", label: t("m.caea742a78f8"), text: `${props.view.metrics.system.os.value ?? "—"} · ${props.view.metrics.system.architecture.value ?? "—"}`},
          {id: "kernel", label: t("m.44aadacb01fe"), text: props.view.metrics.system.kernelVersion.value ?? "—"},
          {id: "uptime", label: t("m.5986e9d976e0"), text: formatUptime(props.view.metrics.uptime.value?.seconds)},
        ];
        if (host.virtualization.value) rows.push({id: "virtualization", label: t("host.virtualization"), text: host.virtualization.value});
        const publicAddresses = new Set<string>();
        for (const [id, label, metric] of [["ipv4", t("host.publicIpv4"), host.publicIpv4], ["ipv6", t("host.publicIpv6"), host.publicIpv6]] as const) {
          if (!metric.value?.trim()) continue;
          publicAddresses.add(metric.value);
          const state = metricState(metric, 900000);
          rows.push({id, label, text: metric.value + (state === "known" ? "" : ` · ${statusLabel(state)}`)});
        }
        const interfaces = new Map<string, Set<string>>();
        for (const address of host.addresses.value ?? []) {
          if (/^(lo$|docker\d*$|br-|veth|virbr)/i.test(address.interface) || publicAddresses.has(address.address)) continue;
          const values = interfaces.get(address.interface) ?? new Set<string>();
          values.add(address.address);
          interfaces.set(address.interface, values);
        }
        for (const [name, addresses] of interfaces) rows.push({id: `interface:${name}`, label: name, text: [...addresses].join(" · ")});
        return rows;
      });
      function formatTime(value: string | undefined, showZone = true): string {
        if (!value) return "—";
        const parsed = new Date(value);
        if (!Number.isFinite(parsed.getTime())) return "—";
        return `${parsed.toLocaleString(locale.value, {
          timeZone: "UTC",
          hour12: false,
        })}${showZone ? " UTC" : ""}`;
      }
      function formatUptime(seconds: number | null | undefined): string {
        if (seconds == null || !Number.isFinite(seconds)) return "—";
        const days = Math.floor(seconds / 86_400);
        const hours = Math.floor((seconds % 86_400) / 3_600);
        const minutes = Math.floor((seconds % 3_600) / 60);
        return days > 0
          ? t("m.2a9d387d65dd", {
              "0": days,
              "1": hours,
            })
          : t("m.7077e2ea25bd", {
              "0": hours,
              "1": minutes,
            });
      }
      const clockOffsetText = derived(() => {
        const offset = props.view.clockOffsetMs;
        if (!Number.isFinite(offset)) return t("m.4d8c1c5b4283");
        return t("m.8d59cb484bc3", {
          "0": offset > 0 ? "+" : "",
          "1": Math.round(offset),
        });
      });
      return () => (
        <>
          <section
            aria-labelledby="metrics-heading"
            data-node-status={nodeOnline.value ? "online" : ("offline" as any)}
            className={__classes("metrics-panel")}
            data-v-64f045e3=""
          >
            <header className={__classes("metrics-header")} data-v-64f045e3="">
              <div data-v-64f045e3="">
                <h2 id="metrics-heading" data-v-64f045e3="">
                  {t("m.c7d6c36fa0e6")}
                </h2>
                <p className={__classes("metrics-subtitle")} data-v-64f045e3="">
                  {__props.view.metrics.system.hostname.value ??
                    __props.view.nodeId}
                  {" · "}
                  {__props.view.metrics.system.platform.value ??
                    __props.view.metrics.system.os.value ??
                    t("m.c6c03f12a7c7")}
                </p>
              </div>
            </header>
            <div className="metric-card resource-summary" data-testid="resource-summary" data-v-64f045e3="">
              {__props.view.metrics.host ? (<section data-summary-item="load"
                  className="summary-item"
                  data-v-64f045e3=""
                >
                  <div className={__classes("card-heading")} data-v-64f045e3=""><h3 data-v-64f045e3="">{t("host.load")}</h3></div>
                  <ResourceReading labelContent={false} label={t("overview.load")} resource="load" value={loadPercent.value} highAt={80} detail={loadPercent.value === null ? "—" : t(loadLevel(loadPercent.value))} />
                  <p className={__classes("metric-reason")} data-v-64f045e3="">
                    {metricReason(__props.view.metrics.host.load)}
                  </p>
                </section>) : null}
              <section data-summary-item="cpu"
                data-testid="cpu-card"
                className="summary-item"
                data-v-64f045e3=""
              >
                <div className={__classes("card-heading")} data-v-64f045e3=""><h3 data-v-64f045e3="">{"CPU"}</h3></div>
                <ResourceReading labelContent={false} label="CPU" resource="cpu" value={percentage(__props.view.metrics.cpu.usagePercent, __props.view.metrics.cpu.usagePercent.value)} valueTestId="cpu-value" detail={`${__props.view.metrics.cpu.logicalCores.value ?? "—"} ${t("overview.cores")}`} />
                {metricReason(__props.view.metrics.cpu.usagePercent) ? (
                  <p
                    data-testid="cpu-reason"
                    className={__classes("metric-reason")}
                    data-v-64f045e3=""
                  >
                    {metricReason(__props.view.metrics.cpu.usagePercent)}
                  </p>
                ) : null}
              </section>
<section data-summary-item="memory"
                data-testid="memory-card"
                className="summary-item"
                data-v-64f045e3=""
              >
                <div className={__classes("card-heading")} data-v-64f045e3=""><h3 data-v-64f045e3="">{t("m.7d8f8c37ec78")}</h3></div>
                <ResourceReading labelContent={false} label={t("overview.memory")} resource="memory" value={percentage(__props.view.metrics.memory, __props.view.metrics.memory.value?.usedPercent)} valueTestId="memory-value" detail={`${formatBytes(__props.view.metrics.memory.value?.usedBytes)} / ${formatBytes(__props.view.metrics.memory.value?.totalBytes)}`} />
                {metricReason(__props.view.metrics.memory) ? (
                  <p
                    data-testid="memory-reason"
                    className={__classes("metric-reason")}
                    data-v-64f045e3=""
                  >
                    {metricReason(__props.view.metrics.memory)}
                  </p>
                ) : null}
              </section>
<section data-summary-item="disk"
                data-testid="disk-card"
                className="summary-item"
                data-v-64f045e3=""
              >
                <div className={__classes("card-heading")} data-v-64f045e3=""><h3 data-v-64f045e3="">{t("m.de7b72a3f852")}</h3></div>
                {diskReason() ? (
                  <p className={__classes("metric-reason")} data-v-64f045e3="">
                    {diskReason()}
                  </p>
                ) : null}
                {__props.view.metrics.disk.detailsTruncated ? (
                  <p
                    data-testid="disk-truncation"
                    className={__classes("metric-reason")}
                    data-v-64f045e3=""
                  >
                    {t("m.7172e22a2c28")}
                    {__props.view.metrics.disk.truncationReason}
                  </p>
                ) : null}
                {__props.view.metrics.disk.mounts.length === 0 ? (
                  <ResourceReading labelContent={false} label={t("overview.disk")} resource="disk" value={null} detail={t("overview.noData")} />
                ) : null}
                <Fragment>
                  {__each(__props.view.metrics.disk.mounts, (mount) => (
                    <div
                      key={`${mount.device}:${mount.mountpoint}` as any}
                      className={__classes("metric-mount-reading")}
                      data-v-64f045e3=""
                    >
                      <ResourceReading labelContent={false} label={__props.view.metrics.disk.mounts.length === 1 ? t("overview.disk") : `${t("overview.disk")} ${mount.mountpoint}`} resource="disk" value={percentage(mount.usage, mount.usage.value?.usedPercent, DISK_TTL_MS.value)} detail={`${formatBytes(mount.usage.value?.usedBytes)} / ${formatBytes(mount.usage.value?.totalBytes)}`} />
                      {metricReason(mount.usage, DISK_TTL_MS.value) ? (
                        <p
                          className={__classes("metric-reason")}
                          data-v-64f045e3=""
                        >
                          {metricReason(mount.usage, DISK_TTL_MS.value)}
                        </p>
                      ) : null}
                    </div>
                  ))}
                </Fragment>
              </section>
              {__props.view.metrics.host ? (<Fragment><section data-summary-item="swap"
                  className="summary-item"
                  data-v-64f045e3=""
                >
                  <div className={__classes("card-heading")} data-v-64f045e3=""><h3 data-v-64f045e3="">{"Swap"}</h3></div>
                  <ResourceReading labelContent={false} label="Swap" resource="swap" value={percentage(__props.view.metrics.host.swap, __props.view.metrics.host.swap.value?.usedPercent)} detail={`${formatBytes(__props.view.metrics.host.swap.value?.usedBytes)} / ${formatBytes(__props.view.metrics.host.swap.value?.totalBytes)}`} />
                  <p className={__classes("metric-reason")} data-v-64f045e3="">
                    {metricReason(__props.view.metrics.host.swap)}
                  </p>
                </section>
<section data-summary-item="traffic"
                  className="summary-item"
                  data-v-64f045e3=""
                >
                  <div className={__classes("card-heading")} data-v-64f045e3="">
                    <h3 data-v-64f045e3="">{t("host.traffic")}</h3>
                  </div>
                  {(trafficAsset.value?.trafficLimitBytes ?? 0) > 0 ? <ResourceReading labelContent={false} label={t("host.traffic")} resource="traffic" value={trafficPercent.value} highAt={trafficAsset.value?.trafficWarningPercent ?? 90} detail={`${formatBytes(trafficUsed.value)} / ${formatBytes(trafficAsset.value?.trafficLimitBytes)}`} /> : null}
                  {__props.view.metrics.host.traffic.reason !== "partial_coverage" && metricReason(__props.view.metrics.host.traffic) ? (
                    <p className={__classes("metric-reason")} data-v-64f045e3="">
                      {metricReason(__props.view.metrics.host.traffic)}
                    </p>
                  ) : null}
                </section></Fragment>) : null}
            </div>
            <div className="metrics-grid host-metrics" data-v-64f045e3="">
              <article
                data-testid="network-card"
                className={__classes("metric-card network-card")}
                data-v-64f045e3=""
              >
                <div className={__classes("card-heading")} data-v-64f045e3="">
                  <h3 data-v-64f045e3="">{t("m.97b31b5d63f5")}</h3>
                </div>
                <RateHistoryChart showTotals kind="network" history={rateHistory} values={[reading(__props.view.metrics.network.summary, __props.view.metrics.network.summary.value?.receivedBytesPerSecond), reading(__props.view.metrics.network.summary, __props.view.metrics.network.summary.value?.sentBytesPerSecond)]} />
                {metricReason(__props.view.metrics.network.summary) ? (
                  <p
                    data-testid="network-reason"
                    className={__classes("metric-reason")}
                    data-v-64f045e3=""
                  >
                    {metricReason(__props.view.metrics.network.summary)}
                  </p>
                ) : null}
              </article>
              <article data-testid="io-card" className="metric-card" data-v-64f045e3="">
                <div className="card-heading" data-v-64f045e3=""><h3 data-v-64f045e3="">{t("overview.io")}</h3></div>
                <RateHistoryChart showTotals kind="io" history={rateHistory} values={[reading(__props.view.metrics.host?.diskIO, __props.view.metrics.host?.diskIO?.value?.readBytesPerSecond), reading(__props.view.metrics.host?.diskIO, __props.view.metrics.host?.diskIO?.value?.writeBytesPerSecond)]} />
                {__props.view.metrics.host?.diskIO && metricReason(__props.view.metrics.host.diskIO) ? <p className="metric-reason" data-v-64f045e3="">{metricReason(__props.view.metrics.host.diskIO)}</p> : null}
              </article>
              {__props.view.metrics.host ? (<Fragment>
                <article
                  className={__classes("metric-card")}
                  data-v-64f045e3=""
                >
                  <div className={__classes("card-heading")} data-v-64f045e3="">
                    <h3 data-v-64f045e3="">{t("host.processes")}</h3>
                  </div>
                  <TelemetryReadings labels={[t("metric.processCount"), "TCP", "UDP"]} values={[String(__props.view.metrics.host.processes.value ?? "—"), String(__props.view.metrics.host.sockets.value?.tcp ?? "—"), String(__props.view.metrics.host.sockets.value?.udp ?? "—")]} />
                  <TelemetryChart label={t("host.processes")} labels={[t("metric.processCount"), "TCP", "UDP"]} points={chartPoints("processes")} />
                  <p className={__classes("metric-reason")} data-v-64f045e3="">
                    {metricReason(__props.view.metrics.host.processes)}
                  </p>
                </article>
                <DiagnosticCards collectorDetails={<Fragment><p className="network-pair" data-v-64f045e3="">
                <span data-v-64f045e3="">{t("host.collectedAt")}</span>
                <strong data-v-64f045e3="">
                  {formatTime(__props.view.collectedAt)}
                </strong>
              </p>
<p className="network-pair" data-v-64f045e3="">
                <span data-v-64f045e3="">{t("m.91a166714259")}</span>
                <strong data-v-64f045e3="">
                  {formatTime(__props.view.receivedAt)}
                </strong>
              </p>
<p className="network-pair" data-v-64f045e3="">
                <span data-v-64f045e3="">{t("m.96ded394f11b")}</span>
                <strong data-testid="clock-offset" data-v-64f045e3="">
                  {clockOffsetText.value}
                </strong>
              </p>
<p className="network-pair" data-v-64f045e3="">
                <span data-v-64f045e3="">{t("m.f43eb988fa4a")}</span>
                <strong
                  data-testid="sample-identity"
                  data-generation={__props.view.generation as any}
                  data-active-generation={__props.view.activeGeneration as any}
                  data-sequence={__props.view.sequence as any}
                  data-v-64f045e3=""
                >
                  {__props.view.generation}
                  {" / "}
                  {__props.view.sequence}
                </strong>
              </p></Fragment>} view={props.view} definitions={diagnosticDefinitions.value} entityDefinitions={entityDefinitions.value} online={nodeOnline.value} elapsedMs={elapsedSinceView.value} />
                <article
                  className={__classes("metric-card hardware-card")}
                  data-v-64f045e3=""
                >
                  <div className="card-heading" data-v-64f045e3=""><h3 data-v-64f045e3="">{t("host.hardware")}</h3></div>
                  <dl className="hardware-details">
                    {hardwareRows.value.map(row => <div key={row.id} data-hardware={row.id}>
                      <dt>{row.label}</dt><dd>{row.text}</dd>
                    </div>)}
                  </dl>
                </article>
              </Fragment>) : null}
            </div>

          </section>
        </>
      );
    },
    input,
    ["view"],
    [],
    {},
    true,
  );
}
