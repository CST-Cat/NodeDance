export function loadPercentage(one: number | null, cores: number | null): number | null {
  return one !== null && Number.isFinite(one) && one >= 0 && cores !== null && Number.isFinite(cores) && cores > 0 ? one / (cores * 1.5) * 100 : null;
}
export function loadLevel(value: number | null): string {
  return value === null ? "overview.noData" : value < 30 ? "overview.loadLow" : value < 70 ? "overview.loadNormal" : value < 80 ? "overview.loadMedium" : "overview.loadHigh";
}
