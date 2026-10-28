"use client";

import { forwardRef, useId, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode, SelectHTMLAttributes } from "react";
import { DropdownMenu } from "../dropdown-menu/dropdown-menu";
import styles from "./select.module.css";

export interface SelectProps {
  portalContainer?: HTMLElement | null;
  "aria-label"?: string;
  label?: string; description?: string; placeholder?: string;
  id?: string; className?: string; disabled?: boolean; required?: boolean;
  name?: string; form?: string; value?: string; defaultValue?: string;
  open?: boolean; defaultOpen?: boolean; onOpenChange?: (open: boolean) => void;
  onValueChange?: (value: string) => void;
  options: { value: string; label: string; disabled?: boolean; icon?: ReactNode }[];
  nativeProps?: SelectHTMLAttributes<HTMLSelectElement>;
  nativeChildren?: ReactNode;
}

/** Form field presentation backed by the installed UIArc Dropdown Menu.
 * The native select retains browser validation, labels, reset and submission.
 * Choice validation and allowed values still come from the Core contract.
 */
export const Select = forwardRef<HTMLButtonElement, SelectProps>(function Select(
  { label, description, placeholder = "Select an option", options, id, className, disabled, required, name, form, value, defaultValue = "", onValueChange, portalContainer, "aria-label": ariaLabel, nativeProps, nativeChildren, open, defaultOpen, onOpenChange }, ref,
) {
  const generatedId = useId(), controlId = id ?? generatedId;
  const source = useRef<HTMLSelectElement>(null), trigger = useRef<HTMLButtonElement | null>(null);
  const [inner, setInner] = useState(defaultValue), [expanded, setExpanded] = useState(defaultOpen ?? false);
  const [inheritedDisabled, setInheritedDisabled] = useState(false), [caption, setCaption] = useState("");
  const [invalid, setInvalid] = useState(false);
  const current = value ?? inner, selected = options.find(option => option.value === current);
  useLayoutEffect(() => {
    const native = source.current;
    if (!native) return;
    const sync = () => {
      const isDisabled = native.matches(":disabled");
      setInheritedDisabled(isDisabled);
      if (isDisabled) setExpanded(false);
      const owner = (native.labels?.[0] ?? native.closest("label"))?.cloneNode(true) as HTMLElement | undefined;
      owner?.querySelectorAll(".ui-select,.arc-dropdown-field").forEach(el => el.remove());
      setCaption(owner?.textContent?.trim() ?? "");
    };
    sync();
    const observer = new MutationObserver(sync);
    const fieldset = native.closest("fieldset");
    if (fieldset) observer.observe(fieldset, { attributes: true, attributeFilter: ["disabled"] });
    const reset = () => { setInvalid(false); if (value === undefined) setInner(defaultValue); };
    native.form?.addEventListener("reset", reset);
    return () => { observer.disconnect(); native.form?.removeEventListener("reset", reset); };
  }, [value, defaultValue, disabled, label, ariaLabel, nativeChildren]);
  const hintId = description ? `${controlId}-description` : nativeProps?.["aria-describedby"];
  return <div className={`${styles.field} arc-dropdown-field`}>
    {label ? <label htmlFor={controlId}>{label}</label> : null}
    <DropdownMenu ref={node => { trigger.current = node; if (typeof ref === "function") ref(node); else if (ref) ref.current = node; }}
      id={controlId} className={[styles.trigger, className].filter(Boolean).join(" ")}
      label={selected?.label ?? placeholder} icon={selected?.icon} value={current}
      items={options} disabled={disabled || inheritedDisabled || !options.some(option => !option.disabled)}
      portalContainer={portalContainer} open={disabled || inheritedDisabled ? false : open ?? expanded} onOpenChange={next => { setExpanded(next); onOpenChange?.(next); }}
      triggerProps={{ "data-form-control": "", "aria-label": ariaLabel ?? label ?? (caption || selected?.label || placeholder), "aria-describedby": hintId, "aria-invalid": invalid || undefined, "data-field-name": name } as any}
      onValueChange={next => {
        setInvalid(false); setInner(next);
        if (source.current && nativeProps?.onChange) {
          source.current.value = next;
          source.current.dispatchEvent(new Event("change", { bubbles: true }));
        } else onValueChange?.(next);
      }} />
    <select {...nativeProps} ref={source} id={nativeProps?.id ?? `${controlId}-source`} name={name ?? nativeProps?.name} form={form ?? nativeProps?.form} disabled={disabled ?? nativeProps?.disabled} required={required ?? nativeProps?.required} value={current} tabIndex={-1} aria-hidden="true" className="dropdown-native-source"
      onChange={event => { setInner(event.target.value); onValueChange?.(event.target.value); nativeProps?.onChange?.(event); }}
      onInvalid={event => { event.preventDefault(); setInvalid(true); trigger.current?.focus(); nativeProps?.onInvalid?.(event); }}>
      {nativeChildren ?? options.map(option => <option key={option.value} value={option.value} disabled={option.disabled}>{option.label}</option>)}
    </select>
    {description && <span id={hintId} className={styles.hint}>{description}</span>}
  </div>;
});
