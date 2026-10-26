import { Fragment, useLayoutEffect, useRef, useState } from "react";
import { useSignals } from "@preact/signals-react/runtime";
import { locale, t } from "../i18n";
import { Button } from "./ManagementUI";
import { Button as FileButton } from "./ui/button";
import { Breadcrumb, BreadcrumbList, BreadcrumbItem, BreadcrumbLink, BreadcrumbSeparator } from "./ui/breadcrumb";
import { DropdownMenu } from "./arc/dropdown-menu/dropdown-menu";

const f = (key: string) => t(`files.ui.${key}`);
export function FileNameText({ name }: { name: string }) {
  const chars = Array.from(name), split = chars.length > 12 ? chars.length - Math.min(8, Math.floor(chars.length / 2)) : chars.length;
  return <span className="file-name-text" title={name}><span className="file-name-start">{chars.slice(0, split).join("")}</span><span className="file-name-end"><span>{chars.slice(split).join("")}</span></span></span>;
}

export function FileAddressBreadcrumb({ path, portal, onNavigate, onEdit }: { path: string; portal: HTMLElement | null; onNavigate: (path: string) => void; onEdit: () => void }) {
  useSignals();
  const labels = ["/", ...path.split("/").filter(Boolean)];
  const host = useRef<HTMLDivElement>(null), measure = useRef<HTMLOListElement>(null), ellipsis = useRef<HTMLSpanElement>(null), space = useRef<HTMLButtonElement>(null);
  const [fold, setFold] = useState<{ path: string; start: number; end: number } | null>(null);
  const activeFold = fold?.path === path ? fold : null;
  useLayoutEffect(() => {
    const container = host.current, list = measure.current, probe = ellipsis.current, blank = space.current;
    if (!container || !list || !probe || !blank) return;
    const update = () => {
      const items = [...list.querySelectorAll<HTMLElement>("[data-crumb-index]")];
      if (items.length !== labels.length) return;
      const widths = items.map(item => item.getBoundingClientRect().width);
      const gap = parseFloat(getComputedStyle(list).columnGap) || 0;
      const separator = list.querySelector<HTMLElement>('[data-slot="breadcrumb-separator"]')?.getBoundingClientRect().width ?? 0;
      const step = separator + gap * 2;
      const style = getComputedStyle(container);
      const available = container.clientWidth - (parseFloat(style.paddingLeft) || 0) - (parseFloat(style.paddingRight) || 0) - (parseFloat(style.columnGap) || 0) - (parseFloat(getComputedStyle(blank).flexBasis) || 0);
      const total = widths.reduce((sum, width) => sum + width, 0) + (widths.length - 1) * step;
      let next: { path: string; start: number; end: number } | null = null;
      if (total > available + .5 && widths.length > 2) {
        const middle = Math.floor((widths.length - 1) / 2), menuWidth = probe.getBoundingClientRect().width;
        let best = -Infinity, balance = Infinity;
        next = { path, start: 1, end: widths.length - 2 };
        for (let start = 1; start <= middle; start++) for (let end = middle; end < widths.length - 1; end++) {
          const hidden = widths.slice(start, end + 1).reduce((sum, width) => sum + width, 0);
          const used = total - hidden + menuWidth + (start - end) * step;
          const distance = Math.abs(start - (widths.length - 1 - end));
          if (used <= available + .5 && (used > best + .5 || (Math.abs(used - best) <= .5 && distance < balance))) { best = used; balance = distance; next = { path, start, end }; }
        }
      }
      setFold(previous => previous?.path === next?.path && previous?.start === next?.start && previous?.end === next?.end ? previous : next);
    };
    const observer = new ResizeObserver(update);
    observer.observe(container); observer.observe(list); observer.observe(probe);
    update();
    document.fonts.addEventListener("loadingdone", update);
    return () => { observer.disconnect(); document.fonts.removeEventListener("loadingdone", update); };
  }, [path, locale.value]);
  const crumb = (index: number, measuring = false) => {
    const current = index === labels.length - 1;
    return <BreadcrumbItem key={index} data-crumb-index={index} className={current && !measuring ? "min-w-0" : "shrink-0"}>{current ? <FileButton type="button" size="sm" variant="ghost" className="file-browser-address-current" aria-label={f("editPath")} aria-current="page" data-address-edit={measuring ? undefined : true} onClick={measuring ? undefined : onEdit}><FileNameText name={labels[index]!} /></FileButton> : <BreadcrumbLink asChild><FileButton type="button" size="sm" variant="ghost" onClick={measuring ? undefined : () => onNavigate(index ? "/" + labels.slice(1, index + 1).join("/") : "/")}>{labels[index]}</FileButton></BreadcrumbLink>}</BreadcrumbItem>;
  };
  const menu = (measuring = false) => <DropdownMenu label="…" triggerProps={{ "aria-label": f("parentDirectories") }} portalContainer={portal} items={measuring || !activeFold ? [] : labels.slice(activeFold.start, activeFold.end + 1).map((label, index) => ({ label, onSelect: () => onNavigate("/" + labels.slice(1, activeFold.start + index + 1).join("/")) }))} />;
  const shown = labels.map((_, index) => index).filter(index => !activeFold || index < activeFold.start || index > activeFold.end);
  const parts: React.ReactNode[] = [];
  for (const index of shown) {
    if (parts.length) parts.push(<BreadcrumbSeparator key={`separator-${index}`} />);
    if (activeFold && index === activeFold.end + 1) parts.push(<BreadcrumbItem key="folded">{menu()}</BreadcrumbItem>, <BreadcrumbSeparator key="after-folded" />);
    parts.push(crumb(index));
  }
  return <div ref={host} className="file-browser-breadcrumb">
    <div className="file-browser-address-measure" aria-hidden="true" inert><Breadcrumb><BreadcrumbList ref={measure} className="flex-nowrap w-max">{labels.map((_, index) => <Fragment key={index}>{index > 0 && <BreadcrumbSeparator />}{crumb(index, true)}</Fragment>)}</BreadcrumbList></Breadcrumb><span ref={ellipsis}>{menu(true)}</span></div>
    <Breadcrumb aria-label={f("path")}><BreadcrumbList className="flex-nowrap">{parts}</BreadcrumbList></Breadcrumb>
    <Button ref={space} type="button" size="sm" variant="ghost" className="file-browser-address-space" aria-hidden="true" tabIndex={-1} onClick={onEdit} />
  </div>;
}
