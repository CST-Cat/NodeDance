"use client";

import { forwardRef, useCallback, useEffect, useId, useImperativeHandle, useRef, useState, useSyncExternalStore } from "react";
import type { FocusEvent, InputHTMLAttributes, KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent, ReactNode } from "react";
import { createPortal } from "react-dom";
import { AnimatePresence, animate, motion, useMotionValue, useReducedMotion, useTransform } from "motion/react";
import type { Transition } from "motion/react";
import { motionTokens } from "../lib/motion-tokens";
import styles from "./scrub-input.module.css";

export type ScrubInputSource = "scrub" | "type" | "key" | "reset";
export interface ScrubInputChangeDetails {
  /** What produced the value: a label drag, a typed entry, an arrow key, or a double click reset. */
  source: ScrubInputSource;
  /** The unit in effect after the change. */
  unit: string;
}

/**
 * A compact number field for inspectors and property panels. Drag the label sideways to scrub the value (the pointer is locked when the
 * browser allows it, so a long drag never runs out of screen), or click in and type an exact value. Typed entries accept arithmetic such as
 * `12*2` or `(100-24)/2`, and a unit from `units` such as `50%`. Shift scrubs and steps ten times coarser, Alt ten times finer. At `min` and
 * `max` the field strains past the limit with rubber band resistance and springs back on release. Arrow keys step, Enter commits, Escape
 * restores the last value, and a double click on the label restores `resetValue`.
 */
export interface ScrubInputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "defaultValue" | "onChange" | "type" | "size" | "min" | "max" | "step" | "prefix"> {
  /** Text or a 16px icon. Icon labels need `aria-label` so the field keeps an accessible name. */
  label: ReactNode;
  value?: number;
  defaultValue?: number;
  /** Fires for every change, including each step of a scrub. */
  onValueChange?: (value: number, details: ScrubInputChangeDetails) => void;
  /** Fires once when a change settles: a scrub ends, Enter or blur commits typing, or a key step lands. */
  onValueCommit?: (value: number, details: ScrubInputChangeDetails) => void;
  min?: number;
  max?: number;
  /** One step for arrows and scrubbing. Defaults to 1. */
  step?: number;
  /** Step with Alt held. Defaults to a tenth of `step`. */
  fineStep?: number;
  /** Step with Shift held. Defaults to ten times `step`. */
  coarseStep?: number;
  /** Decimal places kept. Defaults to the precision of `fineStep`. */
  precision?: number;
  /** Pointer travel in px for one step while scrubbing. Defaults to 2. */
  pixelsPerStep?: number;
  /** Values wrap around from `max` to `min` instead of stopping, as rotation does. Needs both limits. */
  wrap?: boolean;
  /** The unit shown after the number. With more than one entry in `units`, it becomes a button that cycles them. */
  unit?: string;
  defaultUnit?: string;
  /** Units a typed value may carry, such as ["px", "%"]. Defaults to the current unit. */
  units?: readonly string[];
  onUnitChange?: (unit: string) => void;
  /** Double click the label to return here. */
  resetValue?: number;
  /** Shows how far the value sits between `min` and `max` as a quiet fill. On by default when both limits are set. */
  track?: boolean;
  size?: "sm" | "md";
  /** Locks the pointer while scrubbing when the browser allows it. On by default. */
  pointerLock?: boolean;
  className?: string;
}

const { spring, duration, ease } = motionTokens;
/** Movement before a press on the label becomes a scrub, and the most pixels the field strains past a limit. */
const SLOP = 3, STRAIN = 12, OVERSHOOT_CAP = 90;
const physical = (visualDuration: number, bounce: number): Transition => {
  const root = 2 * Math.PI / (visualDuration * 1.2);
  return { type: "spring", stiffness: root * root, damping: 2 * (1 - bounce) * root, mass: 1, restDelta: .01, restSpeed: .5 };
};
const settle = physical(.34, 0), shake = physical(.3, 0);

const decimalsOf = (value: number) => {
  const text = String(value);
  if (text.includes("e-")) return Number(text.split("e-")[1]);
  return (text.split(".")[1] ?? "").length;
};
const roundTo = (value: number, places: number) => { const f = 10 ** places; return Math.round(value * f) / f; };
/** iOS style resistance: each extra pixel gives less, and the strain never exceeds `limit`. */
const rubber = (distance: number, limit = STRAIN) => Math.sign(distance) * (1 - 1 / (Math.abs(distance) * .55 / limit + 1)) * limit;
const format = (value: number, places: number) => String(roundTo(value, places)).replace(/^-0$/, "0");

type Parsed = { value: number; unit?: string } | null;

/** Evaluates + - * / and parentheses over numbers that may carry a unit. Returns null for anything else, or for an unknown unit. */
export function evaluateScrubExpression(text: string, units: readonly string[]): Parsed {
  const source = text.trim().replace(/,/g, ".").replace(/[×x](?=\s*[\d(.])/gi, "*").replace(/÷/g, "/").replace(/−/g, "-");
  if (!source) return null;
  const tokens = source.match(/\d*\.?\d+(?:e[+-]?\d+)?|[a-z%°]+|[-+*/()]|\S/gi);
  if (!tokens) return null;
  let index = 0, unit: string | undefined, bad = false;
  const known = units.map(entry => entry.toLowerCase());
  const peek = () => tokens[index];
  function primary(): number {
    const token = tokens![index++];
    if (token === "(") { const inner = sum(); if (tokens![index++] !== ")") bad = true; return inner; }
    if (token === "-") return -primary();
    if (token === "+") return primary();
    if (token === undefined || !/^\d*\.?\d+/.test(token)) { bad = true; return 0; }
    const number = Number(token);
    const next = peek();
    if (next && /^[a-z%°]+$/i.test(next)) {
      index++;
      const word = next.toLowerCase(), alias = word === "°" ? "deg" : word === "deg" ? "°" : word;
      const found = known.includes(word) ? known.indexOf(word) : known.indexOf(alias);
      if (found < 0) bad = true; else unit = units[found];
    }
    return number;
  }
  function product(): number {
    let left = primary();
    while (peek() === "*" || peek() === "/") { const op = tokens![index++]; const right = primary(); left = op === "*" ? left * right : left / right; }
    return left;
  }
  function sum(): number {
    let left = product();
    while (peek() === "+" || peek() === "-") { const op = tokens![index++]; const right = product(); left = op === "+" ? left + right : left - right; }
    return left;
  }
  const value = sum();
  if (bad || index !== tokens.length || !Number.isFinite(value)) return null;
  return { value, unit };
}

const subscribe = () => () => {};
function useHydrated() { return useSyncExternalStore(subscribe, () => true, () => false); }

type Drag = { id: number; lastX: number; travel: number; raw: number; start: number; moved: boolean; locked: boolean; mouse: boolean };

export const ScrubInput = forwardRef<HTMLInputElement, ScrubInputProps>(function ScrubInput({
  label, value, defaultValue = 0, onValueChange, onValueCommit, min = -Infinity, max = Infinity, step = 1, fineStep, coarseStep, precision,
  pixelsPerStep = 2, wrap = false, unit: unitProp, defaultUnit, units, onUnitChange, resetValue, track, size = "md", pointerLock = true,
  className, disabled, id: idProp, onFocus, onBlur, onKeyDown, "aria-label": ariaLabel, ...rest
}, forwarded) {
  const uid = useId();
  const id = idProp ?? `scrub-${uid.replace(/[^a-zA-Z0-9-]/g, "")}`;
  const hydrated = useHydrated();
  const reduced = !!useReducedMotion() && hydrated;
  const inputRef = useRef<HTMLInputElement>(null);
  const shellRef = useRef<HTMLDivElement>(null);
  const handleRef = useRef<HTMLLabelElement>(null);
  useImperativeHandle(forwarded, () => inputRef.current!, []);

  const fine = fineStep ?? step / 10, coarse = coarseStep ?? step * 10;
  const places = precision ?? Math.max(decimalsOf(step), decimalsOf(fine));
  const canWrap = wrap && Number.isFinite(min) && Number.isFinite(max) && max > min;
  const showTrack = (track ?? true) && Number.isFinite(min) && Number.isFinite(max) && max > min;

  const [inner, setInner] = useState(defaultValue);
  const current = value ?? inner;
  const [innerUnit, setInnerUnit] = useState(defaultUnit ?? unitProp ?? units?.[0] ?? "");
  const unit = unitProp ?? innerUnit;
  const unitList = units ?? (unit ? [unit] : []);
  const [draft, setDraft] = useState<string | null>(null);
  const [scrubbing, setScrubbing] = useState<null | { locked: boolean; mouse: boolean }>(null);
  const [modifier, setModifier] = useState<"fine" | "coarse" | null>(null);
  const [invalid, setInvalid] = useState(false);

  const latest = useRef({ current, unit });
  useEffect(() => { latest.current = { current, unit }; }, [current, unit]);
  const drag = useRef<Drag | null>(null);
  const suppressClick = useRef(false);
  const invalidTimer = useRef(0);
  useEffect(() => () => window.clearTimeout(invalidTimer.current), []);

  /** Strain past a limit in px: positive past max, negative past min. It drives the shell stretch and the value's lean. */
  const strain = useMotionValue(0);
  const shellWidth = useMotionValue(160);
  const shellScale = useTransform(() => 1 + Math.abs(strain.get()) / Math.max(1, shellWidth.get()));
  const shellOrigin = useTransform(strain, s => (s >= 0 ? 0 : 1));
  const lean = useTransform(strain, s => s * .5);
  const fraction = showTrack ? Math.min(1, Math.max(0, (current - min) / (max - min))) : 0;
  const fill = useMotionValue(fraction);
  const cursorX = useMotionValue(0), cursorY = useMotionValue(0);

  useEffect(() => {
    const node = shellRef.current;
    if (!node) return;
    const observer = new ResizeObserver(() => shellWidth.set(node.offsetWidth));
    observer.observe(node);
    return () => observer.disconnect();
  }, [shellWidth]);

  // The fill follows a drag 1:1 and glides to typed or stepped values.
  useEffect(() => {
    if (reduced || drag.current?.moved) { fill.jump(fraction); return; }
    const controls = animate(fill, fraction, spring.smooth);
    return () => controls.stop();
  }, [fill, fraction, reduced]);

  const emit = useCallback((next: number, source: ScrubInputSource, commit: boolean, nextUnit = latest.current.unit) => {
    const changed = next !== latest.current.current;
    if (changed) {
      latest.current = { current: next, unit: nextUnit };
      if (value === undefined) setInner(next);
      onValueChange?.(next, { source, unit: nextUnit });
    }
    if (commit) onValueCommit?.(next, { source, unit: nextUnit });
  }, [onValueChange, onValueCommit, value]);

  const constrain = (next: number) => {
    const rounded = roundTo(next, places);
    if (canWrap) { const span = max - min; return roundTo(((rounded - min) % span + span) % span + min, places); }
    return Math.min(max, Math.max(min, rounded));
  };

  /** A velocity kick on the strain: the field leans toward the push and springs home. */
  function bump(direction: number, kind: Transition = settle) {
    if (reduced) return;
    animate(strain, 0, { ...kind, velocity: direction * 260 } as Transition);
  }

  function flagInvalid() {
    setInvalid(true);
    window.clearTimeout(invalidTimer.current);
    invalidTimer.current = window.setTimeout(() => setInvalid(false), 900);
    bump(-1, shake);
  }

  function commitDraft(text: string | null = draft) {
    setDraft(null);
    if (text === null) return true;
    if (text.trim() === format(current, places)) return true;
    const parsed = evaluateScrubExpression(text, unitList);
    if (!parsed) { flagInvalid(); return false; }
    let nextUnit = latest.current.unit;
    if (parsed.unit && parsed.unit !== nextUnit) {
      nextUnit = parsed.unit;
      if (unitProp === undefined) setInnerUnit(nextUnit);
      onUnitChange?.(nextUnit);
    }
    const next = constrain(parsed.value);
    if (!canWrap && (parsed.value > max || parsed.value < min)) bump(parsed.value > max ? 1 : -1);
    emit(next, "type", true, nextUnit);
    return true;
  }

  function stepBy(direction: 1 | -1, event: { shiftKey: boolean; altKey: boolean }) {
    const amount = event.shiftKey ? coarse : event.altKey ? fine : step;
    const base = latest.current.current;
    if (!canWrap && ((direction > 0 && base >= max) || (direction < 0 && base <= min))) { bump(direction); return; }
    // Coarse steps land on the coarse grid, so 13 with Shift goes to 20, not 23.
    const snapped = event.shiftKey ? (direction > 0 ? Math.floor(base / amount + 1e-9) + 1 : Math.ceil(base / amount - 1e-9) - 1) * amount : base + direction * amount;
    emit(constrain(snapped), "key", true);
  }

  // ---- Scrubbing ----------------------------------------------------------------------------------------------------------------
  const endScrub = useCallback((cancel: boolean) => {
    const state = drag.current;
    drag.current = null;
    if (!state) return;
    document.documentElement.style.removeProperty("cursor");
    if (document.pointerLockElement === handleRef.current) document.exitPointerLock?.();
    setScrubbing(null);
    setModifier(null);
    if (!state.moved) return;
    suppressClick.current = true;
    if (cancel) emit(state.start, "scrub", true);
    else onValueCommit?.(latest.current.current, { source: "scrub", unit: latest.current.unit });
    if (reduced) strain.jump(0); else animate(strain, 0, settle);
  }, [emit, onValueCommit, reduced, strain]);

  useEffect(() => {
    if (!scrubbing) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") { event.preventDefault(); endScrub(true); return; }
      if (event.key === "Shift" || event.key === "Alt") setModifier(event.shiftKey ? "coarse" : event.altKey ? "fine" : null);
    };
    const onLock = () => { if (drag.current?.locked && document.pointerLockElement !== handleRef.current) endScrub(false); };
    window.addEventListener("keydown", onKey);
    window.addEventListener("keyup", onKey);
    document.addEventListener("pointerlockchange", onLock);
    return () => { window.removeEventListener("keydown", onKey); window.removeEventListener("keyup", onKey); document.removeEventListener("pointerlockchange", onLock); };
  }, [endScrub, scrubbing]);

  useEffect(() => () => { document.documentElement.style.removeProperty("cursor"); }, []);

  function onHandleDown(event: ReactPointerEvent<HTMLLabelElement>) {
    if (disabled || drag.current || (event.pointerType === "mouse" && event.button !== 0)) return;
    event.preventDefault();
    if (draft !== null) commitDraft();
    event.currentTarget.setPointerCapture(event.pointerId);
    drag.current = { id: event.pointerId, lastX: event.clientX, travel: 0, raw: latest.current.current, start: latest.current.current, moved: false, locked: false, mouse: event.pointerType === "mouse" };
    cursorX.jump(event.clientX); cursorY.jump(event.clientY);
  }

  function onHandleMove(event: ReactPointerEvent<HTMLLabelElement>) {
    const state = drag.current;
    if (!state || event.pointerId !== state.id) return;
    const locked = document.pointerLockElement === handleRef.current;
    const dx = locked ? event.movementX : event.clientX - state.lastX;
    state.lastX = event.clientX;
    state.travel += dx;
    if (!state.moved) {
      if (Math.abs(state.travel) < SLOP) return;
      state.moved = true;
      inputRef.current?.blur();
      document.documentElement.style.cursor = "ew-resize";
      if (pointerLock && state.mouse && handleRef.current?.requestPointerLock) {
        try {
          const request = handleRef.current.requestPointerLock() as unknown;
          if (request instanceof Promise) request.catch(() => {});
        } catch { /* Pointer lock is optional. */ }
      }
      setScrubbing({ locked: false, mouse: state.mouse });
    }
    if (locked && !state.locked) { state.locked = true; setScrubbing({ locked: true, mouse: true }); }

    // A virtual cursor stands in for the locked pointer and wraps at the window edges, the way long scrubs do in design tools.
    if (locked) {
      const width = window.innerWidth;
      cursorX.jump(((cursorX.get() + dx) % width + width) % width);
    } else { cursorX.jump(event.clientX); cursorY.jump(event.clientY); }

    const amount = event.shiftKey ? coarse : event.altKey ? fine : step;
    const mode = event.shiftKey ? "coarse" : event.altKey ? "fine" : null;
    if (mode !== modifier) setModifier(mode);
    state.raw += (dx / pixelsPerStep) * amount;

    if (!canWrap) {
      const perPx = amount / pixelsPerStep;
      const over = state.raw > max ? (state.raw - max) / perPx : state.raw < min ? (state.raw - min) / perPx : 0;
      // Cap the stored overshoot so reversing starts moving the value again after a short relax, not a long walk back.
      if (Math.abs(over) > OVERSHOOT_CAP) state.raw = (over > 0 ? max : min) + Math.sign(over) * OVERSHOOT_CAP * perPx;
      strain.jump(reduced ? 0 : rubber(Math.max(-OVERSHOOT_CAP, Math.min(OVERSHOOT_CAP, over))));
    }
    const snapped = Math.round(state.raw / amount) * amount;
    const next = constrain(snapped);
    if (next !== latest.current.current) emit(next, "scrub", false);
  }

  function onHandleUp(event: ReactPointerEvent<HTMLLabelElement>) {
    const state = drag.current;
    if (!state || event.pointerId !== state.id) return;
    endScrub(false);
  }

  function onHandleClick(event: ReactMouseEvent<HTMLLabelElement>) {
    if (suppressClick.current) { suppressClick.current = false; event.preventDefault(); return; }
    if (disabled) return;
    event.preventDefault();
    inputRef.current?.focus();
    inputRef.current?.select();
  }

  function onHandleDoubleClick() {
    if (disabled || resetValue === undefined) return;
    emit(constrain(resetValue), "reset", true);
  }

  // ---- Typing -------------------------------------------------------------------------------------------------------------------
  function onInputKeyDown(event: ReactKeyboardEvent<HTMLInputElement>) {
    onKeyDown?.(event);
    if (event.defaultPrevented) return;
    if (event.key === "ArrowUp" || event.key === "ArrowDown") {
      event.preventDefault();
      if (draft !== null && !commitDraft()) return;
      stepBy(event.key === "ArrowUp" ? 1 : -1, event);
      requestAnimationFrame(() => inputRef.current?.select());
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      commitDraft();
      requestAnimationFrame(() => inputRef.current?.select());
      return;
    }
    if (event.key === "Escape" && draft !== null) {
      event.preventDefault();
      event.stopPropagation();
      setDraft(null);
      requestAnimationFrame(() => inputRef.current?.select());
    }
  }

  function onInputFocus(event: FocusEvent<HTMLInputElement>) {
    onFocus?.(event);
    event.currentTarget.select();
  }
  function onInputBlur(event: FocusEvent<HTMLInputElement>) {
    onBlur?.(event);
    commitDraft();
  }

  function cycleUnit() {
    if (unitList.length < 2) return;
    const next = unitList[(unitList.indexOf(unit) + 1) % unitList.length]!;
    if (unitProp === undefined) setInnerUnit(next);
    onUnitChange?.(next);
    latest.current = { ...latest.current, unit: next };
  }

  const text = draft ?? format(current, places);
  const valueText = `${format(current, places)}${unit ? ` ${unit === "%" ? "percent" : unit === "deg" || unit === "°" ? "degrees" : unit}` : ""}`;
  const multiplier = modifier === "coarse" ? `×${format(coarse / step, 2)}` : modifier === "fine" ? `×${format(fine / step, 3)}` : null;
  const labelIsText = typeof label === "string" || typeof label === "number";

  return <div className={[styles.root, className].filter(Boolean).join(" ")} data-size={size} data-disabled={disabled ? "" : undefined}
    data-scrubbing={scrubbing ? "" : undefined} data-invalid={invalid ? "" : undefined}>
    <motion.div ref={shellRef} className={styles.shell} aria-hidden="true" style={{ scaleX: shellScale, originX: shellOrigin }}>
      {showTrack && <motion.span className={styles.fill} style={{ scaleX: fill }} />}
    </motion.div>
    <label ref={handleRef} htmlFor={id} className={styles.handle} data-text={labelIsText ? "" : undefined}
      onPointerDown={onHandleDown} onPointerMove={onHandleMove} onPointerUp={onHandleUp} onPointerCancel={onHandleUp}
      onClick={onHandleClick} onDoubleClick={onHandleDoubleClick}>
      {label}
    </label>
    <motion.div className={styles.body} style={{ x: lean }}>
      <input
        {...rest}
        ref={inputRef}
        id={id}
        className={styles.input}
        type="text"
        inputMode="decimal"
        autoComplete="off"
        spellCheck={false}
        role="spinbutton"
        aria-label={ariaLabel}
        aria-valuenow={current}
        aria-valuemin={Number.isFinite(min) ? min : undefined}
        aria-valuemax={Number.isFinite(max) ? max : undefined}
        aria-valuetext={valueText}
        aria-invalid={invalid || undefined}
        disabled={disabled}
        value={text}
        onChange={event => setDraft(event.target.value)}
        onFocus={onInputFocus}
        onBlur={onInputBlur}
        onKeyDown={onInputKeyDown}
      />
      {unit && (unitList.length > 1
        ? <button type="button" className={styles.unit} data-cycle="" disabled={disabled} onClick={cycleUnit} aria-label={`Unit: ${unit}. Change unit`}>
            <AnimatePresence mode="popLayout" initial={false}>
              <motion.span key={unit} className={styles.unitText}
                initial={reduced ? { opacity: 0 } : { opacity: 0, y: 6, filter: "blur(2px)" }} animate={{ opacity: 1, y: 0, filter: "blur(0px)" }}
                exit={reduced ? { opacity: 0 } : { opacity: 0, y: -6, filter: "blur(2px)", transition: { duration: duration.instant } }}
                transition={reduced ? { duration: duration.instant } : spring.snappy}>{unit}</motion.span>
            </AnimatePresence>
          </button>
        : <span className={styles.unit} aria-hidden="true">{unit}</span>)}
    </motion.div>
    <AnimatePresence>
      {multiplier && !scrubbing?.locked && <motion.span key="tag" className={styles.tag} aria-hidden="true"
        initial={reduced ? { opacity: 0 } : { opacity: 0, y: 4, scale: .96 }} animate={{ opacity: 1, y: 0, scale: 1 }}
        exit={{ opacity: 0, transition: { duration: duration.instant } }} transition={reduced ? { duration: duration.instant } : spring.snappy}>
        {multiplier}
      </motion.span>}
    </AnimatePresence>
    {hydrated && scrubbing?.locked && createPortal(
      <motion.div className={styles.cursor} style={{ x: cursorX, y: cursorY }} aria-hidden="true"
        initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ duration: duration.instant, ease: [...ease.standard] }}>
        <svg width="26" height="16" viewBox="0 0 26 16" className={styles.cursorGlyph}>
          <path d="M1.5 8 L7.5 2.5 V6 H18.5 V2.5 L24.5 8 L18.5 13.5 V10 H7.5 V13.5 Z" />
        </svg>
        <AnimatePresence>
          {multiplier && <motion.span key="multiplier" className={styles.multiplier}
            initial={reduced ? { opacity: 0 } : { opacity: 0, scale: .9, y: -2 }} animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={{ opacity: 0, transition: { duration: duration.instant } }} transition={reduced ? { duration: duration.instant } : spring.snappy}>
            {multiplier}
          </motion.span>}
        </AnimatePresence>
      </motion.div>, document.body)}
  </div>;
});

export default ScrubInput;
