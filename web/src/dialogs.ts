import { signal } from "@preact/signals-react";

export interface DialogChoice {
  value: string;
  label: string;
  detail?: string;
}

interface DialogRequest {
  kind: "confirm" | "prompt" | "choice";
  message: string;
  value: string;
  choices?: DialogChoice[];
  confirmLabel?: string;
  resolve: (value: string | null) => void;
}
export const activeDialog = signal<DialogRequest | null>(null);
const queue: DialogRequest[] = [];
function request(
  kind: DialogRequest["kind"],
  message: string,
  value = "",
  choices?: DialogChoice[],
  confirmLabel?: string,
) {
  return new Promise<string | null>((resolve) => {
    const item = { kind, message, value, choices, confirmLabel, resolve };
    if (activeDialog.value) queue.push(item);
    else activeDialog.value = item;
  });
}
export const requestPrompt = (message: string, value = "") =>
  request("prompt", message, value);
export const requestChoice = (
  message: string,
  choices: DialogChoice[],
  value = "",
  confirmLabel?: string,
) => request("choice", message, value, choices, confirmLabel);
export const requestConfirm = async (message: string) =>
  (await request("confirm", message)) !== null;
export function finishDialog(value: string | null) {
  const current = activeDialog.value;
  activeDialog.value = queue.shift() ?? null;
  current?.resolve(value);
}
export function cancelDialogs() {
  const pending = [activeDialog.value, ...queue];
  queue.length = 0;
  activeDialog.value = null;
  pending.forEach((item) => item?.resolve(null));
}
