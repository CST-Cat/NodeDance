import { useSignals } from "@preact/signals-react/runtime";
import { locale, t } from "../i18n";
import { formatBytes } from "../metric-format";
import type { recentHistory } from "../recent-history";
import TelemetryChart from "./TelemetryChart";
import TelemetryReadings from "./TelemetryReadings";

type Props = { kind: "network" | "io"; history: ReturnType<typeof recentHistory>; values: (number | null)[]; showTotals?: boolean };
export default function RateHistoryChart({kind, history, values, showTotals = false}: Props) {
  useSignals();
  const labels = kind === "network" ? [t("overview.download"), t("overview.upload")] : [t("overview.read"), t("overview.write")];
  const keys = kind === "network" ? ["network.received_bytes_per_second", "network.sent_bytes_per_second"] : ["disk.read_bytes_per_second", "disk.write_bytes_per_second"];
  const totals = showTotals ? history.totals(keys) : [];
  const totalLabels = kind === "network" ? [t("overview.totalDownload"), t("overview.totalUpload")] : [t("overview.totalRead"), t("overview.totalWrite")];
  const clock = (at: number) => new Date(at).toLocaleTimeString(locale.value, {hour12: false, hour: "2-digit", minute: "2-digit"});
  return <div className="rate-history" data-history-kind={kind}>
    <TelemetryReadings labels={kind === "io" ? ["R", "W"] : labels} values={values.map(value => value === null ? "—" : formatBytes(value) + "/s")} />
    <TelemetryChart label={t(kind === "network" ? "overview.network" : "overview.io")} labels={labels} points={history.points(keys)} description={t("overview.historyResolution")} emptyText={t(history.failed.value ? "overview.historyError" : history.loading.value ? "overview.historyLoading" : "overview.noHistory")} />
    <footer className="history-range">
      <span>{clock(history.range.value.from)}</span><span>{t("overview.recent4h")}</span><span>{clock(history.range.value.to)}</span>
    </footer>
    {showTotals ? <div className="history-totals">
      {totalLabels.map((text, index) => <p key={text} className="network-pair" data-v-64f045e3="">
        <span data-v-64f045e3="">{text}</span><strong data-v-64f045e3="">{totals[index] === null ? "—" : formatBytes(totals[index])}</strong>
      </p>)}
    </div> : null}
  </div>;
}
