import { operationRequest, type MetricHistory } from "./api";
import { beforeDispose, cell, derived, watchState } from "./state";
import type { TelemetryPoint } from "./components/TelemetryChart";

const STEP = 5_000;
const WINDOW = 4 * 60 * 60_000;
let active = 0;
const waiting: (() => void)[] = [];
async function acquire() {
  if (active >= 4) await new Promise<void>(resolve => waiting.push(resolve));
  else active++;
  return () => { const next = waiting.shift(); if (next) next(); else active--; };
}

function cachedSeries<T>(build: (keys: string[]) => T) {
  const values = new Map<string, {readonly value: T}>();
  return (keys: string[]): T => {
    const key = JSON.stringify(keys);
    let result = values.get(key);
    if (!result) {
      const selected = [...keys];
      result = derived(() => build(selected));
      values.set(key, result);
    }
    return result.value;
  };
}

// Core owns persistence. Keep only the currently displayed four-hour response;
// never reconstruct history from browser sessions or bridge missing buckets.
export function recentHistory(nodeId: () => string) {
  const history = cell<MetricHistory | null>(null);
  const range = cell({from: Date.now() - WINDOW, to: Date.now()});
  const loading = cell(false), failed = cell(false);
  let epoch = 0, disposed = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let request: AbortController | undefined;
  watchState(nodeId, id => {
    const current = ++epoch;
    clearTimeout(timer); request?.abort();
    history.value = null; failed.value = false;
    let lastSuccessfulTo = 0;
    async function refresh() {
      if (!id || disposed || current !== epoch) return;
      loading.value = true;
      const controller = new AbortController(); request = controller;
      const release = await acquire();
      try {
        if (controller.signal.aborted) return;
        const to = Date.now(), from = to - WINDOW;
        range.value = {from, to};
        const queryFrom = Math.max(from, lastSuccessfulTo ? Math.floor((lastSuccessfulTo - STEP) / STEP) * STEP : from);
        const result = await operationRequest<MetricHistory>("nodes.history", {
          params: {nodeId: id}, query: {resolution: "5s", from: new Date(queryFrom).toISOString(), to: new Date(to).toISOString()},
        }, {signal: controller.signal}, 20_000);
        if (!disposed && current === epoch) {
          const old = history.value;
          const keys = new Set([...(old?.series.map(series => series.key) ?? []), ...result.series.map(series => series.key)]);
          history.value = {...result, from: new Date(from).toISOString(), to: new Date(to).toISOString(), series: [...keys].map(key => ({key, points: [
            ...(old?.series.find(series => series.key === key)?.points ?? []).filter(point => Date.parse(point.bucketAt) >= from && Date.parse(point.bucketAt) < queryFrom),
            ...(result.series.find(series => series.key === key)?.points ?? []),
          ]}))};
          lastSuccessfulTo = to;
          failed.value = false;
        }
      } catch {
        if (!disposed && current === epoch && !controller.signal.aborted) failed.value = true;
      } finally {
        release();
        if (!disposed && current === epoch) { loading.value = false; timer = setTimeout(refresh, STEP); }
      }
    }
    void refresh();
  }, {immediate: true});
  beforeDispose(() => { disposed = true; epoch++; clearTimeout(timer); request?.abort(); });
  const points = cachedSeries((keys): TelemetryPoint[] => {
    const {from, to} = range.value;
    const series = keys.map(key => new Map((history.value?.series.find(s => s.key === key)?.points ?? []).map(p => [Date.parse(p.bucketAt), p])));
    const result: TelemetryPoint[] = [{at: from, values: keys.map(() => null)}];
    for (let at = Math.ceil(from / STEP) * STEP; at <= to; at += STEP) {
      result.push({at, values: series.map(values => {
        const point = values.get(at);
        return point && point.samples > 0 && Number.isFinite(point.value) && point.value >= 0 ? point.value : null;
      })});
    }
    if (result.at(-1)?.at !== to) result.push({at: to, values: keys.map(() => null)});
    return result;
  });
  const totals = cachedSeries((keys): (number | null)[] => {
    const {from, to} = range.value;
    return keys.map(key => {
      let total = 0, observed = false;
      for (const point of history.value?.series.find(series => series.key === key)?.points ?? []) {
        const at = Date.parse(point.bucketAt);
        const seconds = Math.max(0, Math.min(to, at + STEP) - Math.max(from, at)) / 1000;
        if (!Number.isFinite(seconds) || seconds <= 0 || !(point.samples > 0) || !Number.isFinite(point.value) || point.value < 0) continue;
        total += point.value * seconds;
        observed = true;
      }
      return observed ? total : null;
    });
  });
  return {history, range, loading, failed, points, totals};
}
