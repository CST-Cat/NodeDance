import { useEffect, useRef, useState } from "react";
import { useSignals } from "@preact/signals-react/runtime";
import { operationRequest } from "../api";
import { operationURL } from "../operations";
import { t } from "../i18n";
import { Button, Input, ManagementPage } from "./ManagementUI";
import { CopyButton } from "./arc/copy-button/copy-button";
import { FileUpload } from "./arc/file-upload/file-upload";
import { Alert } from "./arc/alert/alert";
import { Card, CardContent } from "./ui/card";
import { SettingsGroup } from "./SettingsGroup";
import { shareIntent, ShareSettingRow, type SharedEntry } from "./FileShares";
const s = (key: string) => t(`shares.${key}`);
type FileRequest = { id: string; name: string; notes: string; expiresAt: number; maxFiles: number; maxBytes: number; entries: SharedEntry[]; revision: number; revoked: boolean; uploadedFiles: number };

export default function FileRequests() {
  useSignals();
  const [items, setItems] = useState<FileRequest[]>([]), [current, setCurrent] = useState<FileRequest>();
  const [name, setName] = useState(""), [notes, setNotes] = useState(""), [days, setDays] = useState("7"), [files, setFiles] = useState("10"), [bytes, setBytes] = useState("1073741824"), [error, setError] = useState(""), [busy, setBusy] = useState(false);
  const intent = useRef(shareIntent());
  async function refresh(signal?: AbortSignal) { const v = await operationRequest<{ requests: FileRequest[] }>("share-requests.list", {}, { signal }); setItems(v.requests); }
  useEffect(() => { const controller = new AbortController(); void refresh(controller.signal).catch(e => { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e)); }); return () => controller.abort(); }, []);
  function select(v?: FileRequest) { setCurrent(v); setName(v?.name ?? ""); setNotes(v?.notes ?? ""); setFiles(String(v?.maxFiles ?? 10)); setBytes(String(v?.maxBytes ?? 1073741824)); setDays(v ? String(v.expiresAt ? Math.max(1, Math.ceil((v.expiresAt - Date.now() / 1000) / 86400)) : 0) : "7"); intent.current = shareIntent(); }
  async function run(action: () => Promise<void>) { setBusy(true); setError(""); try { await action(); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } finally { setBusy(false); } }
  async function download(entry: SharedEntry) { if (!current) return; const anchor = document.createElement("a"); anchor.href = String(await operationURL("share-requests.download", { params: { id: current.id }, query: { path: entry.path } })); anchor.download = entry.path; document.body.append(anchor); anchor.click(); anchor.remove(); }
  return <div className="file-shares">{error && <Alert tone="danger" title={error} />}<SettingsGroup>
    <ShareSettingRow label={s("name")}><Input label="" aria-label={s("name")} value={name} onChange={e => setName(e.target.value)} /></ShareSettingRow>
    <ShareSettingRow label={s("notes")}><Input label="" aria-label={s("notes")} value={notes} onChange={e => setNotes(e.target.value)} /></ShareSettingRow>
    <ShareSettingRow label={s("days")}><Input label="" aria-label={s("days")} type="number" min={0} value={days} onChange={e => setDays(e.target.value)} /></ShareSettingRow>
    <ShareSettingRow label={s("maxFiles")}><Input label="" aria-label={s("maxFiles")} type="number" min={0} value={files} onChange={e => setFiles(e.target.value)} /></ShareSettingRow>
    <ShareSettingRow label={s("maxBytes")}><Input label="" aria-label={s("maxBytes")} type="number" min={0} value={bytes} onChange={e => setBytes(e.target.value)} /></ShareSettingRow>
  </SettingsGroup><div className="share-actions"><Button disabled={busy || !!current?.revoked} onClick={() => void run(async () => { const v = await operationRequest<FileRequest>(current ? "share-requests.update" : "share-requests.create", current ? { params: { id: current.id } } : {}, { body: JSON.stringify({ requestId: intent.current, name, notes, expiryDays: Number(days), maxFiles: Number(files), maxBytes: Number(bytes), ...(current ? { revision: current.revision } : {}) }) }); select(v); await refresh(); })}>{current ? s("save") : s("createRequest")}</Button><Button variant="secondary" onClick={() => select()}>{s("newRequest")}</Button>{current && !current.revoked && <Button disabled={busy} variant="secondary" onClick={() => void run(async () => { await operationRequest("share-requests.revoke", { params: { id: current.id }, query: { revision: String(current.revision) } }); select(); await refresh(); })}>{s("revoke")}</Button>}</div>
  {current && !current.revoked && <><div className="share-link"><Input label={s("link")} readOnly value={`${location.origin}/r/${current.id}`} /><CopyButton value={`${location.origin}/r/${current.id}`} label={s("copy")} /></div><SettingsGroup>{current.entries.map(entry => <div key={entry.path} className="share-file-row"><span>{entry.path}</span><span>{entry.size} B</span><Button variant="secondary" onClick={() => void run(() => download(entry))}>{s("download")}</Button></div>)}</SettingsGroup></>}
  <Button variant="secondary" disabled={busy} onClick={() => void run(async () => { await refresh(); if (current) select(await operationRequest<FileRequest>("share-requests.get", { params: { id: current.id } })); })}>{s("refresh")}</Button>
  <SettingsGroup>{items.map(v => <div className="share-file-row" key={v.id}><Button variant="ghost" onClick={() => void run(async () => select(await operationRequest<FileRequest>("share-requests.get",{params:{id:v.id}})))}>{v.name}</Button><span>{v.uploadedFiles}</span><span>{v.revoked ? s("revoked") : v.expiresAt && v.expiresAt <= Date.now() / 1000 ? s("expired") : s("active")}</span></div>)}</SettingsGroup></div>;
}

export function PublicFileRequest() {
  useSignals();
  const id = location.pathname.split("/")[2] ?? "", [value, setValue] = useState<FileRequest>(), [error, setError] = useState("");
  useEffect(() => { const controller = new AbortController(); void operationRequest<FileRequest>("share-requests.public", { params: { id } }, { signal: controller.signal }).then(setValue).catch(e => { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e)); }); return () => controller.abort(); }, [id]);
  return <ManagementPage registryStyles className="public-file-share"><Card><CardContent><div className="file-shares"><h1>{value?.name || s("requests")}</h1>{error && <Alert tone="danger" title={error} />}{value && <><p>{value.notes}</p><FileUpload label={s("upload")} maxSize={value.maxBytes || undefined} onUpload={async (file, { signal }) => { const { operation, path } = await import("../operations").then(m => m.resolveOperation("share-requests.upload", { params: { id }, query: { name: file.name } })); const r = await fetch(path, { method: operation.method, credentials: "omit", headers: { "Content-Type": "application/octet-stream" }, body: file, signal }); if (!r.ok) throw Error(s("uploadFailed")); }} /></>}</div></CardContent></Card></ManagementPage>;
}
