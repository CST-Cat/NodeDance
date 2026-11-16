export const SPRING_SNAPPY = {
  type: "spring",
  stiffness: 320,
  damping: 28,
  mass: 1,
} as const;

export const SPRING_HEAVY_SNAP = {
  type: "spring",
  stiffness: 1200,
  damping: 80,
  mass: 0.1,
} as const;

export function prefersReducedMotion(): boolean {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}
