import { StateEffect, StateField, type Transaction } from "@codemirror/state";
import { invertedEffects } from "@codemirror/commands";

export const codeDocument = (source: string) => source.replace(/\r\n|\r/g, "\n");

export function editCodeSource(source: string, transaction: Transaction): string {
  const starts = [0];
  for (const match of source.matchAll(/\r\n|\r|\n/g)) starts.push(match.index! + match[0].length);
  const offset = (position: number) => {
    const line = transaction.startState.doc.lineAt(position);
    return starts[line.number - 1]! + position - line.from;
  };
  const ending = source.match(/\r\n|\r|\n/)?.[0] ?? "\n";
  const parts: string[] = [];
  let cursor = 0;
  transaction.changes.iterChanges((from, to, _fromB, _toB, inserted) => {
    const start = offset(from), end = offset(to);
    parts.push(source.slice(cursor, start), inserted.toString().replace(/\n/g, ending));
    cursor = end;
  });
  parts.push(source.slice(cursor));
  return parts.join("");
}

export const restoreCodeSource = StateEffect.define<string>();
const restoreCodeEndings = StateEffect.define<string>();
export const codeSourceField = StateField.define<string>({
  create: () => "",
  update: (source, transaction) => {
    for (const effect of transaction.effects) if (effect.is(restoreCodeSource)) return effect.value;
    for (const effect of [...transaction.effects].reverse()) if (effect.is(restoreCodeEndings)) {
      let index = 0;
      return transaction.newDoc.toString().replace(/\n/g, () => effect.value[index++] === "c" ? "\r\n" : effect.value[index - 1] === "r" ? "\r" : "\n");
    }
    return transaction.docChanged ? editCodeSource(source, transaction) : source;
  }
});
export const codeSourceHistory = invertedEffects.of(transaction => {
  if (!transaction.docChanged) return [];
  const source = transaction.startState.field(codeSourceField);
  let deletedLine = false;
  transaction.changes.iterChanges((from, to) => { if (transaction.startState.doc.sliceString(from, to).includes("\n")) deletedLine = true; });
  if (!(source.includes("\r") && deletedLine) && !transaction.effects.some(effect => effect.is(restoreCodeEndings))) return [];
  return [restoreCodeEndings.of((source.match(/\r\n|\r|\n/g) ?? []).map(ending => ending === "\r\n" ? "c" : ending === "\r" ? "r" : "n").join(""))];
});
