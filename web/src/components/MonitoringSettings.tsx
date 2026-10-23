import { useEffect, useState } from "react";
import { operationRequest } from "../api";
import { display, locale, t } from "../i18n";
import { Alert, Button, Select } from "./ManagementUI";
import { SettingsGroup } from "./SettingsGroup";

type Policy = { retentionDays: number; choices: number[] };

export default function MonitoringSettings() {
  const [policy, setPolicy] = useState<Policy>();
  const [days, setDays] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    void operationRequest<Policy>("monitoring.settings", {}, { signal: controller.signal }).then(result => {
      setPolicy(result);
      setDays(String(result.retentionDays));
    }).catch(reason => { if (!controller.signal.aborted) setError(String(reason)); });
    return () => controller.abort();
  }, []);
  async function save() {
    setBusy(true);
    setError("");
    try {
      await operationRequest("monitoring.configure", {}, { body: JSON.stringify({ retentionDays: Number(days) }) });
      setPolicy(current => current && { ...current, retentionDays: Number(days) });
    } catch (reason) { setError(String(reason)); }
    finally { setBusy(false); }
  }
  return <section className="workspace-section settings-section" aria-labelledby="monitoring-settings-heading" data-testid="monitoring-settings">
    <div className="workspace-section-heading"><h2 id="monitoring-settings-heading">{t("monitoring.heading")}</h2></div>
    <form className="profile-form" onSubmit={event => { event.preventDefault(); void save(); }}>
      {error ? <Alert role="alert" tone="danger" title={t("management.error")}>{display(error)}</Alert> : null}
      <SettingsGroup><div className="parameter-row"><span>{t("monitoring.retention")}</span><Select aria-label={t("monitoring.retention")} value={days} onValueChange={setDays} disabled={!policy || busy} options={(policy?.choices ?? []).map(value => ({ value: String(value), label: t("monitoring.days", { "0": value }) }))} /></div></SettingsGroup>
      <div className="form-actions"><Button type="submit" disabled={!policy || busy || Number(days) === policy.retentionDays}>{busy ? (t("monitoring.saving")) : (t("monitoring.save"))}</Button></div>
    </form>
  </section>;
}
