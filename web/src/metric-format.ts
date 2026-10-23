export function formatBytes(value: number | bigint | null | undefined): string {
  if (value == null) return "—";
  const numeric = Number(value);
  if (!Number.isFinite(numeric) || numeric < 0) return "—";
  if (numeric < 1024) return `${Math.round(numeric)} B`;
  const units = ["KiB", "MiB", "GiB", "TiB", "PiB", "EiB"];
  let amount = numeric;
  let unit = "B";
  for (const next of units) {
    amount /= 1024;
    unit = next;
    if (amount < 1024) break;
  }
  return `${amount.toFixed(1)} ${unit}`;
}
