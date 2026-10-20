import type { ReactNode } from "react";
import type { CopyState } from "../clipboard";
import { t } from "../i18n";
import UiIcon from "./UiIcon";
import "./UiCopyFeedback.css";

export default function UiCopyFeedback({ state, children }: { state: CopyState; children: ReactNode }) {
  return <span className="ui-copy-feedback" data-copy-state={state}>
    <span className="ui-copy-original" aria-hidden={state === "success" || state === "error" ? true : undefined}>{children}</span>
    {state === "success" || state === "error" ? <span className="ui-copy-result" aria-hidden="true"><UiIcon name={state === "success" ? "check" : "close"} /></span> : null}
    <span className="ui-copy-announcement" role="status">{state === "success" ? t("copy.success") : state === "error" ? t("copy.failed") : ""}</span>
  </span>;
}
