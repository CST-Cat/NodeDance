import { useRef, type CSSProperties, type ReactNode } from "react";
import "./UiSegments.css";

// A shared, rectangular choice group. No IDs or layout animation state leak
// between the many independent VPS controls on the same page.
export default function UiSegments<T extends string>({ value, options, onChange, label, className = "" }: {
  value: T; options: { value: T; label: ReactNode }[];
  onChange: (value: T) => void; label: string; className?: string;
}) {
  const buttons = useRef<(HTMLButtonElement | null)[]>([]);
  const index = Math.max(0, options.findIndex(option => option.value === value));
  return <div role="group" aria-label={label} className={`ui-segments ${className}`} style={{ "--segment-count": options.length, "--segment-index": index } as CSSProperties}>
    <span className="ui-segments-indicator" aria-hidden="true" />
    {options.map((option, i) => <button key={option.value} type="button" ref={element => { buttons.current[i] = element; }}
      aria-pressed={option.value === value} tabIndex={i === index ? 0 : -1}
      onClick={() => onChange(option.value)} onKeyDown={event => {
        const rtl = getComputedStyle(event.currentTarget).direction === "rtl";
        const offset = event.key === "ArrowRight" ? (rtl ? -1 : 1) : event.key === "ArrowLeft" ? (rtl ? 1 : -1) : 0;
        const next = event.key === "Home" ? 0 : event.key === "End" ? options.length - 1 : offset ? (i + offset + options.length) % options.length : -1;
        if (next < 0) return;
        event.preventDefault(); onChange(options[next]!.value); buttons.current[next]?.focus({ preventScroll: true });
      }}>{option.label}</button>)}
  </div>;
}
