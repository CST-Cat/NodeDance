import UiSegments from "./UiSegments";
import { recentHistory } from "../recent-history";
import { t } from "../i18n";
import type { Metric, MetricsView } from "../metrics-contract";
import { cell, derived } from "../state";

import { Fragment } from "react";
import { __classes, __each, useController } from "../state";
import "./VpsTelemetry.css";
import ResourceReading from "./ResourceReading";
import RateHistoryChart from "./RateHistoryChart";
import { loadPercentage, loadLevel } from "../resource-readings";
type Props = {
  nodeId: string;
  metrics?: MetricsView;
  online: boolean;
  currentTime: number;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function VpsTelemetry(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const mode = cell<"network" | "io">("network");
      const loadOpen = cell(false);
      const history = recentHistory(() => props.nodeId);
      function number(value: unknown) {
        return typeof value === "number" && Number.isFinite(value) && value >= 0
          ? value
          : null;
      }
      function pair(
        metric: Metric<any> | undefined,
        keys: [string, string],
      ): [number | null, number | null] {
        return usable(metric) && metric?.value
          ? [number(metric.value[keys[0]]), number(metric.value[keys[1]])]
          : [null, null];
      }
      const mount = derived(
        () =>
          props.metrics?.metrics.disk.mounts.find(
            (m) => m.mountpoint === "/",
          ) ?? props.metrics?.metrics.disk.mounts[0],
      );
      function usable(
        metric: Metric<any> | undefined,
        ttl = (props.metrics?.metrics.host?.fastIntervalSeconds ?? 5) * 3000,
      ) {
        const received = Date.parse(props.metrics?.receivedAt ?? "");
        const age =
          (metric?.sampleAgeMillis ?? 0) +
          (Number.isFinite(received)
            ? Math.max(0, props.currentTime - received)
            : 0);
        return (
          props.online &&
          metric?.status === "known" &&
          metric.value != null &&
          age <= ttl
        );
      }
      function bytes(n: number | undefined | null) {
        if (n == null || !Number.isFinite(n)) return "—";
        const units = ["B", "KiB", "MiB", "GiB", "TiB"];
        let i = 0;
        while (n >= 1024 && i < 4) {
          n /= 1024;
          i++;
        }
        return `${n.toFixed(i ? 1 : 0)} ${units[i]}`;
      }
      const loadPercent = derived(() => {
        const load = props.metrics?.metrics.host?.load,
          cores = props.metrics?.metrics.cpu.logicalCores;
        if (!usable(load) || !usable(cores)) return null;
        const one = number(load?.value?.one),
          count = number(cores?.value);
        return loadPercentage(one, count);
      });
      const loadAverages = derived(() => {
        const load = props.metrics?.metrics.host?.load;
        return ["one", "five", "fifteen"].map((key) =>
          usable(load)
            ? number(load?.value?.[key as "one" | "five" | "fifteen"])
            : null,
        );
      });
      const rings = derived(() => [
        {
          key: "load",
          label: t("overview.load"),
          value: loadPercent.value,
          detail: loadPercent.value === null ? "—" : t(loadLevel(loadPercent.value)),
        },
        {
          key: "cpu",
          label: "CPU",
          value: usable(props.metrics?.metrics.cpu.usagePercent)
            ? number(props.metrics?.metrics.cpu.usagePercent.value)
            : null,
          detail: props.metrics?.metrics.cpu.logicalCores.value
            ? `${props.metrics.metrics.cpu.logicalCores.value} ${t("overview.cores")}`
            : "—",
        },
        {
          key: "memory",
          label: t("overview.memory"),
          value: usable(props.metrics?.metrics.memory)
            ? number(props.metrics?.metrics.memory.value?.usedPercent)
            : null,
          detail: `${bytes(props.metrics?.metrics.memory.value?.usedBytes)} / ${bytes(props.metrics?.metrics.memory.value?.totalBytes)}`,
        },
        {
          key: "disk",
          label: t("overview.disk"),
          value: usable(
            mount.value?.usage,
            (props.metrics?.metrics.host?.diskIntervalSeconds ?? 30) * 3000,
          )
            ? number(mount.value?.usage.value?.usedPercent)
            : null,
          detail: `${bytes(mount.value?.usage.value?.usedBytes)} / ${bytes(mount.value?.usage.value?.totalBytes)}`,
        },
      ]);
      const fresh = derived(() =>
        usable(
          mode.value === "network"
            ? props.metrics?.metrics.network.summary
            : props.metrics?.metrics.host?.diskIO,
          (props.metrics?.metrics.host?.fastIntervalSeconds ?? 5) * 3000,
        ),
      );
      const latest = derived(() =>
        fresh.value ? (mode.value === "network" ? pair(props.metrics?.metrics.network.summary, ["receivedBytesPerSecond", "sentBytesPerSecond"]) : pair(props.metrics?.metrics.host?.diskIO, ["readBytesPerSecond", "writeBytesPerSecond"])) : [null, null],
      );
      return () => (
        <>
          <div
            data-state={fresh.value ? "live" : ("offline" as any)}
            className={__classes("vps-telemetry")}
            data-v-85963ddb=""
          >
            <div
              aria-label={t("overview.resources") as any}
              className={__classes("resource-rings")}
              data-v-85963ddb=""
            >
              <Fragment>
                {__each(rings.value, (ring) => (
                  <ResourceReading key={ring.key} resource={ring.key} label={ring.label} value={ring.value} detail={ring.detail} highAt={ring.key === "load" ? 80 : 90}
                    labelContent={ring.key === "load" ? (
                      <button
                        type="button"
                        aria-expanded={loadOpen.value as any}
                        aria-label={t("overview.loadDetails") as any}
                        className={__classes("load-trigger")}
                        onClick={(event: any) => {
                          loadOpen.value = !loadOpen.value;
                        }}
                        onKeyDown={(event: any) => {
                          event.stopPropagation();
                          event.preventDefault();
                          loadOpen.value = false;
                        }}
                        data-v-85963ddb=""
                      >
                        {ring.label}
                      </button>
                    ) : (
                      <span data-v-85963ddb="">{ring.label}</span>
                    )}>
                    {ring.key === "load" && loadOpen.value ? (
                      <div
                        role="region"
                        aria-label={t("overview.loadDetails") as any}
                        className={__classes("load-details")}
                        onKeyDown={(event: any) => {
                          event.stopPropagation();
                          event.preventDefault();
                          loadOpen.value = false;
                        }}
                        data-v-85963ddb=""
                      >
                        <dl data-v-85963ddb="">
                          <Fragment>
                            {__each(loadAverages.value, (value, index) => (
                              <Fragment key={index as any}>
                                <dt data-v-85963ddb="">
                                  {t("overview.loadAverage", {
                                    "0": [1, 5, 15][index],
                                  })}
                                </dt>
                                <dd data-v-85963ddb="">
                                  {value === null ? "—" : value.toFixed(2)}
                                </dd>
                              </Fragment>
                            ))}
                          </Fragment>
                        </dl>
                        <p data-v-85963ddb="">{t("overview.loadFormula")}</p>
                        <button
                          type="button"
                          onClick={(event: any) => {
                            loadOpen.value = false;
                          }}
                          data-v-85963ddb=""
                        >
                          {t("overview.close")}
                        </button>
                      </div>
                    ) : null}
                  </ResourceReading>
                ))}
              </Fragment>
            </div>
            <div className={__classes("live-monitor")} data-v-85963ddb="">
              <header data-v-85963ddb="">
                <UiSegments value={mode.value} onChange={value => { mode.value = value; }}
                  label={t("overview.monitor")} className="monitor-tabs"
                  options={[{ value: "network", label: t("overview.network") }, { value: "io", label: t("overview.io") }]} />
                {!__props.online ? (
                  <span
                    className={__classes("monitor-state")}
                    data-v-85963ddb=""
                  >
                    {t("overview.offline")}
                  </span>
                ) : !fresh.value ? (
                  <span className="monitor-state" data-v-85963ddb="">{t("overview.stale")}</span>
                ) : history.failed.value || history.loading.value ? (
                  <span className="monitor-state" data-v-85963ddb="">{t(history.failed.value ? "overview.historyError" : "overview.historyLoading")}</span>
                ) : null}
              </header>
              <RateHistoryChart kind={mode.value} history={history} values={latest.value} />
            </div>
          </div>
        </>
      );
    },
    input,
    ["nodeId", "metrics", "online", "currentTime"],
    [],
    {},
    true,
  );
}
