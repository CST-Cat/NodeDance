import { t } from "../i18n";
import "./UiLoadState.css";

export default function UiLoadState({ loading, ready, failed = false }: { loading: boolean; ready: boolean; failed?: boolean }) {
  if (!loading || failed) return null;
  return <div className="ui-load-state" data-phase={ready ? "refresh" : "initial"} role="status">
    <span>{t(ready ? "load.refreshing" : "load.initial")}</span>
    {!ready ? <div className="ui-load-placeholder" aria-hidden="true"><i /><i /><i /></div> : null}
  </div>;
}
