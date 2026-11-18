import { prefersReducedMotion } from "./motion";

export interface FollowState {
  pinned: boolean;
  canJump: boolean;
}

export interface ScrollEdgeOptions {
  size?: number;
  tolerance?: number;
  follow?: {
    onChange?: (state: FollowState) => void;
    hint?: number;
  };
}

export interface ScrollEdges {
  followLatest(smooth?: boolean): void;
  dispose(): void;
}

const INPUT_COOLDOWN_MS = 150;
const USER_INPUTS = [
  "wheel",
  "touchstart",
  "touchmove",
  "pointerdown",
  "keydown",
] as const;

export function installScrollEdges(
  element: HTMLElement | null | undefined,
  options: ScrollEdgeOptions = {},
): ScrollEdges {
  if (!element) return { followLatest: () => {}, dispose: () => {} };
  const el = element;
  const size = options.size ?? 28;
  const tolerance = options.tolerance ?? 2;
  const follow = options.follow?.onChange ? options.follow : undefined;
  const followHint = follow?.hint ?? 120;

  let pinned = true;
  let canJump = false;
  let lastTop = el.scrollTop;
  let seenHeight = el.scrollHeight;
  let held = false;
  let deferred = false;
  let release: ReturnType<typeof setTimeout> | undefined;
  let frame = 0;

  function notify() {
    follow?.onChange?.({ pinned, canJump });
  }

  function setPinned(next: boolean) {
    if (next === pinned) return;
    pinned = next;
    notify();
  }

  function lineHeight() {
    const style = getComputedStyle(el);
    const line = parseFloat(style.lineHeight);
    return Number.isFinite(line) ? line : parseFloat(style.fontSize);
  }

  function trackPosition() {
    const top = el.scrollTop;
    const height = el.scrollHeight;
    const distance = Math.min(height, seenHeight) - top - el.clientHeight;
    if (distance <= tolerance) setPinned(true);
    else if (top < lastTop) setPinned(false);
    else if (top > lastTop && distance <= lineHeight()) setPinned(true);
    lastTop = top;
    if (!held) seenHeight = height;
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
        const nextJump = el.scrollHeight - top - el.clientHeight > followHint;
        if (nextJump !== canJump) {
          canJump = nextJump;
          notify();
        }
      }
    });
  }

  function followContent() {
    if (!follow) return;
    trackPosition();
    deferred = held;
    if (!pinned || held) return;
    el.scrollTop = el.scrollHeight;
    lastTop = el.scrollTop;
  }

  function hold() {
    held = true;
    clearTimeout(release);
    release = setTimeout(() => {
      held = false;
      if (deferred) followContent();
    }, INPUT_COOLDOWN_MS);
  }

  function markUserInput() {
    seenHeight = el.scrollHeight;
    hold();
  }

  function onScroll() {
    if (follow) {
      trackPosition();
      if (held) hold();
    }
    measure();
  }

  function onResize() {
    measure();
    followContent();
  }

  function followLatest(smooth = false) {
    el.scrollTo({
      top: el.scrollHeight,
      behavior: smooth && !prefersReducedMotion() ? "smooth" : "auto",
    });
    clearTimeout(release);
    held = false;
    deferred = false;
    lastTop = el.scrollTop;
    seenHeight = el.scrollHeight;
    pinned = true;
    canJump = false;
    notify();
  }

  const observer = new ResizeObserver(onResize);
  observer.observe(el);
  const mutations = new MutationObserver(() => {
    measure();
    followContent();
  });
  mutations.observe(el, { childList: true, subtree: true, characterData: true });

  el.addEventListener("scroll", onScroll, { passive: true });
  if (follow)
    for (const input of USER_INPUTS)
      el.addEventListener(input, markUserInput, { passive: true });
  followContent();
  measure();
  notify();

  return {
    followLatest,
    dispose() {
      if (frame) cancelAnimationFrame(frame);
      clearTimeout(release);
      mutations.disconnect();
      observer.disconnect();
      el.removeEventListener("scroll", onScroll);
      for (const input of USER_INPUTS)
        el.removeEventListener(input, markUserInput);
    },
  };
}
