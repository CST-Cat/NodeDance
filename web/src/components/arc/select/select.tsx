"use client";

import { forwardRef, useId, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { DropdownMenu } from "../dropdown-menu/dropdown-menu";
import styles from "./select.module.css";

export interface SelectProps {
  portalContainer?: HTMLElement | null;
  "aria-label"?: string;
  label?: string; description?: string; placeholder?: string;
  id?: string; className?: string; disabled?: boolean; required?: boolean;
  name?: string; value?: string; defaultValue?: string;
  open?: boolean; defaultOpen?: boolean; onOpenChange?: (open: boolean) => void;
  onValueChange?: (value: string) => void;
  options: { value: string; label: string; disabled?: boolean; icon?: ReactNode }[];
}

/** Form field presentation backed by the installed UIArc Dropdown Menu.
 * Choice validation and allowed values come from the Core contract.
 */
export const Select = forwardRef<HTMLButtonElement, SelectProps>(function Select(
  { label, description, placeholder = "Select an option", options, id, className, disabled, required, name, value, defaultValue = "", onValueChange, portalContainer, "aria-label": ariaLabel, open, defaultOpen, onOpenChange }, ref,
) {
  const generatedId = useId(), controlId = id ?? generatedId;
  const field = useRef<HTMLDivElement>(null), trigger = useRef<HTMLButtonElement | null>(null);
  const [inner, setInner] = useState(defaultValue), [expanded, setExpanded] = useState(defaultOpen ?? false);
  const [inheritedDisabled, setInheritedDisabled] = useState(false);
  const current = value ?? inner, selected = options.find(option => option.value === current);
  useLayoutEffect(() => {
    const root = field.current;
    if (!root) return;
    const sync = () => {
      const isDisabled = root.closest("fieldset")?.disabled ?? false;
      setInheritedDisabled(isDisabled);
      if (isDisabled) setExpanded(false);
    };
    sync();
    const observer = new MutationObserver(sync);
    const fieldset = root.closest("fieldset");
    if (fieldset) observer.observe(fieldset, { attributes: true, attributeFilter: ["disabled"] });
    const reset = () => { if (value === undefined) setInner(defaultValue); };
    const form = root.closest("form");
    form?.addEventListener("reset", reset);
    return () => { observer.disconnect(); form?.removeEventListener("reset", reset); };
  }, [value, defaultValue]);
  const hintId = description ? `${controlId}-description` : undefined;
  return <div ref={field} className={`${styles.field} arc-dropdown-field`}>
    {label ? <label htmlFor={controlId}>{label}</label> : null}
    <DropdownMenu ref={node => { trigger.current = node; if (typeof ref === "function") ref(node); else if (ref) ref.current = node; }}
      id={controlId} className={[styles.trigger, className].filter(Boolean).join(" ")}
      label={selected?.label ?? placeholder} icon={selected?.icon} value={current}
      items={options} disabled={disabled || inheritedDisabled || !options.some(option => !option.disabled)}
      portalContainer={portalContainer} open={disabled || inheritedDisabled ? false : open ?? expanded} onOpenChange={next => { setExpanded(next); onOpenChange?.(next); }}
      triggerProps={{ "data-form-control": "", "aria-label": ariaLabel ?? label ?? (selected?.label || placeholder), "aria-describedby": hintId, "aria-required": required || undefined, "data-field-name": name } as any}
      onValueChange={next => { setInner(next); onValueChange?.(next); }} />
    {description && <span id={hintId} className={styles.hint}>{description}</span>}
  </div>;
});
