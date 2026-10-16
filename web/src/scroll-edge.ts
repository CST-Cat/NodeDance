/**
 * Scroll-edge fading and streaming auto-follow for overflowing panels.
 *
 * Ported from the local Open Claude UI reference:
 * - code-examples/ScrollFadeContainer.tsx (7-stop nonlinear alpha mask,
 *   2px boundary tolerance, 28px feather size, scroll-state sensors)
 * - code-examples/useClaudeScrollPinning.ts (40px bottom snap tolerance,
 *   120px "jump to latest" hint, 150ms user-input cooldown, unsmoothed
 *   programmatic re-anchoring)
 *
 * Differences from the reference (and why):
 * - The reference re-measures on scroll events plus a ResizeObserver on the
 *   scroller itself. NodeDance streams append text between scroll events, so
 *   boundary state is also re-measured from content mutations (coalesced into
 *   one animation frame).
 * - The mask is expressed as CSS custom properties consumed by the shared
 *   `.scroll-fade` rule (style.css) so panels opt in with a class and this
 *   module only measures boundaries.
 */

import { prefersReducedMotion } from "./motion";

export interface FollowState {
  /** Anchored to the bottom edge; fresh content may auto-scroll. */
  pinned: boolean;
  /** Far enough from the bottom that a "jump to latest" hint is useful. */
  canJump: boolean;
}

export interface ScrollEdgeOptions {
  /** Feather size in px (Open Claude UI uses 28). */
  size?: number;
  /** Boundary tolerance in px (Open Claude UI uses 2). */
  tolerance?: number;
  /** Streaming auto-follow behaviour (useClaudeScrollPinning port). */
  follow?: {
    onChange?: (state: FollowState) => void;
    /** Snap tolerance from the bottom, px (default 40). */
    tolerance?: number;
    /** Distance that enables the jump hint, px (default 120). */
    hint?: number;
  };
}

export interface ScrollEdges {
  /** Re-anchor to the bottom edge; instant by default and under reduced motion. */
  followLatest(smooth?: boolean): void;
  dispose(): void;
}

const INPUT_COOLDOWN_MS = 150;

export function installScrollEdges(
  element: HTMLElement | null | undefined,
  options: ScrollEdgeOptions = {},
): ScrollEdges {
  if (!element) return { followLatest: () => {}, dispose: () => {} };
  const el = element;
  const size = options.size ?? 28;
  const tolerance = options.tolerance ?? 2;
  const follow = options.follow?.onChange ? options.follow : undefined;
  const followTolerance = follow?.tolerance ?? 40;
  const followHint = follow?.hint ?? 120;

  let pinned = true;
  let canJump = false;
  let lastUserInputAt = 0;
  let frame = 0;

  function notify() {
    follow?.onChange?.({ pinned, canJump });
  }

  function measure() {
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      const top = el.scrollTop;
      const atTop = top <= tolerance;
      const atBottom = top + el.clientHeight >= el.scrollHeight - tolerance;
      el.style.setProperty("--scroll-fade-size", `${size}px`);
      el.style.setProperty("--scroll-fade-top", atTop ? "0px" : `${size}px`);
      el.style.setProperty(
        "--scroll-fade-bottom",
        atBottom ? "0px" : `${size}px`,
      );
      if (follow) {
        const distance = el.scrollHeight - top - el.clientHeight;
        const nextPinned = distance <= followTolerance;
        const nextJump = distance > followHint;
        if (nextPinned !== pinned || nextJump !== canJump) {
          pinned = nextPinned;
          canJump = nextJump;
          notify();
        }
      }
    });
  }

  /**
   * useClaudeScrollPinning's dependency effect: while pinned (and outside the
   * user-input cooldown) new content re-anchors the viewport instantly.
   */
  function followContent() {
    if (!follow) return;
    const distance = el.scrollHeight - el.scrollTop - el.clientHeight;
    if (
      pinned &&
      distance > followTolerance &&
      performance.now() - lastUserInputAt > INPUT_COOLDOWN_MS
    ) {
      el.scrollTop = el.scrollHeight;
      pinned = true;
      canJump = false;
      notify();
    }
  }

  // Wheel/touch input releases the pin immediately (OpenUI markUserInput).
  function markUserInput() {
    lastUserInputAt = performance.now();
    if (pinned || canJump) {
      pinned = false;
      notify();
    }
  }

  // Programmatic appends also dispatch scroll events; measure() recomputes
  // purely from geometry, so no scroll source needs special-casing.
  function onScroll() {
    measure();
  }

  /**
   * Re-anchor to the bottom edge. The OpenUI reference jumps instantly
   * (behavior "auto": debounced, non-smooth, instant alignment) so a
   * mid-flight position can never flip the pin state back; smooth scrolling
   * stays available for explicit callers and is disabled under reduced motion.
   */
  function followLatest(smooth = false) {
    el.scrollTo({
      top: el.scrollHeight,
      behavior: smooth && !prefersReducedMotion() ? "smooth" : "auto",
    });
    // An explicit jump is a programmatic re-anchor: it re-establishes the
    // streaming contract, so any pending user-input cooldown no longer
    // suppresses auto-following (useClaudeScrollPinning resets on jump).
    lastUserInputAt = 0;
    pinned = true;
    canJump = false;
    notify();
  }

  const observer = new ResizeObserver(measure);
  observer.observe(el);
  // Content appends change scrollHeight without changing the scroller's own
  // box, which the ResizeObserver above cannot see.
  const mutations = new MutationObserver(() => {
    measure();
    followContent();
  });
  mutations.observe(el, { childList: true, subtree: true, characterData: true });

  el.addEventListener("scroll", onScroll, { passive: true });
  el.addEventListener("wheel", markUserInput, { passive: true });
  el.addEventListener("touchstart", markUserInput, { passive: true });
  measure();
  notify();

  return {
    followLatest,
    dispose() {
      if (frame) cancelAnimationFrame(frame);
      mutations.disconnect();
      observer.disconnect();
      el.removeEventListener("scroll", onScroll);
      el.removeEventListener("wheel", markUserInput);
      el.removeEventListener("touchstart", markUserInput);
    },
  };
}
