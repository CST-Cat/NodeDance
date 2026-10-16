/**
 * Shared spring presets for framer-motion transitions.
 *
 * Ported from the local Open Claude UI reference
 * (.references/open-claude-ui/code-examples/ClaudeMotionPresets.ts). Both
 * presets below were already inlined in NodeDance surfaces (sidebar context
 * swap, custom select list reveal); they are centralised here so the values
 * have one authoritative definition instead of duplicated literals.
 *
 * - SPRING_SNAPPY: settled reveal for popovers and lists (small overshoot).
 * - SPRING_HEAVY_SNAP: overdamped panel hand-off with zero overshoot.
 * Framer's MotionConfig reducedMotion="user" (main.tsx) disables these when
 * the user prefers reduced motion.
 */
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
