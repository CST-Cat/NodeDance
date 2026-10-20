import { clipboardFeedback } from "../clipboard";
import { pathDisplay } from "../path-display";
import { t } from "../i18n";
import { cell, useController } from "../state";
import UiCopyFeedback from "./UiCopyFeedback";
import UiDialog from "./UiDialog";
import UiIcon from "./UiIcon";
import "./UiPath.css";

export default function UiPath(input: { value: string; copyOnly?: boolean; directory?: boolean }) {
  return useController((props: typeof input) => {
    const open = cell(false), copied = clipboardFeedback();
    return () => {
      const parts = pathDisplay(props.value, props.directory);
      const copy = <button type="button" className="ui-path-copy" aria-label={t("path.copy")} title={t("path.copy")} onClick={() => { void copied.copy(props.value); }}><UiCopyFeedback state={copied.state.value}><UiIcon name="copy" /></UiCopyFeedback></button>;
      return props.copyOnly ? copy : <span className="ui-path" title={props.value}>
        <button type="button" className="ui-path-value" aria-label={t("path.view", { "0": props.value })} onClick={() => { open.value = true; }}>
          <span className="ui-path-parent">{parts.parent}</span><span className="ui-path-name"><span className="ui-path-stem">{parts.stem}</span><span className="ui-path-extension">{parts.extension}</span></span>
        </button>
        {copy}
        {open.value ? <UiDialog open title={t("path.full")} onClose={() => { open.value = false; }}><pre className="ui-path-full">{props.value}</pre><footer data-v-b0bbf977=""><button type="button" onClick={() => { open.value = false; }}>{t("overview.close")}</button></footer></UiDialog> : null}
      </span>;
    };
  }, input, ["value", "copyOnly", "directory"], [], {}, true);
}
