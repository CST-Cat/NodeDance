import type { EChartsOption } from "echarts";
import { LineChart } from "echarts/charts";
import { GridComponent, TooltipComponent } from "echarts/components";
import * as echarts from "echarts/core";
import { CanvasRenderer } from "echarts/renderers";
import type { MetricHistory } from "../api";
import { display, locale, t } from "../i18n";
import { operationRequest } from "../api";
import type { DiagnosticDefinition } from "./DiagnosticCards";
import {
  afterMount,
  beforeDispose,
  cell,
  derived,
  uniqueId,
  watchState,
} from "../state";

import { Fragment } from "react";
import { __classes, __each, __style, useController } from "../state";
import "./HistoricalMetrics.css";
import UiSelect from "./UiSelect";
type Props = {
  history: MetricHistory | null;
  loading: boolean;
  error: string;
  nodeOnline: boolean;
  availableOnly?: boolean;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function HistoricalMetrics(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      echarts.use([LineChart, GridComponent, TooltipComponent, CanvasRenderer]);
      const props = __props;
 const definitions=cell<Record<string,DiagnosticDefinition>>({});let disposed=false;
      const headingId = uniqueId();
      const host = cell<HTMLElement>();
      const selectedKey = cell("cpu.usage_percent");
      const chartError = cell("");
      let chart: echarts.ECharts | undefined;
      let observer: ResizeObserver | undefined;
      let themeObserver: MutationObserver | undefined;
      const availableKeys = derived(() => {
        const labels = new Map<string, string>([
          ["cpu.usage_percent", t("m.1c82e5547854")],
          ["memory.used_percent", t("m.ace212ad8fd3")],
          ["memory.used_bytes", t("m.688cab237917")],
          ["network.received_bytes_per_second", t("m.9cf3b70addc3")],
          ["network.sent_bytes_per_second", t("m.81c8be973451")],
        ]);
        for(const [key,d] of Object.entries(definitions.value))labels.set(key,locale.value==='zh-CN'?d.nameZh:d.name);
        for (const series of props.history?.series ?? []) {
          if (!labels.has(series.key))
            labels.set(
              series.key,
              series.key === "disk.used_percent"
                ? t("guest.metric.disk.used_percent")
                : series.key,
            );
          if (series.key.startsWith("disk:")) {
            const suffix = series.key.endsWith(":used_percent")
              ? "used_percent"
              : "used_bytes";
            const mount = series.key.slice(
              "disk:".length,
              -`:${suffix}`.length,
            );
            labels.set(
              series.key,
              t("m.c47bd176f3ca", {
                "0": mount,
                "1":
                  suffix === "used_percent"
                    ? t("m.9746ae777abb")
                    : t("m.666f1c71e1e0"),
              }),
            );
          }
        }
        return [...labels.entries()]
          .filter(
            ([key]) =>
              !props.availableOnly ||
              props.history?.series.some((series) => series.key === key),
          )
          .map(([key, label]) => ({
            key,
            label,
          }));
      });
      const selectedLabel = derived(
        () =>
          availableKeys.value.find((item) => item.key === selectedKey.value)
            ?.label ?? selectedKey.value,
      );
      const sampleCount = derived(
        () =>
          props.history?.series.find(
            (series) => series.key === selectedKey.value,
          )?.points.length ?? 0,
      );
      function chartOptions(): EChartsOption {
        const style = host.value ? getComputedStyle(host.value) : null;
        const color = (name: string, fallback: string) =>
          style?.getPropertyValue(name).trim() || fallback;
        const muted = color("--muted", "#6c6a64");
        const line = color("--line", "#e6dfd8");
        const accent = color("--accent-fill", "#cc785c");
        const history = props.history;
        const metric = history?.series.find(
          (item) => item.key === selectedKey.value,
        );
        const from = history ? Date.parse(history.from) : Number.NaN;
        const to = history ? Date.parse(history.to) : Number.NaN;
        const step = history?.resolution === "hour" ? 3_600_000 : history?.resolution === "5s" ? 5_000 : 60_000;
        const first = Number.isFinite(from) ? Math.ceil(from / step) * step : 0;
        const last = Number.isFinite(to) ? Math.ceil(to / step) * step : 0;
        const values = new Map(
          (metric?.points ?? []).map((point) => [
            Date.parse(point.bucketAt),
            point.value,
          ]),
        );
        const data: Array<[number, number | null]> = [];
        for (let at = first; at < last; at += step) {
          const value = values.get(at);
          data.push([
            at,
            value !== undefined && Number.isFinite(value) ? value : null,
          ]);
        }
        const diskPercent =
          selectedKey.value.startsWith("disk:") &&
          selectedKey.value.endsWith(":used_percent");
        const definition=definitions.value[selectedKey.value];
 const percent = definition?.unit==="%" || selectedKey.value.endsWith("_percent") || diskPercent;
        const byteRate = definition?definition.unit==="bytes/s":selectedKey.value.startsWith("network.");
        const bytes = definition?.unit==="bytes" || selectedKey.value.endsWith("_bytes");
        let divisor = 1;
        let unit = percent ? "%" : definition?.unit??"";
        if (byteRate) {
          divisor = 1024 * 1024;
          unit = "MiB/s";
        } else if (bytes) {
          divisor = 1024 ** 3;
          unit = "GiB";
        }
        const normalized = data.map(
          ([at, value]) =>
            [at, value === null ? null : value / divisor] as [
              number,
              number | null,
            ],
        );
        return {
          animation: false,
          grid: {
            left: 54,
            right: 20,
            top: 24,
            bottom: 38,
          },
          tooltip: {
            backgroundColor: color("--surface", "#efe9de"),
            borderColor: line,
            textStyle: {
              color: color("--text", "#252523"),
            },
            trigger: "axis",
            valueFormatter: (value) =>
              value == null
                ? t("m.63ef182b7603")
                : `${Number(value).toFixed(2)} ${unit}`,
          },
          xAxis: {
            type: "time",
            splitNumber: 4,
            axisLabel: {
              color: muted,
              hideOverlap: true,
              formatter: (value: number) =>
                to - from > 172800000
                  ? new Date(value).toLocaleDateString(locale.value, {
                      month: "short",
                      day: "numeric",
                    })
                  : new Date(value).toLocaleTimeString(locale.value, {
                      hour: "2-digit",
                      minute: "2-digit",
                    }),
            },
            axisPointer: {
              label: {
                formatter: (params: { value: string | number | Date }) =>
                  new Date(params.value).toLocaleString(locale.value),
              },
            },
            axisLine: {
              lineStyle: {
                color: line,
              },
            },
          },
          yAxis: {
            type: "value",
            scale: !percent,
            name: unit,
            nameTextStyle: {
              color: muted,
            },
            axisLabel: {
              color: muted,
            },
            splitLine: {
              lineStyle: {
                color: line,
              },
            },
          },
          series: [
            {
              name: selectedLabel.value,
              type: "line",
              showSymbol: (metric?.points.length ?? 0) < 2,
              connectNulls: false,
              sampling: "lttb",
              data: normalized,
              lineStyle: {
                width: 2,
                color: accent,
              },
              itemStyle: {
                color: accent,
              },
              areaStyle: {
                color: color("--accent-soft", "#f1e1d7"),
              },
            },
          ],
        };
      }
      function render() {
        if (!host.value) return;
        try {
          chartError.value = "";
          chart ??= echarts.init(host.value, undefined, {
            renderer: "canvas",
          });
          chart.setOption(chartOptions(), true);
        } catch {
          chartError.value = t("m.3ed77cde9795");
        }
      }
      watchState(
        () => [props.history, selectedKey.value, locale.value],
        render,
        {
          deep: true,
          flush: "post",
        },
      );
      watchState(
        availableKeys,
        (items) => {
          if (!items.some((item) => item.key === selectedKey.value))
            selectedKey.value = items[0]?.key ?? "cpu.usage_percent";
        },
        {
          immediate: true,
        },
      );
      afterMount(() => {
 if(!props.availableOnly)void operationRequest<{definitions:Record<string,DiagnosticDefinition>}>("metrics.catalog").then(result=>{if(!disposed){definitions.value=result.definitions??{};render()}}).catch(()=>{});
        render();
        if (host.value) {
          observer = new ResizeObserver(() => chart?.resize());
          observer.observe(host.value);
          themeObserver = new MutationObserver(render);
          themeObserver.observe(document.documentElement, {
            attributes: true,
            attributeFilter: ["data-theme"],
          });
        }
      });
      beforeDispose(() => {
 disposed=true;
        observer?.disconnect();
        themeObserver?.disconnect();
        chart?.dispose();
        chart = undefined;
      });
      return () => (
        <>
          <section
            aria-labelledby={headingId as any}
            data-testid="history-panel"
            className={__classes("history-panel")}
            data-v-8fb9e739=""
          >
            <header className={__classes("history-heading")} data-v-8fb9e739="">
              <div data-v-8fb9e739="">
                <span className={__classes("eyebrow")} data-v-8fb9e739="">
                  {t("label.aacdab5b45f0")}
                </span>
                <h2 id={headingId as any} data-v-8fb9e739="">
                  {t("m.bfb41404e88b")}
                </h2>
                {__props.history ? (
                  <p data-v-8fb9e739="">
                    {new Date(__props.history.from).toLocaleString(
                      locale.value,
                    )}
                    {" — "}
                    {new Date(__props.history.to).toLocaleString(locale.value)}
                    {" · "}
                    {__props.history.resolution === "minute"
                      ? t("m.bd957bc497aa")
                      : t("m.3b6fefc50feb")}
                    {t("m.a4768faecb38")}
                  </p>
                ) : null}
              </div>
              <label
                className={__classes("history-metric-select")}
                data-v-8fb9e739=""
              >
                {t("m.b87e67c7dae0")}
                <UiSelect
                  aria-label={t("m.4a2f28a82624") as any}
                  modelValue={selectedKey.value}
                  onUpdateModelValue={(__nextValue: any) => {
                    selectedKey.value = __nextValue;
                  }}
                  modelModifiers={{}}
                  data-v-8fb9e739=""
                >
                  <Fragment>
                    {__each(availableKeys.value, (item) => (
                      <option
                        key={item.key as any}
                        value={item.key as any}
                        data-v-8fb9e739=""
                      >
                        {item.label}
                      </option>
                    ))}
                  </Fragment>
                </UiSelect>
              </label>
            </header>
            {!__props.nodeOnline ? (
              <p
                role="status"
                className={__classes("history-stale")}
                data-v-8fb9e739=""
              >
                {t("m.b116276abf68")}
              </p>
            ) : (
              <p className={__classes("history-note")} data-v-8fb9e739="">
                {t("m.7a9f26412898")}
              </p>
            )}
            {__props.loading ? (
              <div
                role="status"
                className={__classes("history-state")}
                data-v-8fb9e739=""
              >
                {t("m.a980a5f91841")}
              </div>
            ) : __props.error ? (
              <div
                role="alert"
                className={__classes("history-state history-error")}
                data-v-8fb9e739=""
              >
                {display(__props.error)}
              </div>
            ) : chartError.value ? (
              <div
                role="alert"
                className={__classes("history-state history-error")}
                data-v-8fb9e739=""
              >
                {display(chartError.value)}
              </div>
            ) : null}
            <div
              ref={__dom(host)}
              data-testid="history-chart"
              data-sample-count={sampleCount.value as any}
              aria-label={
                t("m.ea8e1f947456", {
                  "0": selectedLabel.value,
                }) as any
              }
              className={__classes("history-chart")}
              style={__style({
                display:
                  !__props.loading && !__props.error && !chartError.value
                    ? undefined
                    : "none",
              })}
              data-v-8fb9e739=""
            ></div>
            {!__props.loading && !__props.error && sampleCount.value === 0 ? (
              <p className={__classes("history-empty")} data-v-8fb9e739="">
                {t("m.f3de6936eb3b")}
              </p>
            ) : null}
          </section>
        </>
      );
    },
    input,
    ["history", "loading", "error", "nodeOnline", "availableOnly"],
    [],
    {},
    true,
  );
}
