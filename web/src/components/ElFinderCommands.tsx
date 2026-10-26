import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import type { NodeFileEntry } from "../api";
import { locale } from "../i18n";

export type FileCommand = { action: string; paths: string[] };
export type FileCommands = { execute: (action: string, paths: string[]) => void };
export default forwardRef<FileCommands, { path: string; rows: NodeFileEntry[]; onAction: (command: FileCommand) => void }>(function ElFinderCommands({ path, rows, onAction }, ref) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [mounted, setMounted] = useState(false);
  const ready = useRef(false), queue = useRef<FileCommand[]>([]);
  const handler = useRef(onAction); handler.current = onAction;
  const data = JSON.stringify({ path, rows, language: locale.value });
  const send = (value: Record<string, unknown>) => frame.current?.contentWindow?.postMessage({ channel: "nodedance-elfinder-commands", ...value }, location.origin);
  useImperativeHandle(ref, () => ({ execute(action, paths) { const command = { action, paths }; if (ready.current) send({ type: "command", ...command }); else queue.current.push(command); } }), []);
  useEffect(() => {
    function receive(event: MessageEvent) {
      if (event.source !== frame.current?.contentWindow || event.origin !== location.origin || event.data?.channel !== "nodedance-elfinder-commands") return;
      if (event.data.type === "mounted") setMounted(true);
      else if (event.data.type === "ready") { ready.current = true; for (const command of queue.current.splice(0)) send({ type: "command", ...command }); }
      else if (event.data.type === "action") handler.current(event.data);
    }
    addEventListener("message", receive);
    return () => removeEventListener("message", receive);
  }, []);
  useEffect(() => { if (mounted) { ready.current = false; send({ type: "data", value: JSON.parse(data) }); } }, [mounted, data]);
  return <iframe ref={frame} title="File commands" src="/vendor/elfinder-commands/" sandbox="allow-scripts allow-same-origin" hidden aria-hidden="true" />;
});
