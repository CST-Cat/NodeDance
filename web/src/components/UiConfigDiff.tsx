import { useMemo, useState } from "react";
import { configurationDiff, splitConfigDiff, type ConfigDiffLine } from "../configuration-diff";
import { t } from "../i18n";
import UiSegments from "./UiSegments";
import "./UiConfigDiff.css";

export default function UiConfigDiff({ before, after }: { before: string; after: string }) {
  const [mode, setMode] = useState<"unified" | "split">("unified");
  const lines = useMemo(() => configurationDiff(before, after), [before, after]);
  const rows = useMemo(() => splitConfigDiff(lines ?? []), [lines]);
  function content(line?: ConfigDiffLine, marked = false) {
    return line ? <>{marked ? <span className="diff-sign" aria-hidden="true">{line.kind === "add" ? "+" : line.kind === "remove" ? "−" : " "}</span> : null}<code>{line.raw.replace(/\n$/, "")}</code>{!line.raw.endsWith("\n") ? <small className="diff-no-newline">{t("diff.noNewline")}</small> : null}</> : null;
  }
  return <div className="config-diff" data-testid="config-diff">
    <header className="config-diff-header">
      <span className="diff-counts"><span>− {lines?.filter(line => line.kind === "remove").length ?? "—"}</span><span>+ {lines?.filter(line => line.kind === "add").length ?? "—"}</span></span>
      <UiSegments label={t("diff.view")} value={mode} onChange={setMode} options={[{ value: "unified", label: t("diff.unified") }, { value: "split", label: t("diff.split") }]} />
    </header>
    {lines === null ? <p role="status">{t("diff.unavailable")}</p> : lines.length === 0 ? <p>{t("diff.empty")}</p> : <>
      <div className="diff-unified" data-selected={mode === "unified"}>
        <table aria-label={t("diff.unified")}><thead><tr><th>{t("diff.before")}</th><th>{t("diff.after")}</th><th><span className="diff-sr">{t("diff.content")}</span></th></tr></thead><tbody>
          {lines.map((line, index) => <tr key={index} data-kind={line.kind}><td className="diff-number">{line.oldNumber}</td><td className="diff-number">{line.newNumber}</td><td><span className="diff-sign" aria-hidden="true">{line.kind === "add" ? "+" : line.kind === "remove" ? "−" : " "}</span>{content(line)}</td></tr>)}
        </tbody></table>
      </div>
      <div className="diff-split" data-selected={mode === "split"}>
        <table aria-label={t("diff.split")}><thead><tr><th colSpan={2}>{t("diff.before")}</th><th colSpan={2}>{t("diff.after")}</th></tr></thead><tbody>
          {rows.map((row, index) => <tr key={index}><td className="diff-number" data-kind={row.before?.kind}>{row.before?.oldNumber}</td><td data-kind={row.before?.kind}>{content(row.before, true)}</td><td className="diff-number" data-kind={row.after?.kind}>{row.after?.newNumber}</td><td data-kind={row.after?.kind}>{content(row.after, true)}</td></tr>)}
        </tbody></table>
      </div>
    </>}
  </div>;
}
