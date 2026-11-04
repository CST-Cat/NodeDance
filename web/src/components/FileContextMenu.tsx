import type { ReactNode } from "react";
import type { DropdownItem } from "./arc/dropdown-menu/dropdown-menu";
import { ContextMenu, ContextMenuTrigger, ContextMenuContent, ContextMenuItem } from "./ui/context-menu";
import { useFileContextGesture } from "./useFileContextGesture";

export default function FileContextMenu({ children, label, items, disabled, onOpen }: { children: ReactNode; label: string; items: DropdownItem[]; disabled?: boolean; onOpen: () => void }) {
  const gesture = useFileContextGesture({ disabled, onOpen });
  return <ContextMenu open={gesture.open} onOpenChange={gesture.onOpenChange}><ContextMenuTrigger {...gesture.triggerProps} disabled={disabled} asChild>{children}</ContextMenuTrigger><ContextMenuContent aria-label={label} className="file-context-menu registry-controls">{items.map(item => <ContextMenuItem key={item.label} disabled={item.disabled} variant={item.destructive ? "destructive" : "default"} onSelect={item.onSelect}>{item.label}</ContextMenuItem>)}</ContextMenuContent></ContextMenu>;
}
