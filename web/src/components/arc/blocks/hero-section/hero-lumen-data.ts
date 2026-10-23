import type { LineChartDatum } from "../../line-chart/line-chart";

/** Sample data for Lumen, a revenue analytics product. Replace it with your own product's numbers. */

export type LumenRange = "30d" | "90d" | "12m";

export const lumenRanges: { value: LumenRange; label: string }[] = [
  { value: "30d", label: "30D" },
  { value: "90d", label: "90D" },
  { value: "12m", label: "12M" },
];

/** A smooth, deterministic walk: a trend, one slow wave, and a little noise that repeats for the same seed. */
function walk(count: number, start: number, end: number, wave: number, seed: number) {
  let s = seed;
  const rand = () => { s = (s * 16807) % 2147483647; return s / 2147483647 - .5; };
  return Array.from({ length: count }, (_, i) => {
    const t = i / (count - 1);
    return Math.round(start + (end - start) * (t * t * .35 + t * .65) + Math.sin(t * Math.PI * 2.3) * wave + rand() * wave * .6);
  });
}

const months = ["Oct", "Nov", "Dec", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep"];

function series(range: LumenRange): LineChartDatum[] {
  if (range === "12m") {
    const now = walk(12, 9_800, 36_400, 2_600, 7), before = walk(12, 6_200, 21_900, 2_200, 19);
    return months.map((month, i) => ({ key: `m${i}`, label: `${month} ${i < 3 ? 2025 : 2026}`, axisLabel: i % 2 === 0 ? month : undefined, values: { mrr: now[i], last: before[i] } }));
  }
  const days = range === "30d" ? 30 : 13;
  const step = range === "30d" ? 1 : 7;
  const now = walk(days, range === "30d" ? 640 : 4_300, range === "30d" ? 1_960 : 9_800, range === "30d" ? 260 : 900, range === "30d" ? 3 : 11);
  const before = walk(days, range === "30d" ? 520 : 3_600, range === "30d" ? 1_180 : 6_100, range === "30d" ? 220 : 700, 29);
  const end = new Date(Date.UTC(2026, 8, 24));
  return now.map((value, i) => {
    const date = new Date(end.getTime() - (days - 1 - i) * step * 86_400_000);
    const label = date.toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
    const every = range === "30d" ? 7 : 3;
    return { key: date.toISOString().slice(0, 10), label, axisLabel: (days - 1 - i) % every === 0 ? label : undefined, values: { mrr: value, last: before[i] } };
  });
}

export const lumenSeries: Record<LumenRange, LineChartDatum[]> = { "30d": series("30d"), "90d": series("90d"), "12m": series("12m") };

export interface LumenKpi {
  id: string;
  label: string;
  tone: "accent" | "success" | "warning" | "danger";
  byRange: Record<LumenRange, { value: string; change: string; data: number[] }>;
}

export const lumenKpis: LumenKpi[] = [
  { id: "mrr", label: "MRR", tone: "accent", byRange: {
    "30d": { value: "$482.9K", change: "+8.2%", data: walk(12, 446, 483, 4, 5) },
    "90d": { value: "$482.9K", change: "+19.9%", data: walk(12, 402, 483, 6, 9) },
    "12m": { value: "$482.9K", change: "+51.7%", data: walk(12, 318, 483, 8, 13) },
  } },
  { id: "nrr", label: "Net retention", tone: "success", byRange: {
    "30d": { value: "118%", change: "+3 pts", data: walk(12, 112, 118, 1.5, 17) },
    "90d": { value: "116%", change: "+5 pts", data: walk(12, 109, 116, 1.8, 21) },
    "12m": { value: "114%", change: "+9 pts", data: walk(12, 103, 114, 2.4, 23) },
  } },
  { id: "new", label: "New customers", tone: "success", byRange: {
    "30d": { value: "214", change: "+12%", data: walk(12, 5, 9, 2, 31) },
    "90d": { value: "602", change: "+18%", data: walk(12, 38, 56, 6, 37) },
    "12m": { value: "2,140", change: "+34%", data: walk(12, 120, 214, 16, 41) },
  } },
  { id: "churn", label: "Churned MRR", tone: "success", byRange: {
    "30d": { value: "$6.1K", change: "−14%", data: walk(12, 9, 6, 1.2, 43) },
    "90d": { value: "$21.4K", change: "−9%", data: walk(12, 26, 21, 2, 47) },
    "12m": { value: "$96.8K", change: "−22%", data: walk(12, 12, 7, 1.4, 53) },
  } },
];

export interface LumenMover {
  name: string;
  logo: string;
  /** Mono marks draw in the text color through a mask; color marks keep their own colors. */
  mono?: boolean;
  change: string;
  amount: number;
}

export const lumenMovers: LumenMover[] = [
  { name: "Linear", logo: "/block-logos/linear-color.svg", change: "Moved to Enterprise", amount: 4_200 },
  { name: "Raycast", logo: "/block-logos/raycast-color.svg", change: "Added 120 seats", amount: 2_850 },
  { name: "Vercel", logo: "/block-logos/vercel.svg", mono: true, change: "Annual prepay", amount: 1_900 },
  { name: "Loom", logo: "/block-logos/loom-color.svg", change: "Removed 40 seats", amount: -1_240 },
  { name: "Supabase", logo: "/block-logos/supabase-color.svg", change: "Added forecasting", amount: 980 },
  { name: "Framer", logo: "/block-logos/framer.svg", mono: true, change: "Upgraded to Scale", amount: 760 },
];

export const lumenInsight: Record<LumenRange, { lead: string; rest: string }> = {
  "30d": { lead: "MRR grew $36.4K this month.", rest: "62% came from 14 expansions on the Scale plan, led by Linear." },
  "90d": { lead: "MRR grew $80.6K this quarter.", rest: "Expansion outpaced new business for the first time since March." },
  "12m": { lead: "MRR grew $164.5K in twelve months.", rest: "Net retention above 110% did more than new logos did." },
};
