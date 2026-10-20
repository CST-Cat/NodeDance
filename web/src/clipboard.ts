import { beforeDispose, cell } from "./state";

export type CopyState = "idle" | "pending" | "success" | "error";

// One clipboard path for SSH, enrollment commands and host information.
// The fallback preserves focus and selection, including inside a native dialog.
export async function copyToClipboard(text: string): Promise<void> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return;
    }
  } catch { /* Try the browser's user-gesture clipboard path. */ }
  const active = document.activeElement as HTMLElement | null;
  const selection = window.getSelection();
  const ranges = selection ? Array.from({ length: selection.rangeCount }, (_, i) => selection.getRangeAt(i).cloneRange()) : [];
  const input = document.createElement("textarea");
  input.value = text;
  input.readOnly = true;
  input.style.cssText = "position:fixed;left:-9999px;top:0";
  (active?.closest("dialog[open]") ?? document.body).append(input);
  try {
    input.select();
    if (!document.execCommand("copy")) throw new Error("clipboard unavailable");
  } finally {
    input.remove();
    if (active?.isConnected) active.focus({ preventScroll: true });
    selection?.removeAllRanges();
    for (const range of ranges) selection?.addRange(range);
  }
}

export function clipboardFeedback() {
  const state = cell<CopyState>("idle");
  let timer: ReturnType<typeof setTimeout> | undefined;
  let revision = 0;
  function reset() {
    revision++;
    if (timer) clearTimeout(timer);
    state.value = "idle";
  }
  beforeDispose(reset);
  return {
    state, reset,
    async copy(text: string): Promise<boolean> {
      reset();
      const current = revision;
      state.value = "pending";
      let copied = false;
      try { await copyToClipboard(text); copied = true; } catch { /* Expose failure through the same control. */ }
      if (current === revision) {
        state.value = copied ? "success" : "error";
        timer = setTimeout(() => { state.value = "idle"; }, 2500);
      }
      return copied;
    },
  };
}
