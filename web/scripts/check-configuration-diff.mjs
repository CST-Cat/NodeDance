import assert from "node:assert/strict";
import { configurationDiff, splitConfigDiff } from "../src/configuration-diff.ts";

function verify(before, after) {
  const lines = configurationDiff(before, after);
  assert.ok(lines);
  assert.equal(lines.filter(line => line.kind !== "add").map(line => line.raw).join(""), before);
  assert.equal(lines.filter(line => line.kind !== "remove").map(line => line.raw).join(""), after);
  const rows = splitConfigDiff(lines);
  assert.equal(rows.flatMap(row => row.before ? [row.before.raw] : []).join(""), before);
  assert.equal(rows.flatMap(row => row.after ? [row.after.raw] : []).join(""), after);
  let oldNumber = 0, newNumber = 0;
  for (const line of lines) {
    if (line.oldNumber !== null) assert.equal(line.oldNumber, ++oldNumber);
    if (line.newNumber !== null) assert.equal(line.newNumber, ++newNumber);
  }
}
for (const [before, after] of [
  ["", ""], ["", "first\nsecond"], ["first\nsecond", ""],
  ["one\ntwo\nthree\n", "one\nchanged\nthree\n"],
  ["same", "same\n"], ["a\r\nb\r\n", "a\nb\n"],
  ["\n\n", "\n"], ["<script>alert(1)</script>", "<img src=x onerror=alert(1)>"],
]) verify(before, after);
let random = 51;
for (let fixture = 0; fixture < 80; fixture++) {
  const pick = () => (random = (random * 1664525 + 1013904223) >>> 0) % 7;
  verify(Array.from({ length: 30 }, () => `${pick()}\n`).join(""), Array.from({ length: 30 }, () => `${pick()}\n`).join(""));
}
assert.equal(configurationDiff("a".repeat(256001), ""), null);
assert.equal(configurationDiff("a\n".repeat(2001), "a\n".repeat(2001)), null);
console.log("Configuration diff: exact reconstruction, line numbers, split pairing, CRLF, final newline, empty files and preview bounds passed.");
