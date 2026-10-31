import { useEffect, useRef, useState } from "react";
import { Check } from "lucide-react";
import { useSignals } from "@preact/signals-react/runtime";
import { operationRequest, type AgentNode, type AgentNodesResponse, type NodeFileEntry } from "../api";
import { t } from "../i18n";
import { fileParent } from "../file-manager";
import type { FileManager } from "../file-manager";
import { Button, Input, Select } from "./ManagementUI";
import { Dialog, DialogContent } from "./arc/dialog/dialog";
import { Alert } from "./arc/alert/alert";
import NodeFiles from "./NodeFiles";
import "./FileTransfer.css";

const x = (key: string) => t(`files.transfer.${key}`);
const size = (n: number) => n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(1)} MB`;

export default function FileTransfer({ sourceNodeId, files, manager, container, onClose }: { sourceNodeId: string; files: NodeFileEntry[]; manager: FileManager; container?: HTMLElement | null; onClose: () => void }) {
  useSignals();
  const [nodes, setNodes] = useState<AgentNode[]>([]);
  const [targetId, setTargetId] = useState(sourceNodeId);
  const [targetPath, setTargetPath] = useState(files[0] ? fileParent(files[0].path) : "/");
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
  async function start() {
    const controller = new AbortController();
    pending.current = controller;
    setBusy(true); setError("");
    try {
      const result = await manager.transfer(targetId, files.map(file => file.path), targetPath, controller.signal);
      if (result.failed?.length) setError(result.failed.map(failure => `${failure.sourcePath}: ${failure.error}`).join("; "));
      else setDone(true);
    } catch (reason) {
      if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      pending.current = null; setBusy(false);
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
      <div>
        <div className="file-transfer-files-head"><span>{x("files")}</span><span className="file-transfer-count">{files.length}</span></div>
        <ul className="file-transfer-files">
          {files.map(file => <li key={file.path}><span className="file-transfer-name">{file.path}</span><span className="file-transfer-size">{size(file.size)}</span></li>)}
        </ul>
      </div>
      <div className="file-transfer-actions">
        {busy && !done && <Button variant="secondary" onClick={() => pending.current?.abort()}>{t("dialog.cancel")}</Button>}
        <Button onClick={done ? onClose : () => void start()} disabled={!done && (busy || !targetNode || !targetPath.startsWith("/"))} loading={busy && !done} aria-label={done ? t("dialog.confirm") : undefined}>{done ? <Check size={18} /> : x("start")}</Button>
      </div>
    </div>
    {picker && targetNode && <NodeFiles nodeId={targetNode.nodeId} nodeName={targetNode.displayName} path={targetPath} directoryPicker dialog onClose={() => setPicker(false)} onChoose={chosen => { setTargetPath(chosen); setPicker(false); }} />}
  </DialogContent></Dialog>;
}
