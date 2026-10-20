import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { flushSync } from "react-dom";
import { prefersReducedMotion } from "../motion";
import "./MobileOverviewBar.css";

type Mode = "inline" | "expanded" | "compact";

// One set of controls anchored to the original count card. Resize the slot
// smoothly and reserve the released space at the page end, so document scroll
// position remains stable even near the bottom; never scroll or pin the page.
export default function MobileOverviewBar({ children, holdExpanded }: {
  children: ReactNode; holdExpanded: boolean;
}) {
  const slot = useRef<HTMLDivElement>(null), panel = useRef<HTMLDivElement>(null);
  const [mode, setMode] = useState<Mode>("inline");
  const current = useRef<Mode>("inline"), hold = useRef(holdExpanded);
  const before = useRef(new Map<Element, DOMRect>());
  const animations = useRef<Animation[]>([]);
  const frameBefore = useRef<{ height: number; reserve: number } | null>(null);
  hold.current = holdExpanded;

  function change(next: Mode) {
    if (current.current === next || !slot.current || !panel.current) return;
    before.current = new Map(Array.from(panel.current.querySelectorAll<HTMLElement>(
      ".node-summary strong, .node-count-label, .node-count-compact-label, .node-search, .overview-search-toggle svg, .dashboard-toolbar-actions > button svg",
    )).map(el => [el, el.getBoundingClientRect()]));
    const dashboard = slot.current.closest<HTMLElement>(".nodes-dashboard")!;
    frameBefore.current = { height: slot.current.getBoundingClientRect().height, reserve: matchMedia("(max-width: 900px)").matches ? Math.max(0, parseFloat(getComputedStyle(dashboard).paddingBottom) - 32) : 0 };
    animations.current.forEach(animation => animation.cancel());
    current.current = next;
    setMode(next);
  }

  useLayoutEffect(() => {
    if (!frameBefore.current || !panel.current || !slot.current) return;
    const previous = frameBefore.current;
    frameBefore.current = null;
    const dashboard = slot.current.closest<HTMLElement>(".nodes-dashboard")!;
    const nextHeight = panel.current.getBoundingClientRect().height;
    const mobile = matchMedia("(max-width: 900px)").matches;
    const reserve = mobile ? Math.max(0, previous.reserve + previous.height - nextHeight) : 0;
    dashboard.style.setProperty("--overview-scroll-reserve", `${reserve}px`);
    if (prefersReducedMotion() || !mobile) {
      slot.current.style.removeProperty("height");
      before.current.clear();
      return;
    }
    const easing = getComputedStyle(panel.current).getPropertyValue("--ux-ease").trim();
    const options = { duration: 220, easing, fill: "none" as const };
    slot.current.style.height = `${nextHeight}px`;
    const heightAnimation = slot.current.animate([{ height: `${previous.height}px` }, { height: `${nextHeight}px` }], options);
    animations.current = [heightAnimation, dashboard.animate([{ paddingBottom: `${32 + previous.reserve}px` }, { paddingBottom: `${32 + reserve}px` }], options)];
    const target = mode;
    void heightAnimation.finished.then(() => {
      if (current.current === target) slot.current?.style.removeProperty("height");
    }).catch(() => {});
    for (const [el, previous] of before.current) {
      const next = el.getBoundingClientRect();
      if (!next.width || !next.height) continue;
      const visibleBefore = previous.width > 0 && previous.height > 0;
      const from = visibleBefore
        ? `translate(${previous.x - next.x}px, ${previous.y - next.y}px) scale(${previous.width / next.width}, ${previous.height / next.height})`
        : "translateY(6px)";
      animations.current.push(el.animate([
        { transform: from, opacity: visibleBefore ? 1 : 0 },
        { transform: "none", opacity: 1 },
      ], { duration: 220, easing, fill: "none" }));
    }
    before.current.clear();
  }, [mode]);

  useEffect(() => {
    const mobile = matchMedia("(max-width: 900px)");
    let last = Math.max(0, scrollY), distance = 0, frame = 0;
    function update() {
      frame = 0;
      const y = Math.max(0, Math.min(scrollY, document.documentElement.scrollHeight - innerHeight));
      const delta = y - last;
      last = y;
      if (!mobile.matches || !slot.current || !panel.current) return;
      if (y <= 12) {
        distance = 0;
        change("inline");
        return;
      }
      if ((delta > 0 && distance < 0) || (delta < 0 && distance > 0)) distance = 0;
      distance += delta;
      const editing = panel.current.contains(document.activeElement)
        && document.activeElement?.matches("input, textarea, select, [role=combobox]");
      if (hold.current || editing) { if (current.current === "compact") change("expanded"); return; }
      if (distance > 24) {
        change("compact"); distance = 0;
      } else if (distance < -16) {
        change("expanded"); distance = 0;
      }
    }
    const scroll = () => { if (!frame) frame = requestAnimationFrame(update); };
    const resize = () => {
      last = Math.max(0, scrollY); distance = 0;
      if (!mobile.matches) change("inline");
      else scroll();
    };
    window.addEventListener("scroll", scroll, { passive: true });
    window.addEventListener("resize", resize);
    mobile.addEventListener("change", resize);
    scroll();
    return () => {
      window.removeEventListener("scroll", scroll);
      window.removeEventListener("resize", resize);
      mobile.removeEventListener("change", resize);
      cancelAnimationFrame(frame);
      animations.current.forEach(animation => animation.cancel());
      slot.current?.closest<HTMLElement>(".nodes-dashboard")?.style.removeProperty("--overview-scroll-reserve");
    };
  }, []);

  useLayoutEffect(() => {
    if (holdExpanded && current.current === "compact") change("expanded");
  }, [holdExpanded]);

  return <div ref={slot} className="overview-toolbar-slot" data-mode={mode}>
    <div ref={panel} className="overview-toolbar-panel" onClickCapture={event => {
      if (!(event.target instanceof Element) || !event.target.closest("[data-expand-search]")) return;
      flushSync(() => change("expanded"));
      panel.current?.querySelector<HTMLInputElement>(".node-search input")?.focus({ preventScroll: true });
    }} onKeyDownCapture={event => {
      if (event.key === "Escape" && current.current === "expanded" && !hold.current
        && scrollY > 12) {
        event.preventDefault();
        (document.activeElement as HTMLElement)?.blur();
        flushSync(() => change("compact"));
        panel.current?.querySelector<HTMLButtonElement>("[data-expand-search]")?.focus({ preventScroll: true });
      }
    }}>
      {children}
    </div>
  </div>;
}
