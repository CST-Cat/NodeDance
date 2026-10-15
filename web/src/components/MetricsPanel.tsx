import { locale, t } from "../i18n";
import type { Metric, MetricStatus, MetricsView } from "../metrics-contract";
import { afterMount, beforeDispose, cell, derived, watchState } from "../state";

import { Fragment } from "react";
import { __classes, __each, useController } from "../state";
import "./MetricsPanel.css";
type Props = {
  view: MetricsView;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function MetricsPanel(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
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
          deep: true,
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
      const nodeStatusText = derived(() =>
        nodeOnline.value ? t("m.b9086662b1df") : t("m.be1b4f3c6c1c"),
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
      function formatBytes(value: number | null | undefined): string {
        if (value == null || !Number.isFinite(value) || value < 0) return "—";
        if (value < 1024) return `${Math.round(value)} B`;
        const units = ["KiB", "MiB", "GiB", "TiB", "PiB"];
        let amount = value;
        let unit = "B";
        for (const next of units) {
          amount /= 1024;
          unit = next;
          if (amount < 1024) break;
        }
        return `${amount.toFixed(1)} ${unit}`;
      }
      function formatRate(value: number | null | undefined): string {
        return value == null || !Number.isFinite(value) || value < 0
          ? "—"
          : `${formatBytes(value)}/s`;
      }
      function formatPercent(value: number | null | undefined): string {
        return value == null || !Number.isFinite(value)
          ? "—"
          : `${value.toFixed(1)}%`;
      }
      function formatTime(value: string | undefined): string {
        if (!value) return "—";
        const parsed = new Date(value);
        if (!Number.isFinite(parsed.getTime())) return "—";
        return `${parsed.toLocaleString(locale.value, {
          timeZone: "UTC",
          hour12: false,
        })} UTC`;
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
      const primaryInterfaces = derived(() =>
        props.view.metrics.network.interfaces.filter(
          (iface) => iface.includedInSummary,
        ),
      );
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
                <p className={__classes("metrics-eyebrow")} data-v-64f045e3="">
                  {t("m.c797a5342dcf")}
                </p>
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
              <div
                role="status"
                data-testid="node-status"
                className={__classes(
                  "node-state",
                  nodeOnline.value ? "is-online" : "is-offline",
                )}
                data-v-64f045e3=""
              >
                <span
                  aria-hidden="true"
                  className={__classes("state-dot")}
                  data-v-64f045e3=""
                ></span>
                {nodeStatusText.value}
              </div>
            </header>
            <div className={__classes("metrics-grid")} data-v-64f045e3="">
              <article
                data-testid="cpu-card"
                className={__classes("metric-card")}
                data-v-64f045e3=""
              >
                <div className={__classes("card-heading")} data-v-64f045e3="">
                  <h3 data-v-64f045e3="">{"CPU"}</h3>
                  <span
                    data-status={
                      metricState(__props.view.metrics.cpu.usagePercent) as any
                    }
                    className={__classes("status-pill")}
                    data-v-64f045e3=""
                  >
                    {statusLabel(
                      metricState(__props.view.metrics.cpu.usagePercent),
                    )}
                  </span>
                </div>
                <strong
                  data-testid="cpu-value"
                  className={__classes("primary-value")}
                  data-v-64f045e3=""
                >
                  {formatPercent(__props.view.metrics.cpu.usagePercent.value)}
                </strong>
                <p className={__classes("metric-caption")} data-v-64f045e3="">
                  {__props.view.metrics.cpu.logicalCores.value ?? "—"}
                  {t("m.4a5d806c0374")}
                </p>
                {metricReason(__props.view.metrics.cpu.usagePercent) ? (
                  <p
                    data-testid="cpu-reason"
                    className={__classes("metric-reason")}
                    data-v-64f045e3=""
                  >
                    {metricReason(__props.view.metrics.cpu.usagePercent)}
                  </p>
                ) : null}
              </article>
              <article
                data-testid="memory-card"
                className={__classes("metric-card")}
                data-v-64f045e3=""
              >
                <div className={__classes("card-heading")} data-v-64f045e3="">
                  <h3 data-v-64f045e3="">{t("m.7d8f8c37ec78")}</h3>
                  <span
                    data-status={
                      metricState(__props.view.metrics.memory) as any
                    }
                    className={__classes("status-pill")}
                    data-v-64f045e3=""
                  >
                    {statusLabel(metricState(__props.view.metrics.memory))}
                  </span>
                </div>
                <strong
                  data-testid="memory-value"
                  className={__classes("primary-value")}
                  data-v-64f045e3=""
                >
                  {formatPercent(
                    __props.view.metrics.memory.value?.usedPercent,
                  )}
                </strong>
                <p className={__classes("metric-caption")} data-v-64f045e3="">
                  {formatBytes(__props.view.metrics.memory.value?.usedBytes)}
                  {" / "}
                  {formatBytes(__props.view.metrics.memory.value?.totalBytes)}
                  {t("m.937683da32c1")}
                </p>
                {metricReason(__props.view.metrics.memory) ? (
                  <p
                    data-testid="memory-reason"
                    className={__classes("metric-reason")}
                    data-v-64f045e3=""
                  >
                    {metricReason(__props.view.metrics.memory)}
                  </p>
                ) : null}
              </article>
              <article
                data-testid="disk-card"
                className={__classes("metric-card disk-card")}
                data-v-64f045e3=""
              >
                <div className={__classes("card-heading")} data-v-64f045e3="">
                  <h3 data-v-64f045e3="">{t("m.de7b72a3f852")}</h3>
                  <span
                    data-status={diskState() as any}
                    className={__classes("status-pill")}
                    data-v-64f045e3=""
                  >
                    {statusLabel(diskState())}
                  </span>
                </div>
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
                  <p className={__classes("metric-caption")} data-v-64f045e3="">
                    {t("m.1d0afbd4d7a2")}
                  </p>
                ) : null}
                <Fragment>
                  {__each(__props.view.metrics.disk.mounts, (mount) => (
                    <div
                      key={`${mount.device}:${mount.mountpoint}` as any}
                      className={__classes("mount-row")}
                      data-v-64f045e3=""
                    >
                      <div
                        className={__classes("mount-title")}
                        data-v-64f045e3=""
                      >
                        <strong data-v-64f045e3="">{mount.mountpoint}</strong>
                        <span
                          data-status={
                            metricState(mount.usage, DISK_TTL_MS.value) as any
                          }
                          className={__classes("status-pill small")}
                          data-v-64f045e3=""
                        >
                          {statusLabel(
                            metricState(mount.usage, DISK_TTL_MS.value),
                          )}
                        </span>
                      </div>
                      <p data-v-64f045e3="">
                        {formatPercent(mount.usage.value?.usedPercent)}
                        {" · "}
                        {formatBytes(mount.usage.value?.usedBytes)}
                        {" / "}
                        {formatBytes(mount.usage.value?.totalBytes)}
                      </p>
                      <p className={__classes("mount-meta")} data-v-64f045e3="">
                        {mount.device}
                        {" · "}
                        {mount.filesystem}
                      </p>
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
              </article>
              <article
                data-testid="network-card"
                className={__classes("metric-card network-card")}
                data-v-64f045e3=""
              >
                <div className={__classes("card-heading")} data-v-64f045e3="">
                  <h3 data-v-64f045e3="">{t("m.97b31b5d63f5")}</h3>
                  <span
                    data-status={
                      metricState(__props.view.metrics.network.summary) as any
                    }
                    className={__classes("status-pill")}
                    data-v-64f045e3=""
                  >
                    {statusLabel(
                      metricState(__props.view.metrics.network.summary),
                    )}
                  </span>
                </div>
                <div className={__classes("network-pair")} data-v-64f045e3="">
                  <span data-v-64f045e3="">{t("m.6e684586884e")}</span>
                  <strong data-v-64f045e3="">
                    {formatRate(
                      __props.view.metrics.network.summary.value
                        ?.receivedBytesPerSecond,
                    )}
                  </strong>
                </div>
                <div className={__classes("network-pair")} data-v-64f045e3="">
                  <span data-v-64f045e3="">{t("m.edecf0ae6e51")}</span>
                  <strong data-v-64f045e3="">
                    {formatRate(
                      __props.view.metrics.network.summary.value
                        ?.sentBytesPerSecond,
                    )}
                  </strong>
                </div>
                {metricReason(__props.view.metrics.network.summary) ? (
                  <p
                    data-testid="network-reason"
                    className={__classes("metric-reason")}
                    data-v-64f045e3=""
                  >
                    {metricReason(__props.view.metrics.network.summary)}
                  </p>
                ) : null}
                {__props.view.metrics.network.detailsTruncated ? (
                  <p
                    data-testid="network-truncation"
                    className={__classes("metric-reason")}
                    data-v-64f045e3=""
                  >
                    {t("m.093a185c2e51")}
                    {__props.view.metrics.network.truncationReason}
                  </p>
                ) : null}
                <Fragment>
                  {__each(primaryInterfaces.value, (iface) => (
                    <div
                      key={iface.name as any}
                      data-testid="network-interface"
                      data-interface-name={iface.name as any}
                      className={__classes("interface-row")}
                      data-v-64f045e3=""
                    >
                      <div
                        className={__classes("interface-title")}
                        data-v-64f045e3=""
                      >
                        <strong data-v-64f045e3="">{iface.name}</strong>
                        <span
                          className={__classes("interface-note")}
                          data-v-64f045e3=""
                        >
                          {t("m.09f99341ca07")}
                        </span>
                        <span
                          className={__classes("interface-rates")}
                          data-v-64f045e3=""
                        >
                          <span data-v-64f045e3="">
                            {formatRate(
                              iface.rate.value?.receivedBytesPerSecond,
                            )}
                            {" ↓"}
                          </span>
                          <span data-v-64f045e3="">
                            {formatRate(iface.rate.value?.sentBytesPerSecond)}
                            {" ↑"}
                          </span>
                        </span>
                        <span
                          data-status={metricState(iface.rate) as any}
                          data-testid={
                            `interface-rate-status-${iface.name}` as any
                          }
                          className={__classes("status-pill small")}
                          data-v-64f045e3=""
                        >
                          {statusLabel(metricState(iface.rate))}
                        </span>
                      </div>
                      {metricReason(iface.rate) ? (
                        <p
                          data-testid={
                            `interface-rate-reason-${iface.name}` as any
                          }
                          className={__classes(
                            "metric-reason interface-reason",
                          )}
                          data-v-64f045e3=""
                        >
                          {metricReason(iface.rate)}
                        </p>
                      ) : null}
                    </div>
                  ))}
                </Fragment>
              </article>
            </div>
            {__props.view.metrics.host ? (
              <div
                className={__classes("metrics-grid host-metrics")}
                data-v-64f045e3=""
              >
                <article
                  className={__classes("metric-card")}
                  data-v-64f045e3=""
                >
                  <div className={__classes("card-heading")} data-v-64f045e3="">
                    <h3 data-v-64f045e3="">{"Swap"}</h3>
                    <span
                      data-status={
                        metricState(__props.view.metrics.host.swap) as any
                      }
                      className={__classes("status-pill")}
                      data-v-64f045e3=""
                    >
                      {statusLabel(metricState(__props.view.metrics.host.swap))}
                    </span>
                  </div>
                  <strong
                    className={__classes("primary-value")}
                    data-v-64f045e3=""
                  >
                    {formatPercent(
                      __props.view.metrics.host.swap.value?.usedPercent,
                    )}
                  </strong>
                  <p className={__classes("metric-caption")} data-v-64f045e3="">
                    {formatBytes(
                      __props.view.metrics.host.swap.value?.usedBytes,
                    )}
                    {" / "}
                    {formatBytes(
                      __props.view.metrics.host.swap.value?.totalBytes,
                    )}
                  </p>
                  <p className={__classes("metric-reason")} data-v-64f045e3="">
                    {metricReason(__props.view.metrics.host.swap)}
                  </p>
                </article>
                <article
                  className={__classes("metric-card")}
                  data-v-64f045e3=""
                >
                  <div className={__classes("card-heading")} data-v-64f045e3="">
                    <h3 data-v-64f045e3="">{t("host.load")}</h3>
                    <span
                      data-status={
                        metricState(__props.view.metrics.host.load) as any
                      }
                      className={__classes("status-pill")}
                      data-v-64f045e3=""
                    >
                      {statusLabel(metricState(__props.view.metrics.host.load))}
                    </span>
                  </div>
                  <strong
                    className={__classes("primary-value")}
                    data-v-64f045e3=""
                  >
                    {__props.view.metrics.host.load.value?.one.toFixed(2) ??
                      "—"}
                  </strong>
                  <p className={__classes("metric-caption")} data-v-64f045e3="">
                    {"5m: "}
                    {__props.view.metrics.host.load.value?.five.toFixed(2) ??
                      "—"}
                    {" · 15m: "}
                    {__props.view.metrics.host.load.value?.fifteen.toFixed(2) ??
                      "—"}
                  </p>
                  <p className={__classes("metric-reason")} data-v-64f045e3="">
                    {metricReason(__props.view.metrics.host.load)}
                  </p>
                </article>
                <article
                  className={__classes("metric-card")}
                  data-v-64f045e3=""
                >
                  <div className={__classes("card-heading")} data-v-64f045e3="">
                    <h3 data-v-64f045e3="">{t("host.processes")}</h3>
                    <span
                      data-status={
                        metricState(__props.view.metrics.host.processes) as any
                      }
                      className={__classes("status-pill")}
                      data-v-64f045e3=""
                    >
                      {statusLabel(
                        metricState(__props.view.metrics.host.processes),
                      )}
                    </span>
                  </div>
                  <strong
                    className={__classes("primary-value")}
                    data-v-64f045e3=""
                  >
                    {__props.view.metrics.host.processes.value ?? "—"}
                  </strong>
                  <p className={__classes("metric-caption")} data-v-64f045e3="">
                    {"TCP: "}
                    {__props.view.metrics.host.sockets.value?.tcp ?? "—"}
                    {" · UDP: "}
                    {__props.view.metrics.host.sockets.value?.udp ?? "—"}
                    {" · "}
                    {statusLabel(
                      metricState(__props.view.metrics.host.sockets),
                    )}
                  </p>
                  <p className={__classes("metric-reason")} data-v-64f045e3="">
                    {metricReason(__props.view.metrics.host.processes)}
                  </p>
                </article>
                <article
                  className={__classes("metric-card")}
                  data-v-64f045e3=""
                >
                  <div className={__classes("card-heading")} data-v-64f045e3="">
                    <h3 data-v-64f045e3="">{t("host.traffic")}</h3>
                    <span
                      data-status={
                        metricState(__props.view.metrics.host.traffic) as any
                      }
                      className={__classes("status-pill")}
                      data-v-64f045e3=""
                    >
                      {statusLabel(
                        metricState(__props.view.metrics.host.traffic),
                      )}
                    </span>
                  </div>
                  <p className={__classes("network-pair")} data-v-64f045e3="">
                    {"↓ "}
                    {formatBytes(
                      __props.view.metrics.host.traffic.value
                        ?.periodReceivedBytes,
                    )}
                    {" · ↑ "}
                    {formatBytes(
                      __props.view.metrics.host.traffic.value?.periodSentBytes,
                    )}
                  </p>
                  <p className={__classes("metric-caption")} data-v-64f045e3="">
                    {t("host.period")}
                    {": "}
                    {formatTime(
                      __props.view.metrics.host.traffic.value?.periodStart ??
                        "",
                    )}
                  </p>
                  <p className={__classes("metric-caption")} data-v-64f045e3="">
                    {t("host.total")}
                    {": ↓ "}
                    {formatBytes(
                      __props.view.metrics.host.traffic.value?.receivedBytes,
                    )}
                    {" · ↑ "}
                    {formatBytes(
                      __props.view.metrics.host.traffic.value?.sentBytes,
                    )}
                  </p>
                  {__props.view.metrics.host.traffic.reason ===
                  "partial_coverage" ? (
                    <p
                      className={__classes("metric-reason")}
                      data-v-64f045e3=""
                    >
                      {t("host.partialTraffic")}
                    </p>
                  ) : (
                    <p
                      className={__classes("metric-reason")}
                      data-v-64f045e3=""
                    >
                      {metricReason(__props.view.metrics.host.traffic)}
                    </p>
                  )}
                </article>
                <article
                  className={__classes("metric-card")}
                  data-v-64f045e3=""
                >
                  <div className={__classes("card-heading")} data-v-64f045e3="">
                    <h3 data-v-64f045e3="">{"GPU"}</h3>
                    <span
                      data-status={
                        metricState(
                          __props.view.metrics.host.gpus,
                          90000,
                        ) as any
                      }
                      className={__classes("status-pill")}
                      data-v-64f045e3=""
                    >
                      {statusLabel(
                        metricState(__props.view.metrics.host.gpus, 90000),
                      )}
                    </span>
                  </div>
                  <Fragment>
                    {__each(__props.view.metrics.host.gpus.value, (gpu) => (
                      <div
                        key={gpu.id as any}
                        className={__classes("mount-row")}
                        data-v-64f045e3=""
                      >
                        <strong data-v-64f045e3="">{gpu.name}</strong>
                        <p data-v-64f045e3="">
                          {formatPercent(gpu.utilization)}
                          {" · "}
                          {formatBytes(gpu.memoryUsedBytes)}
                          {" / "}
                          {formatBytes(gpu.memoryTotalBytes)}
                          {" · "}
                          {gpu.temperatureCelsius ?? "—"}
                          {" °C"}
                        </p>
                      </div>
                    ))}
                  </Fragment>
                  <p className={__classes("metric-reason")} data-v-64f045e3="">
                    {metricReason(__props.view.metrics.host.gpus, 90000)}
                  </p>
                </article>
                <article
                  className={__classes("metric-card")}
                  data-v-64f045e3=""
                >
                  <h3 data-v-64f045e3="">{t("host.hardware")}</h3>
                  <p data-v-64f045e3="">
                    {__props.view.metrics.host.cpu.value?.model ?? "—"}
                    {" · "}
                    {__props.view.metrics.host.cpu.value?.physicalCores ??
                      "—"}{" "}
                    {t("host.physicalCores")}
                  </p>
                  <p data-v-64f045e3="">
                    {__props.view.metrics.host.virtualization.value ?? "—"}
                  </p>
                  <p className={__classes("metric-caption")} data-v-64f045e3="">
                    {t("host.cache")}
                    {": "}
                    {formatBytes(
                      __props.view.metrics.host.memory.value?.cachedBytes,
                    )}
                    {" · "}
                    {t("host.buffers")}
                    {": "}
                    {formatBytes(
                      __props.view.metrics.host.memory.value?.bufferedBytes,
                    )}
                    {" · "}
                    {__props.view.metrics.host.memoryMode}
                  </p>
                  <Fragment>
                    {__each(
                      __props.view.metrics.host.addresses.value,
                      (address) => (
                        <p
                          key={(address.interface + address.address) as any}
                          className={__classes("metric-caption")}
                          data-v-64f045e3=""
                        >
                          {address.interface}
                          {" · "}
                          {address.address}
                        </p>
                      ),
                    )}
                  </Fragment>
                  <p className={__classes("metric-caption")} data-v-64f045e3="">
                    {"IPv4: "}
                    {__props.view.metrics.host.publicIpv4.value ?? "—"}
                    {" · "}
                    {statusLabel(
                      metricState(__props.view.metrics.host.publicIpv4, 900000),
                    )}
                  </p>
                  <p className={__classes("metric-caption")} data-v-64f045e3="">
                    {"IPv6: "}
                    {__props.view.metrics.host.publicIpv6.value ?? "—"}
                    {" · "}
                    {statusLabel(
                      metricState(__props.view.metrics.host.publicIpv6, 900000),
                    )}
                  </p>
                </article>
              </div>
            ) : null}
            <footer className={__classes("metrics-footer")} data-v-64f045e3="">
              <div data-v-64f045e3="">
                <span data-v-64f045e3="">{t("m.1ed65d95a3d5")}</span>
                <strong data-v-64f045e3="">
                  {formatTime(__props.view.collectedAt)}
                </strong>
              </div>
              <div data-v-64f045e3="">
                <span data-v-64f045e3="">{t("m.91a166714259")}</span>
                <strong data-v-64f045e3="">
                  {formatTime(__props.view.receivedAt)}
                </strong>
              </div>
              <div data-v-64f045e3="">
                <span data-v-64f045e3="">{t("m.96ded394f11b")}</span>
                <strong data-testid="clock-offset" data-v-64f045e3="">
                  {clockOffsetText.value}
                </strong>
              </div>
              <div data-v-64f045e3="">
                <span data-v-64f045e3="">{t("m.5986e9d976e0")}</span>
                <strong data-v-64f045e3="">
                  {formatUptime(__props.view.metrics.uptime.value?.seconds)}
                </strong>
              </div>
              <div data-v-64f045e3="">
                <span data-v-64f045e3="">{t("m.aeae0144dae1")}</span>
                <strong
                  data-testid="boot-id"
                  className={__classes("boot-id")}
                  data-v-64f045e3=""
                >
                  {__props.view.metrics.uptime.value?.bootId ??
                    __props.view.bootId ??
                    t("m.4d8c1c5b4283")}
                </strong>
              </div>
              <div data-v-64f045e3="">
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
              </div>
              <div data-v-64f045e3="">
                <span data-v-64f045e3="">{t("m.caea742a78f8")}</span>
                <strong data-v-64f045e3="">
                  {__props.view.metrics.system.os.value ?? t("m.4d8c1c5b4283")}
                  {" · "}
                  {__props.view.metrics.system.architecture.value ??
                    t("m.4d8c1c5b4283")}
                </strong>
              </div>
              <div data-v-64f045e3="">
                <span data-v-64f045e3="">{t("m.44aadacb01fe")}</span>
                <strong data-v-64f045e3="">
                  {__props.view.metrics.system.kernelVersion.value ??
                    t("m.4d8c1c5b4283")}
                </strong>
              </div>
            </footer>
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
