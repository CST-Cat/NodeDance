import { locale, t } from "../i18n";
import type { Metric, MetricsView } from "../metrics-contract";
import { cell, derived, watchState } from "../state";

import { Fragment } from "react";
import { __classes, __each, __style, useController } from "../state";
import "./VpsTelemetry.css";
type Props = {
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
      type Point = {
        at: number;
        network: [number | null, number | null];
        io: [number | null, number | null];
      };
      const samples = cell<Point[]>([]);
      let identity = "",
        last = 0;
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
      watchState(
        () => props.metrics,
        (value) => {
          if (!value) return;
          const next = [
            value.generation,
            value.metrics.uptime.value?.bootId ?? value.bootId ?? "",
          ].join(":");
          if (identity !== next) {
            identity = next;
            samples.value = [];
            last = 0;
          }
          const at = Date.parse(value.collectedAt);
          if (!Number.isFinite(at) || at <= last) return;
          last = at;
          samples.value = [
            ...samples.value,
            {
              at,
              network: pair(value.metrics.network.summary, [
                "receivedBytesPerSecond",
                "sentBytesPerSecond",
              ]),
              io: pair(value.metrics.host?.diskIO, [
                "readBytesPerSecond",
                "writeBytesPerSecond",
              ]),
            },
          ].slice(-60);
        },
        {
          immediate: true,
        },
      );
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
        return one === null || !count ? null : (one / (count * 1.5)) * 100;
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
          detail:
            loadPercent.value === null
              ? "—"
              : loadPercent.value < 30
                ? t("overview.loadLow")
                : loadPercent.value < 70
                  ? t("overview.loadNormal")
                  : loadPercent.value < 80
                    ? t("overview.loadMedium")
                    : t("overview.loadHigh"),
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
      const values = derived(() => samples.value.map((p) => p[mode.value]));
      const max = derived(() =>
        Math.max(
          1,
          ...values.value.flatMap((p) =>
            p.filter((n): n is number => n !== null),
          ),
        ),
      );
      const fresh = derived(() =>
        usable(
          mode.value === "network"
            ? props.metrics?.metrics.network.summary
            : props.metrics?.metrics.host?.diskIO,
          (props.metrics?.metrics.host?.fastIntervalSeconds ?? 5) * 3000,
        ),
      );
      const latest = derived(() =>
        fresh.value ? (values.value.at(-1) ?? [null, null]) : [null, null],
      );
      const labels = derived(() =>
        mode.value === "network"
          ? [t("overview.download"), t("overview.upload")]
          : [t("overview.read"), t("overview.write")],
      );
      function point(index: number, value: number) {
        const first = samples.value[0]?.at ?? 0,
          last = samples.value.at(-1)?.at ?? 0;
        return [
          last > first
            ? 8 + ((samples.value[index]!.at - first) / (last - first)) * 484
            : 8,
          88 - (value / max.value) * 76,
        ];
      }
      function path(series: number) {
        let output = "",
          active = false;
        values.value.forEach((p, index) => {
          const value = p[series];
          if (value == null) {
            active = false;
            return;
          }
          const [x, y] = point(index, value);
          output += `${active ? "L" : "M"}${x!.toFixed(1)},${y!.toFixed(1)} `;
          active = true;
        });
        return output;
      }
      function clock(at?: number) {
        return at
          ? new Date(at).toLocaleTimeString(locale.value, {
              hour12: false,
              hour: "2-digit",
              minute: "2-digit",
              second: "2-digit",
            })
          : "—";
      }
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
                  <div
                    key={ring.key as any}
                    data-resource={ring.key as any}
                    className={__classes("resource-reading")}
                    data-v-85963ddb=""
                  >
                    <div
                      role="img"
                      aria-label={
                        `${ring.label} ${ring.value === null ? t("overview.noData") : ring.value.toFixed(1) + "%"}` as any
                      }
                      className={__classes("ring")}
                      data-v-85963ddb=""
                    >
                      <svg
                        viewBox="0 0 80 80"
                        aria-hidden="true"
                        data-v-85963ddb=""
                      >
                        <circle
                          cx="40"
                          cy="40"
                          r="34"
                          className={__classes("ring-track")}
                          data-v-85963ddb=""
                        ></circle>
                        {ring.value !== null ? (
                          <circle
                            cx="40"
                            cy="40"
                            r="34"
                            pathLength="100"
                            stroke-dasharray={
                              `${Math.min(100, ring.value)} 100` as any
                            }
                            className={__classes("ring-value", {
                              "is-high":
                                ring.value >= (ring.key === "load" ? 80 : 90),
                            })}
                            data-v-85963ddb=""
                          ></circle>
                        ) : null}
                      </svg>
                      <strong data-v-85963ddb="">
                        {ring.value === null
                          ? "—"
                          : ring.value.toFixed(1) + "%"}
                      </strong>
                    </div>
                    {ring.key === "load" ? (
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
                    )}
                    <small data-v-85963ddb="">{ring.detail}</small>
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
                  </div>
                ))}
              </Fragment>
            </div>
            <div className={__classes("live-monitor")} data-v-85963ddb="">
              <header data-v-85963ddb="">
                <div
                  aria-label={t("overview.monitor") as any}
                  className={__classes("monitor-tabs")}
                  data-v-85963ddb=""
                >
                  <button
                    type="button"
                    aria-pressed={mode.value === ("network" as any)}
                    onClick={(event: any) => {
                      mode.value = "network";
                    }}
                    data-v-85963ddb=""
                  >
                    {t("overview.network")}
                  </button>
                  <button
                    type="button"
                    aria-pressed={mode.value === ("io" as any)}
                    onClick={(event: any) => {
                      mode.value = "io";
                    }}
                    data-v-85963ddb=""
                  >
                    {t("overview.io")}
                  </button>
                </div>
                {!__props.online ? (
                  <span
                    className={__classes("monitor-state")}
                    data-v-85963ddb=""
                  >
                    {t("overview.offline")}
                  </span>
                ) : !fresh.value &&
                  values.value.some((p) => p.some((v) => v !== null)) ? (
                  <span
                    className={__classes("monitor-state")}
                    data-v-85963ddb=""
                  >
                    {t("overview.stale")}
                  </span>
                ) : !values.value.some((p) => p.some((v) => v !== null)) ? (
                  <span
                    className={__classes("monitor-state")}
                    data-v-85963ddb=""
                  >
                    {t("overview.waitSamples")}
                  </span>
                ) : null}
              </header>
              <div className={__classes("monitor-values")} data-v-85963ddb="">
                <Fragment>
                  {__each(labels.value, (label, index) => (
                    <span
                      key={label as any}
                      data-series={index as any}
                      data-v-85963ddb=""
                    >
                      {label}{" "}
                      <strong data-v-85963ddb="">
                        {latest.value[index] == null
                          ? "—"
                          : bytes(latest.value[index]) + "/s"}
                      </strong>
                    </span>
                  ))}
                </Fragment>
              </div>
              <svg
                viewBox="0 0 500 100"
                preserveAspectRatio="none"
                role="img"
                aria-label={
                  `${t("overview.monitor")} · ${labels.value.join(" / ")}` as any
                }
                data-points={samples.value.length as any}
                className={__classes("rate-chart")}
                data-v-85963ddb=""
              >
                <title data-v-85963ddb="">{t("overview.liveOnly")}</title>
                <path
                  d="M8 12H492 M8 50H492 M8 88H492"
                  className={__classes("chart-grid")}
                  data-v-85963ddb=""
                ></path>
                <Fragment>
                  {__each([0, 1], (index) => (
                    <path
                      key={index as any}
                      d={path(index) as any}
                      data-series={index as any}
                      className={__classes("chart-line")}
                      data-v-85963ddb=""
                    ></path>
                  ))}
                </Fragment>
                {samples.value.length === 1 ? (
                  <Fragment>
                    <Fragment>
                      {__each([0, 1], (index) => (
                        <circle
                          key={index as any}
                          cx={8 as any}
                          cy={
                            latest.value[index] == null
                              ? 88
                              : (point(0, latest.value[index]!)[1] as any)
                          }
                          r="2.5"
                          data-series={index as any}
                          className={__classes("chart-dot")}
                          style={__style({
                            display:
                              latest.value[index] != null ? undefined : "none",
                          })}
                          data-v-85963ddb=""
                        ></circle>
                      ))}
                    </Fragment>
                  </Fragment>
                ) : null}
              </svg>
              <footer data-v-85963ddb="">
                <span data-v-85963ddb="">{clock(samples.value[0]?.at)}</span>
                <span data-v-85963ddb="">{t("overview.liveOnly")}</span>
                <span data-v-85963ddb="">
                  {clock(samples.value.at(-1)?.at)}
                </span>
              </footer>
            </div>
          </div>
        </>
      );
    },
    input,
    ["metrics", "online", "currentTime"],
    [],
    {},
    true,
  );
}
