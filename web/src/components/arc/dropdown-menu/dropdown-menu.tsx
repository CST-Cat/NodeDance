"use client";

import { Fragment, useEffect, useLayoutEffect, useRef, useState, forwardRef } from "react";
import type { CSSProperties, FocusEvent, ReactNode, ButtonHTMLAttributes } from "react";
import * as DropdownPrimitive from "@radix-ui/react-dropdown-menu";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { Check, ChevronDown } from "lucide-react";
import { motionTokens } from "../lib/motion-tokens";
import styles from "./dropdown-menu.module.css";

export interface DropdownItem { value?: string; label: string; onSelect?: () => void; disabled?: boolean; icon?: ReactNode; destructive?: boolean; separatorBefore?: boolean; }
export interface DropdownMenuProps {
 label: string; items: DropdownItem[]; icon?: ReactNode;
 value?: string; onValueChange?: (value: string) => void;
 disabled?: boolean; id?: string; className?: string; menuClassName?: string;
 portalContainer?: HTMLElement | null;
 triggerProps?: Omit<ButtonHTMLAttributes<HTMLButtonElement>, "children" | "onClick" | "disabled">;
 open?: boolean; onOpenChange?: (open: boolean) => void;
}

type Highlight = { top: number; height: number; danger: boolean; glide: boolean };

/** A new trigger label rises in while the old one leaves, and the trigger width springs to the measured text instead of snapping. */
function TriggerLabel({ text }: { text: string }) {
  const reduced = useReducedMotion();
  const measure = useRef<HTMLSpanElement>(null);
  const measured = useRef<string | null>(null);
  const [size, setSize] = useState<{ width: number | "auto"; animate: boolean }>({ width: "auto", animate: false });
  useLayoutEffect(() => {
    const node = measure.current;
    if (!node) return;
    const observer = new ResizeObserver(([entry]) => {
      if (!entry) return;
      const current = node.textContent;
      // Only a text change morphs; the first measure and font swaps settle instantly.
      const animate = measured.current !== null && measured.current !== current;
      measured.current = current;
      setSize({ width: Math.ceil(entry.borderBoxSize?.[0]?.inlineSize ?? node.offsetWidth), animate });
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, []);
  return <motion.span className={styles.label} initial={false} animate={{ width: size.width }} transition={size.animate && !reduced ? motionTokens.spring.morph : { duration: 0 }}>
    <span ref={measure} className={styles.labelMeasure} aria-hidden="true">{text}</span>
    <AnimatePresence mode="popLayout" initial={false}>
      <motion.span key={text} className={styles.labelText} data-dropdown-label="" initial={reduced ? false : { opacity: 0, y: "0.3em", filter: `blur(${motionTokens.blur.soft}px)` }} animate={{ opacity: 1, y: 0, filter: "blur(0px)" }} exit={reduced ? { opacity: 0, transition: { duration: 0 } } : { opacity: 0, y: "-0.3em", filter: `blur(${motionTokens.blur.subtle}px)`, transition: { duration: motionTokens.duration.fast, ease: [...motionTokens.ease.standard] } }} transition={{ duration: motionTokens.duration.standard, ease: [...motionTokens.ease.enter] }}>{text}</motion.span>
    </AnimatePresence>
  </motion.span>;
}

export const DropdownMenu = forwardRef<HTMLButtonElement, DropdownMenuProps>(function DropdownMenu({ label, items, icon, value, onValueChange, disabled, id, className, menuClassName, portalContainer, triggerProps, open, onOpenChange }: DropdownMenuProps, ref) {
  const trigger = useRef<HTMLButtonElement | null>(null);
  const [container, setContainer] = useState<HTMLElement | null>(null);
  useLayoutEffect(() => { setContainer(trigger.current?.closest<HTMLElement>('dialog,[role="dialog"]') ?? portalContainer ?? trigger.current?.closest<HTMLElement>(".app-shell") ?? null); }, [portalContainer]);
  const selection = onValueChange !== undefined;
  const reduced = useReducedMotion();
  const [highlight, setHighlight] = useState<Highlight | null>(null);
  const pointer = useRef(false);
  const clearTimer = useRef(0);
  useEffect(() => () => window.clearTimeout(clearTimer.current), []);
  // Radix focuses the highlighted item (pointer or keyboard) and the content when the pointer leaves an item.
  function onMenuFocus(event: FocusEvent<HTMLDivElement>) {
    const item = event.target instanceof HTMLElement ? event.target.closest<HTMLElement>('[role="menuitem"],[role="menuitemradio"]') : null;
    window.clearTimeout(clearTimer.current);
    if (!item) {
      // A short grace period keeps the highlight gliding across separators and item gaps.
      clearTimer.current = window.setTimeout(() => setHighlight(null), pointer.current ? 70 : 0);
      return;
    }
    const next = { top: item.offsetTop, height: item.offsetHeight, danger: item.dataset.tone === "danger" };
    const glide = pointer.current;
    setHighlight(current => ({ ...next, glide: glide && current !== null }));
  }
  const renderItem = (item: DropdownItem, index: number) => {
    const props = {
      className: [styles.item, item.destructive ? styles.destructive : ""].filter(Boolean).join(" "),
      "data-tone": item.destructive ? "danger" : undefined,
      "data-option-value": item.value,
      style: { "--i": index } as CSSProperties,
      disabled: item.disabled,
    };
    const content = <>{item.icon && <span className={styles.icon} aria-hidden="true">{item.icon}</span>}<span className={styles.itemLabel} data-option-label="">{item.label}</span>{selection && <DropdownPrimitive.ItemIndicator className={styles.indicator}><Check size={16} aria-hidden="true" /></DropdownPrimitive.ItemIndicator>}</>;
    return <Fragment key={item.value ?? item.label}>{item.separatorBefore && <DropdownPrimitive.Separator className={styles.separator}/>} {selection
      ? <DropdownPrimitive.RadioItem {...props} value={item.value ?? item.label}>{content}</DropdownPrimitive.RadioItem>
      : <DropdownPrimitive.Item {...props} onSelect={item.onSelect}>{content}</DropdownPrimitive.Item>}
    </Fragment>;
  };
  return <DropdownPrimitive.Root open={open} onOpenChange={next => { if (next) { window.clearTimeout(clearTimer.current); setHighlight(null); } onOpenChange?.(next); }}>
    <DropdownPrimitive.Trigger {...triggerProps} ref={node => { trigger.current = node; if (typeof ref === "function") ref(node); else if (ref) ref.current = node; }} id={id} disabled={disabled} data-dropdown-field={selection ? "" : undefined} data-selected-value={selection ? value : undefined} className={[styles.trigger, className].filter(Boolean).join(" ")} type="button">{icon && <span className={styles.triggerIcon} aria-hidden="true">{icon}</span>}<TriggerLabel text={label}/><ChevronDown className={styles.chevron} size={15} strokeWidth={1.8} aria-hidden="true"/></DropdownPrimitive.Trigger>
    <DropdownPrimitive.Portal container={container}><DropdownPrimitive.Content className={["product-arc arc-ui product-dropdown-menu", styles.menu, menuClassName].filter(Boolean).join(" ")} data-selection={selection || undefined} sideOffset={6} align={selection ? "start" : "end"} collisionPadding={12} loop onFocus={onMenuFocus} onPointerMoveCapture={() => { pointer.current = true; }} onKeyDownCapture={() => { pointer.current = false; }}>
      <motion.span className={styles.highlight} data-tone={highlight?.danger ? "danger" : undefined} aria-hidden="true" initial={false} animate={highlight ? { y: highlight.top, height: highlight.height, opacity: 1 } : { opacity: 0 }} transition={{ default: highlight?.glide && !reduced ? motionTokens.spring.snappy : { duration: 0 }, opacity: { duration: reduced ? 0 : .08 } }}/>
      {selection ? <DropdownPrimitive.RadioGroup value={value} onValueChange={onValueChange}>{items.map(renderItem)}</DropdownPrimitive.RadioGroup> : items.map(renderItem)}
    </DropdownPrimitive.Content></DropdownPrimitive.Portal>
  </DropdownPrimitive.Root>;
});

export default DropdownMenu;
