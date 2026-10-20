import { t } from "../i18n";
import "./UiTaskProgress.css";

export default function UiTaskProgress({ status, phase, completed, total, message, error, children }: {
  status: string; phase?: string; completed?: number; total?: number;
  message?: string; error?: string; children?: import("react").ReactNode;
}) {
  const known = ["queued", "running", "succeeded", "failed", "timed_out", "canceled", "unknown", "rolled_back", "rollback_failed", "journal_failed"].includes(status);
  const measurable = status === "running" && Number.isFinite(completed) && Number.isFinite(total) && total! > 0 && completed! >= 0;
  return <div className="ui-task-progress" data-status={status} role="status">
    <div className="ui-task-summary"><strong>{known ? t(`task.state.${status}`) : status}</strong>{phase ? <span className="ui-task-phase">{phase}</span> : null}{measurable ? <span>{completed} / {total}</span> : null}</div>
    {measurable ? <progress value={Math.min(completed!, total!)} max={total} aria-label={t("task.progress")} /> : null}
    {message ? <div className="ui-task-result">{message}</div> : null}
    {error ? <div className="ui-task-error" role="alert">{error}</div> : null}
    {children}
  </div>;
}
