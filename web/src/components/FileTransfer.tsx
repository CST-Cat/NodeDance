import { useEffect, useRef, useState } from "react";
import { Check } from "lucide-react";
import { useSignals } from "@preact/signals-react/runtime";
import { operationRequest, type AgentNode, type AgentNodesResponse, type NodeFileEntry } from "../api";
import { t } from "../i18n";
import type { FileManager, FileTransferProgress } from "../file-manager";
import { Button, Input, Select } from "./ManagementUI";
import { Dialog, DialogContent } from "./arc/dialog/dialog";
import { Alert } from "./arc/alert/alert";
import NodeFiles from "./NodeFiles";
import { Progress } from "./arc/progress/progress";
import { Tabs, TabsList, TabsTrigger } from "./arc/tabs/tabs";
import "./FileTransfer.css";

const x = (key: string) => t(`files.transfer.${key}`);
const size = (n: number) => n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(1)} MB`;

export default function FileTransfer({ sourceNodeId, files, manager, container, onClose }: { sourceNodeId: string; files: NodeFileEntry[]; manager: FileManager; container?: HTMLElement | null; onClose: () => void }) {
  useSignals();
  const [nodes, setNodes] = useState<AgentNode[]>([]);
  const [targetId, setTargetId] = useState(sourceNodeId);
  const [targetPath, setTargetPath] = useState("/");
  const [bandwidth, setBandwidth] = useState("0");
  const [mode, setMode] = useState("relay");
  const [transport, setTransport] = useState("");
  const [started, setStarted] = useState(false);
  const [progress, setProgress] = useState<Record<number, { completed: number; total: number; rate: number | null; phase: string }>>({});
  const samples = useRef(new Map<number, { completed: number; time: number }>());
  function updateProgress(items: FileTransferProgress[] | null) {
    if (!items) {
      samples.current.clear();
      setProgress(previous => Object.fromEntries(Object.entries(previous).map(([index, item]) => [index, { ...item, rate: null }])));
      return;
    }
    const now = performance.now();
    const next: typeof progress = {};
    for (const { index, task } of items) {
      const total = task.transfer?.file.size ?? task.progress.total;
      const completed = task.status === "succeeded" ? total : Math.min(total, task.progress.completed);
      const last = samples.current.get(index);
      const rate = last && now > last.time ? Math.max(0, completed - last.completed) * 1000 / (now - last.time) : 0;
      samples.current.set(index, { completed, time: now });
      const phase = task.status === "succeeded" ? "completed" : task.status === "running" || task.status === "queued" ? task.progress.phase === "verifying" ? files[index]?.kind === "directory" ? "unpacking" : "verifying" : completed > 0 ? "transferring" : "preparing" : task.status;
      next[index] = { completed, total, rate: phase === "transferring" ? rate : 0, phase };
    }
    setProgress(previous => ({ ...previous, ...next }));
  }
  const [picker, setPicker] = useState(false);
  const [error, setError] = useState(""), [busy, setBusy] = useState(false), [done, setDone] = useState(false);
  const pending = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    void operationRequest<AgentNodesResponse>("nodes.list", {}, { signal: controller.signal }).then(result => {
      setNodes(result.nodes);
      const other = result.nodes.find(node => node.nodeId !== sourceNodeId && node.status === "online");
      if (other) setTargetId(other.nodeId);
    }).catch(reason => { if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : String(reason)); });
    return () => { controller.abort(); pending.current?.abort(); };
  }, [sourceNodeId]);
  const targetNode = nodes.find(node => node.nodeId === targetId);
  const [lineTypes, setLineTypes] = useState<{ source: string; target: string }>({ source: "", target: "" });
  useEffect(() => {
    const controller = new AbortController();
    const fetchType = (nodeId: string) => operationRequest<{ lineType?: string }>("nodes.asset", { params: { nodeId } }, { signal: controller.signal }).then(asset => asset.lineType ?? "").catch(() => "");
    void Promise.all([fetchType(sourceNodeId), targetId && targetId !== sourceNodeId ? fetchType(targetId) : Promise.resolve("")]).then(([source, target]) => {
      if (!controller.signal.aborted) setLineTypes({ source, target });
    });
    return () => controller.abort();
  }, [sourceNodeId, targetId]);
  const relayHint = [lineTypes.source, lineTypes.target].filter(Boolean).length === 2 &&
    ([lineTypes.source, lineTypes.target].includes("homelab") && [lineTypes.source, lineTypes.target].every(value => value === "homelab" || value === "nat"));
  async function start() {
    const controller = new AbortController();
    pending.current = controller;
    setBusy(true); setError(""); setStarted(true); setProgress({}); samples.current.clear();
    try {
      const result = await manager.transfer(targetId, files.map(file => file.path), targetPath, controller.signal, Number(bandwidth || 0), mode, updateProgress);
      setProgress(previous => {
        const next = { ...previous };
        result.transferred.forEach(file => { const index = files.findIndex(item => item.path === file.sourcePath); if (index >= 0) next[index] = { completed: file.size, total: file.size, rate: 0, phase: "completed" }; });
        result.failed?.forEach(file => { const index = files.findIndex(item => item.path === file.sourcePath); if (index >= 0) next[index] = { completed: next[index]?.completed ?? 0, total: next[index]?.total ?? 0, rate: 0, phase: "failed" }; });
        return next;
      });
      if (result.failed?.length) setError(result.failed.map(failure => `${failure.sourcePath}: ${failure.error}`).join("; "));
      else {
        const transports = new Set(result.transferred.map(file => file.transport));
        setTransport(transports.size === 1 ? result.transferred[0]?.transport ?? "" : "");
        setDone(true);
      }
    } catch (reason) {
      if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      pending.current = null; setBusy(false);
      setProgress(previous => Object.fromEntries(Object.entries(previous).map(([index, item]) => [index, { ...item, rate: 0, phase: ["preparing", "transferring", "verifying", "unpacking"].includes(item.phase) ? "stopped" : item.phase }])));
    }
  }
  return <Dialog open onOpenChange={open => { if (!open && !busy) onClose(); }}><DialogContent container={container} title={x("title")} className="file-transfer-dialog">
    <div className="file-transfer">
      {error && <Alert tone="danger" title={error} />}
      <div className="file-transfer-field">
        <span className="file-transfer-label">{x("targetNode")}</span>
        <Select label="" aria-label={x("targetNode")} value={targetId} disabled={busy || done} onValueChange={value => { setTargetId(value); }} options={nodes.map(node => ({ value: node.nodeId, label: node.displayName }))} />
      </div>
      <div className="file-transfer-field">
        <span className="file-transfer-label">{x("targetDirectory")}</span>
        <div className="file-transfer-path"><Input label="" aria-label={x("targetDirectory")} value={targetPath} disabled={busy || done} onChange={event => setTargetPath(event.target.value)} /><Button type="button" variant="secondary" className="file-transfer-browse" disabled={busy || done || !targetNode} onClick={() => setPicker(true)}>{x("browse")}</Button></div>
      </div>
      <div className="file-transfer-options">
        <div className="file-transfer-field">
          <span className="file-transfer-label">{x("mode")}</span>
          <Tabs className="product-arc arc-ui" data-native-controls value={mode} onValueChange={setMode}>
            <TabsList aria-label={x("mode")}>
              <TabsTrigger value="relay" disabled={busy || done}>{x("modeRelay")}</TabsTrigger>
              <TabsTrigger value="direct" disabled={busy || done}>{x("modeDirect")}</TabsTrigger>
            </TabsList>
          </Tabs>
          {relayHint ? <span className="file-transfer-hint">{x("relayHint")}</span> : null}
        </div>
        <div className="file-transfer-field">
          <span className="file-transfer-label">{x("bandwidth")}</span>
          <Input label="" aria-label={x("bandwidth")} type="number" step="any" value={bandwidth} disabled={busy || done} onChange={event => setBandwidth(event.target.value)} />
        </div>
      </div>
      <div>
        <div className="file-transfer-files-head"><span>{x("files")}</span><span className="file-transfer-count">{files.length}</span></div>
        <ul className="file-transfer-files">
          {files.map((file, index) => {
            const item = progress[index];
            return <li key={file.path}>
              <div className="file-transfer-file-heading"><span className="file-transfer-name">{file.path}</span><span className="file-transfer-size">{file.kind === "directory" ? t("files.ui.folder") : size(file.size)}</span></div>
              {started && <div className="file-transfer-progress">
                <Progress label={item ? x(item.phase) : busy ? x("preparing") : x("stopped")} value={item?.phase === "completed" && item.total === 0 ? 1 : item?.completed ?? 0} max={item?.total || 1} showValue />
                <div className="file-transfer-progress-meta"><span>{size(item?.completed ?? 0)} / {item ? size(item.total) : file.kind === "file" ? size(file.size) : "—"}</span><span>{item?.rate === null ? "—" : ((item?.rate ?? 0) / 1000000).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 3 })} MB/s</span></div>
              </div>}
            </li>;
          })}
        </ul>
      </div>
      <div className="file-transfer-actions">
        {done && <span role="status">{x(transport === "rsync_direct" ? "direct" : transport === "rsync_relay" ? "relay" : transport === "chunk_relay" ? "chunks" : "completed")}</span>}
        {busy && !done && <Button variant="secondary" onClick={() => pending.current?.abort()}>{t("dialog.cancel")}</Button>}
        <Button onClick={done ? onClose : () => void start()} disabled={!done && (busy || !targetNode || !targetPath.trim())} loading={busy && !done} aria-label={done ? t("dialog.confirm") : undefined}>{done ? <Check size={18} /> : x("start")}</Button>
      </div>
    </div>
    {picker && targetNode && <NodeFiles nodeId={targetNode.nodeId} nodeName={targetNode.displayName} path="/" directoryPicker dialog onClose={() => setPicker(false)} onChoose={chosen => { setTargetPath(chosen); setPicker(false); }} />}
  </DialogContent></Dialog>;
}
