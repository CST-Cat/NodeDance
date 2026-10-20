import { diffLines } from "diff";

export type ConfigDiffLine = {
  kind: "same" | "remove" | "add";
  oldNumber: number | null; newNumber: number | null;
  raw: string;
};

// Diffing is presentation only; it never normalizes or changes saved content.
// Bound the preview work independently of Core's existing config validation.
export function configurationDiff(before: string, after: string): ConfigDiffLine[] | null {
  if (before.length + after.length > 256_000) return null;
  const changes = diffLines(before, after, { timeout: 80, maxEditLength: 4000 });
  if (!changes) return null;
  const lines: ConfigDiffLine[] = [];
  let oldNumber = 1, newNumber = 1;
  for (const change of changes) {
    for (const raw of change.value.match(/[^\n]*\n|[^\n]+$/g) ?? []) {
      lines.push({
        kind: change.added ? "add" : change.removed ? "remove" : "same",
        oldNumber: change.added ? null : oldNumber++,
        newNumber: change.removed ? null : newNumber++, raw,
      });
      if (lines.length > 2000) return null;
    }
  }
  return lines;
}

export function splitConfigDiff(lines: ConfigDiffLine[]) {
  const rows: { before?: ConfigDiffLine; after?: ConfigDiffLine }[] = [];
  let removed: ConfigDiffLine[] = [], added: ConfigDiffLine[] = [];
  function flush() {
    for (let i = 0; i < Math.max(removed.length, added.length); i++) rows.push({ before: removed[i], after: added[i] });
    removed = []; added = [];
  }
  for (const line of lines) {
    if (line.kind === "same") { flush(); rows.push({ before: line, after: line }); }
    else (line.kind === "remove" ? removed : added).push(line);
  }
  flush();
  return rows;
}
