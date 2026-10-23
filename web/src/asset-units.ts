const gigabyte = 1_000_000_000n;

export function trafficGigabytes(value: unknown): string {
  if (typeof value === "string") return value;
  const bytes = BigInt((value ?? 0) as number | bigint);
  const magnitude = bytes < 0n ? -bytes : bytes;
  const fraction = (magnitude % gigabyte).toString().padStart(9, "0").replace(/0+$/, "");
  return `${bytes < 0n ? "-" : ""}${magnitude / gigabyte}${fraction ? "." + fraction : ""}`;
}

export function trafficBytes(value: string): bigint | string {
  const input = value.trim();
  if (!input) return 0n;
  const match = /^([+-]?)(\d*)(?:\.(\d*))?(?:e([+-]?\d+))?$/i.exec(input);
  if (!match || !(match[2] || match[3])) return value;
  const fraction = match[3] ?? "";
  const digits = (match[2]! + fraction).replace(/^0+/, "");
  if (!digits) return 0n;
  const shift = 9 + Number(match[4] ?? 0) - fraction.length;
  if (shift < -digits.length) return 0n;
  if (digits.length + shift > 100) return value;
  const sign = match[1] === "-" ? -1n : 1n;
  if (shift >= 0) return sign * BigInt(digits) * 10n ** BigInt(shift);
  const split = digits.length + shift;
  const round = digits[split]! > "5" || digits[split] === "5" && (sign > 0n || /[1-9]/.test(digits.slice(split + 1)));
  return sign * (BigInt(digits.slice(0, split) || "0") + (round ? 1n : 0n));
}
