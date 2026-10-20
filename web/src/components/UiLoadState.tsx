import { t } from "../i18n";
import "./UiLoadState.css";

// A single presentation for first reads and background refreshes. Existing
// data remains owned by the caller and is never removed by this component.
export default function UiLoadState({ loading, ready, failed = false }: { loading: boolean; ready: boolean; failed?: boolean }) {
  if (!loading || failed) return null;
  return <div className="ui-load-state" data-phase={ready ? "refresh" : "initial"} role="status">
    <span>{t(ready ? "load.refreshing" : "load.initial")}</span>
    {!ready ? <div className="ui-load-placeholder" aria-hidden="true"><i /><i /><i /></div> : null}
  </div>;
}
