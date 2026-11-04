import { useEffect, useRef, useState, type HTMLAttributes } from "react";

type Options = {
  disabled?: boolean;
  onOpen: (target: HTMLElement) => void;
  resolveTarget?: (target: Element, trigger: HTMLElement) => HTMLElement | null;
};

export function useFileContextGesture({ disabled = false, onOpen, resolveTarget = (_target, trigger) => trigger }: Options) {
  const [open, setOpen] = useState(false);
  const target = useRef<HTMLElement | null>(null);
  const origin = useRef<{ id: number; x: number; y: number } | null>(null);
  const consumeClick = useRef(false);
  useEffect(() => {
    if (disabled) { setOpen(false); origin.current = null; consumeClick.current = false; }
  }, [disabled]);
  const onOpenChange = (next: boolean) => {
    if (next) {
      if (disabled || !target.current?.isConnected) return;
      consumeClick.current = origin.current !== null;
      onOpen(target.current);
    }
    setOpen(next);
  };
  const triggerProps: HTMLAttributes<HTMLElement> = {
    onContextMenuCapture: event => { target.current = event.target instanceof Element ? resolveTarget(event.target, event.currentTarget) : null; },
    onPointerDown: event => {
      consumeClick.current = false;
      target.current = event.target instanceof Element ? resolveTarget(event.target, event.currentTarget) : null;
      origin.current = !disabled && event.isPrimary && (event.pointerType === "touch" || event.pointerType === "pen") && target.current ? { id: event.pointerId, x: event.clientX, y: event.clientY } : null;
    },
    onPointerMove: event => {
      const start = origin.current;
      if (!start || start.id !== event.pointerId) return;
      if ((event.clientX - start.x) ** 2 + (event.clientY - start.y) ** 2 <= 100) event.preventDefault();
      else origin.current = null;
    },
    onPointerUp: () => { origin.current = null; },
    onPointerCancel: () => { origin.current = null; target.current = null; consumeClick.current = false; },
    onClickCapture: event => {
      if (consumeClick.current && event.detail > 0) { consumeClick.current = false; event.preventDefault(); event.stopPropagation(); }
    },
    onDragStartCapture: event => { if (origin.current) event.preventDefault(); },
  };
  return { open, onOpenChange, triggerProps };
}
