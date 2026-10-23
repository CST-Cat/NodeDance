"use client";

import { useEffect, useRef, useState } from "react";
import { GripVertical } from "lucide-react";
import { AnimatePresence, Reorder, motion, useDragControls, useMotionValue, useReducedMotion, type Variants } from "motion/react";
import { motionTokens } from "../lib/motion-tokens";
import styles from "./reorderable-list.module.css";

export type ReorderableItem = { id: string; title: string; detail?: string };

/** The drop settles on a spring close to `spring.snappy`: quick, with a hint of give. */
const settle = { bounceStiffness: 520, bounceDamping: 40 };
const roll: Variants = {
  enter: (direction: number) => ({ opacity: 0, y: `${.5 * direction}em`, filter: `blur(${motionTokens.blur.subtle}px)` }),
  center: { opacity: 1, y: 0, filter: "blur(0px)" },
  exit: (direction: number) => ({ opacity: 0, y: `${-.5 * direction}em`, filter: `blur(${motionTokens.blur.subtle}px)`, transition: { duration: motionTokens.duration.instant, ease: [...motionTokens.ease.standard] } }),
};

export function ReorderableList({ items, onReorder, label = "Reorder items" }: { items: ReorderableItem[]; onReorder: (items: ReorderableItem[]) => void; label?: string }) {
  const [moved, setMoved] = useState<string | null>(null);
  const handles = useRef(new Map<string, HTMLButtonElement>());
  useEffect(() => {
    if (!moved) return;
    // Reordering moves DOM nodes, which can drop focus. Keep it on the handle that was moved.
    const handle = handles.current.get(moved);
    if (handle && document.activeElement !== handle) handle.focus();
    const timer = window.setTimeout(() => setMoved(null), 260);
    return () => window.clearTimeout(timer);
  }, [moved, items]);
  function move(id: string, direction: -1 | 1) {
    const index = items.findIndex(item => item.id === id);
    const next = index + direction;
    if (next < 0 || next >= items.length) return;
    const copy = [...items];
    [copy[index], copy[next]] = [copy[next]!, copy[index]!];
    setMoved(id);
    onReorder(copy);
  }
  return <Reorder.Group axis="y" values={items} onReorder={onReorder} className={styles.list} aria-label={label}>
    {items.map((item, index) => <Row key={item.id} item={item} index={index} count={items.length} onMove={move} lifted={moved === item.id} handleRef={element => { if (element) handles.current.set(item.id, element); else handles.current.delete(item.id); }} />)}
  </Reorder.Group>;
}

function Row({ item, index, count, onMove, lifted, handleRef }: { item: ReorderableItem; index: number; count: number; onMove: (id: string, direction: -1 | 1) => void; lifted: boolean; handleRef: (element: HTMLButtonElement | null) => void }) {
  const controls = useDragControls();
  const reduced = useReducedMotion() ?? false;
  const [dragging, setDragging] = useState(false);
  // The row lifts as soon as the handle is pressed, before the drag threshold, and settles if released in place.
  const [pressed, setPressed] = useState(false);
  useEffect(() => {
    if (!pressed) return;
    const release = () => setPressed(false);
    window.addEventListener("pointerup", release);
    window.addEventListener("pointercancel", release);
    return () => { window.removeEventListener("pointerup", release); window.removeEventListener("pointercancel", release); };
  }, [pressed]);
  const [previous, setPrevious] = useState(index);
  const [direction, setDirection] = useState(1);
  if (previous !== index) { setDirection(index > previous ? 1 : -1); setPrevious(index); }
  const raised = dragging || lifted || pressed;
  // Reduced motion: the row still follows the pointer, but lands in its slot at once instead of gliding there.
  const y = useMotionValue(0);
  return <Reorder.Item value={item} dragControls={controls} dragListener={false} dragMomentum={false} dragTransition={settle} data-lifted={raised || undefined}
    style={{ y }}
    animate={{ scale: raised && !reduced ? 1.02 : 1 }}
    onDragStart={() => setDragging(true)} onDragEnd={() => { setDragging(false); if (reduced) y.jump(0); }}
    transition={reduced ? { duration: 0 } : { ...motionTokens.spring.smooth, scale: motionTokens.spring.snappy }} className={styles.row}>
    <button ref={handleRef} type="button" className={styles.handle} aria-label={`Reorder ${item.title}. Position ${index + 1} of ${count}. Use arrow keys.`} onPointerDown={event => { if (event.button === 0) setPressed(true); controls.start(event); }} onKeyDown={event => { if (event.key === "ArrowUp" || event.key === "ArrowDown") { event.preventDefault(); onMove(item.id, event.key === "ArrowUp" ? -1 : 1); } }}><GripVertical size={16} aria-hidden="true" /></button>
    <span className={styles.copy}><strong>{item.title}</strong>{item.detail && <small>{item.detail}</small>}</span>
    <span className={styles.position}><span className={styles.srOnly}>{String(index + 1).padStart(2, "0")}</span>
      {/* The number rolls in the direction the row travelled, so a new position reads as movement. Reduced motion keeps one node and swaps the text:
          removing a keyed digit mid-drag re-measures the row and would throw it off the pointer. */}
      <AnimatePresence mode="popLayout" initial={false} custom={direction}>
        <motion.span key={reduced ? "static" : index} aria-hidden="true" className={styles.digits} custom={direction} variants={roll} initial={reduced ? false : "enter"} animate="center" exit={reduced ? undefined : "exit"} transition={{ duration: reduced ? 0 : motionTokens.duration.standard, ease: [...motionTokens.ease.enter] }}>{String(index + 1).padStart(2, "0")}</motion.span>
      </AnimatePresence>
    </span>
  </Reorder.Item>;
}
