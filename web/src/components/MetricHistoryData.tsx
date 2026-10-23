import { useState } from "react";
import type { MetricHistory } from "../api";
import { locale, t } from "../i18n";
import { Button, Select } from "./ManagementUI";
import { Pagination } from "./arc/pagination/pagination";
import { SortableDataTable } from "./arc/sortable-data-table/sortable-data-table";

export function metricHistoryCSV(history: MetricHistory): string {
  const quote = (value: unknown) => `"${String(value).replaceAll('"', '""')}"`;
  const rows: unknown[][] = [["nodeId", "resolution", "metric", "bucketAt", "value", "minimum", "maximum", "samples"]];
  for (const series of history.series) for (const point of series.points)
    rows.push([history.nodeId, history.resolution, series.key, point.bucketAt, point.value, point.minimum, point.maximum, point.samples]);
  return rows.map(row => row.map(quote).join(",")).join("\r\n") + "\r\n";
}

export default function MetricHistoryData({ history }: { history: MetricHistory }) {
  const [key, setKey] = useState(history.series[0]?.key ?? "");
  const [page, setPage] = useState(0);
  const selected = history.series.find(series => series.key === key) ?? history.series[0];
  const points = selected?.points ?? [];
  const current = Math.min(page, Math.max(0, Math.ceil(points.length / 100) - 1));
  function download(format: "csv" | "json") {
    const text = format === "csv" ? metricHistoryCSV(history) : JSON.stringify(history, null, 2);
    const url = URL.createObjectURL(new Blob([text], { type: format === "csv" ? "text/csv;charset=utf-8" : "application/json" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = `monitoring-${history.nodeId}-${history.resolution}.${format}`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  return <section className="product-arc arc-ui registry-controls metric-history-data" data-native-controls="" data-testid="history-data">
    <div className="metric-history-toolbar"><h3>{t("monitoring.data")}</h3><div className="metric-history-actions"><Button variant="secondary" disabled={!history.series.length} onClick={() => download("csv")}>CSV</Button><Button variant="secondary" disabled={!history.series.length} onClick={() => download("json")}>JSON</Button></div></div>
    <Select aria-label={t("monitoring.metric")} value={selected?.key ?? ""} onValueChange={value => { setKey(value); setPage(0); }} options={history.series.map(series => ({ value: series.key, label: series.key }))} />
    <SortableDataTable rows={points.slice(current * 100, (current + 1) * 100).map(point => ({ ...point }))} rowKey="bucketAt" caption={selected?.key ?? "Monitoring data"} emptyMessage={t("monitoring.empty")} columns={[
      { key: "bucketAt", label: t("monitoring.time"), render: value => new Date(String(value)).toLocaleString(locale.value) },
      { key: "value", label: t("monitoring.average"), numeric: true },
      { key: "minimum", label: t("monitoring.minimum"), numeric: true },
      { key: "maximum", label: t("monitoring.maximum"), numeric: true },
      { key: "samples", label: t("monitoring.samples"), numeric: true },
    ]} />
    {points.length > 100 ? <Pagination page={current + 1} pageCount={Math.ceil(points.length / 100)} onPageChange={value => setPage(value - 1)} label={t("monitoring.pages")} /> : null}
  </section>;
}
