import { useEffect, useRef, useState } from "react";
import type { NodeFileEntry } from "../api";
import { locale, t } from "../i18n";

export interface GridAction { action: string; paths: string[]; destination?: string; cut?: boolean }
export default function ElFinderGrid({ path, rows, selection, busy, canPaste, hidden, onSelection, onAction, onEscape }: { path: string; rows: NodeFileEntry[]; selection: string[]; busy: boolean; canPaste: boolean; hidden: boolean; onSelection: (paths: string[]) => void; onAction: (action: GridAction) => void; onEscape: () => void }) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [mounted, setMounted] = useState(false);
  const handlers = useRef({ onSelection, onAction, onEscape }); handlers.current = { onSelection, onAction, onEscape };
  const data = JSON.stringify({ path, rows, language: locale.value, shareLabel: t("shares.share") });
  const send = (value: Record<string, unknown>) => frame.current?.contentWindow?.postMessage({ channel: "nodedance-elfinder-grid", ...value }, location.origin);
  useEffect(() => {
    const receive = (event: MessageEvent) => {
      if (event.source !== frame.current?.contentWindow || event.origin !== location.origin || event.data?.channel !== "nodedance-elfinder-grid") return;
      if (event.data.type === "mounted") setMounted(true);
      else if (event.data.type === "selection") handlers.current.onSelection(event.data.paths);
      else if (event.data.type === "action") handlers.current.onAction(event.data);
      else if (event.data.type === "escape") handlers.current.onEscape();
    };
    addEventListener("message", receive);
    return () => removeEventListener("message", receive);
  }, []);
  useEffect(() => { if (mounted) send({ type: "data", value: { ...JSON.parse(data), busy, canPaste } }); }, [mounted, data]);
  useEffect(() => { if (mounted) send({ type: "selection", paths: selection }); }, [mounted, hidden, JSON.stringify(selection), data]);
  useEffect(() => { if (mounted) send({ type: "status", busy, canPaste }); }, [mounted, busy, canPaste]);
  useEffect(() => { if (mounted) send({ type: "visibility", value: !hidden }); }, [mounted, hidden]);
  return <iframe ref={frame} className="file-browser-native-grid" title={t("files.ui.grid")} src="/vendor/elfinder-grid/" sandbox="allow-scripts allow-same-origin" hidden={hidden} />;
}
