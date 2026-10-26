import { cloneElement, isValidElement, useEffect, useId, useRef, useState } from "react";
import { useSignals } from "@preact/signals-react/runtime";
import { operationRequest } from "../api";
import { operationURL } from "../operations";
import { locale, t } from "../i18n";
import { Button, Input, ManagementPage, Select } from "./ManagementUI";
import { Dialog, DialogContent } from "./arc/dialog/dialog";
import { PasswordField } from "./arc/password-field/password-field";
import { CopyButton } from "./arc/copy-button/copy-button";
import { SettingsGroup } from "./SettingsGroup";
import { Alert } from "./arc/alert/alert";
import { Card, CardContent } from "./ui/card";
import "./FileShares.css";
import FileRequests from "./FileRequests";
import { FileUpload } from "./arc/file-upload/file-upload";
import { Tabs, TabsList, TabsTrigger } from "./arc/tabs/tabs";

const s = (key: string) => t(`shares.${key}`);
export const shareIntent = () => Array.from(crypto.getRandomValues(new Uint8Array(32)), b => b.toString(16).padStart(2, "0")).join("");
export type SharedEntry = { path: string; size: number; directory?: boolean };
export type FileShare = { id: string; name: string; nodeId?: string; paths?: string[]; entries: SharedEntry[]; size: number; createdAt: number; expiresAt: number; maxDownloads: number; downloads: number; passwordProtected: boolean; revision: number; revoked: boolean };
const link = (v: FileShare) => `${location.origin}/s/${v.id}`;
const size = (n: number) => n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(1)} MB`;
export function ShareSettingRow({ label, children }: { label: string; children: React.ReactNode }) { const id=useId();const field=isValidElement<{id?:string}>(children)?cloneElement(children,{id:children.props.id??id}):children;return <div className="share-setting-row"><label htmlFor={isValidElement<{id?:string}>(children)?children.props.id??id:undefined}>{label}</label><div>{field}</div></div>; }

export default function FileShares({ nodeId, paths, container, onClose }: { nodeId: string; paths: string[]; container?: HTMLElement | null; onClose: () => void }) {
  useSignals();
  const [tab, setTab] = useState("shares");
 const [uploading, setUploading]=useState(paths.length===0);
 const [items, setItems] = useState<FileShare[]>([]), [current, setCurrent] = useState<FileShare>(), [creating, setCreating] = useState(paths.length > 0);
  const [name, setName] = useState(paths.length === 1 ? paths[0]!.split("/").pop()! : ""), [days, setDays] = useState("7"), [downloads, setDownloads] = useState("0"), [password, setPassword] = useState(""), [replaceId, setReplaceId] = useState("");
  const [passwordTouched, setPasswordTouched] = useState(false), [error, setError] = useState(""), [busy, setBusy] = useState(false), [qr, setQr] = useState("");
  const intent = useRef(shareIntent()), pending = useRef<AbortController | null>(null);
  async function refresh(signal?: AbortSignal) { const result = await operationRequest<{ shares: FileShare[] }>("shares.list", {}, { signal }); setItems(result.shares); }
  useEffect(() => { const controller = new AbortController(); void refresh(controller.signal).catch(e => { if (!controller.signal.aborted) setError(String(e)); }); return () => { controller.abort(); pending.current?.abort(); }; }, []);
  function select(v: FileShare) { setUploading(false); setCurrent(v); setCreating(false); setName(v.name); setDays(String(v.expiresAt ? Math.max(1, Math.ceil((v.expiresAt - Date.now() / 1000) / 86400)) : 0)); setDownloads(String(v.maxDownloads)); setPassword(""); setPasswordTouched(false); setReplaceId(""); setQr(""); intent.current = shareIntent(); }
  async function run(action: (signal: AbortSignal) => Promise<void>) { const controller = new AbortController(); pending.current = controller; setBusy(true); setError(""); try { await action(controller.signal); } catch (e) { if (!controller.signal.aborted) setError(String(e)); } finally { pending.current = null; setBusy(false); } }
  async function save() { await run(async signal => { const v = await operationRequest<FileShare>(creating ? "shares.create" : "shares.update", creating ? {} : { params: { id: current!.id } }, { signal, body: JSON.stringify({ requestId: intent.current, nodeId, paths: creating ? paths : [], name, expiryDays: Number(days), maxDownloads: Number(downloads), ...(creating || passwordTouched ? { password } : {}), ...(current && !creating ? { revision: current.revision } : {}), ...(replaceId ? { replaceId } : {}) }) }, 30 * 60_000); select(v); await refresh(signal); }); }
  const uploadIntents=useRef(new WeakMap<File,Map<string,string>>());
  async function upload(file: File, signal?: AbortSignal) { const key=JSON.stringify([days,downloads,password]);let intents=uploadIntents.current.get(file);if(!intents){intents=new Map();uploadIntents.current.set(file,intents);}let requestId=intents.get(key);if(!requestId){requestId=shareIntent();intents.set(key,requestId);}const v=await operationRequest<FileShare>("shares.upload",{query:{name:file.name,expiryDays:days,maxDownloads:downloads,requestId}},{body:file,signal,headers:{"Content-Type":"application/octet-stream","X-Share-Password":password}},30*60_000);setCurrent(v);setCreating(false);setName(v.name);await refresh(signal); }
  async function showQR() { if (!current) return; await run(async () => { const QR = await import("qrcode"); setQr(await QR.toDataURL(link(current), { width: 240, margin: 2, color: { dark: "#000000", light: "#ffffff" } })); }); }
  const active = current && !current.revoked;
  return <Dialog open onOpenChange={open => { if (!open) onClose(); }}><DialogContent container={container} title={s("title")} className="file-shares-dialog"><div className="file-shares">
    <Tabs value={tab} onValueChange={setTab}><TabsList><TabsTrigger value="shares">{s("title")}</TabsTrigger><TabsTrigger value="requests">{s("requests")}</TabsTrigger></TabsList></Tabs>
 {tab === "requests" ? <FileRequests /> : <>
 {error && <Alert tone="danger" title={error} />}
    <Button variant="secondary" onClick={() => {setCreating(true);setUploading(true);setCurrent(undefined);intent.current=shareIntent();}}>{s("upload")}</Button>
    {(creating || current) && <>
      <SettingsGroup>
        <ShareSettingRow label={s("name")}><Input label="" aria-label={s("name")} value={name} disabled={busy || !!current?.revoked} onChange={e => setName(e.target.value)} /></ShareSettingRow>
        <ShareSettingRow label={s("days")}><Input label="" aria-label={s("days")} type="number" min={0} value={days} disabled={busy || !!current?.revoked} onChange={e => setDays(e.target.value)} /></ShareSettingRow>
        <ShareSettingRow label={s("downloads")}><Input label="" aria-label={s("downloads")} type="number" min={0} value={downloads} disabled={busy || !!current?.revoked} onChange={e => setDownloads(e.target.value)} /></ShareSettingRow>
        <ShareSettingRow label={s("password")}><PasswordField label="" aria-label={s("password")} autoComplete="new-password" value={password} disabled={busy || !!current?.revoked} placeholder={current?.passwordProtected && !passwordTouched ? s("keepPassword") : s("optional")} onChange={e => { setPassword(e.target.value); setPasswordTouched(true); }} /></ShareSettingRow>
        {!creating && active && <ShareSettingRow label={s("replace")}><Select label="" aria-label={s("replace")} value={replaceId} onValueChange={setReplaceId} options={[{ value: "", label: s("unchanged") }, ...items.filter(v => v.id !== current.id && !v.revoked).map(v => ({ value: v.id, label: v.name }))]} /></ShareSettingRow>}
      </SettingsGroup>
      {uploading && <div onPaste={e => {const files=Array.from(e.clipboardData.files);if(files.length){e.preventDefault();void run(async signal=>{for(const file of files)await upload(file,signal);});}}}><FileUpload label={s("upload")} onUpload={(file,options)=>upload(file,options.signal)} /></div>}
      <div className="share-actions">{(creating && paths.length > 0 && !uploading || active) && <Button disabled={busy} onClick={() => void save()}>{creating ? s("create") : s("save")}</Button>}{busy && <Button variant="secondary" onClick={() => pending.current?.abort()}>{s("cancel")}</Button>}{active && <Button variant="secondary" disabled={busy} onClick={() => void run(async signal => { await operationRequest("shares.revoke", { params: { id: current.id }, query: { revision: String(current.revision) } }, { signal }); setCurrent(undefined); await refresh(signal); })}>{s("revoke")}</Button>}</div>
      {active && <>
        <div className="share-link"><Input label={s("link")} readOnly value={link(current)} /><CopyButton value={link(current)} label={s("copy")} /></div>
        <div className="share-actions"><Button variant="secondary" disabled={busy} onClick={() => void showQR()}>{s("qr")}</Button><Button variant="secondary" onClick={() => { location.href = `mailto:?subject=${encodeURIComponent(current.name)}&body=${encodeURIComponent(link(current))}`; }}>{s("email")}</Button>{typeof navigator.share === "function" && <Button variant="secondary" onClick={() => void navigator.share({ title: current.name, url: link(current) }).catch(e => setError(String(e)))}>{s("systemShare")}</Button>}</div>
        {qr && <img src={qr} width={240} height={240} alt={s("qr")} />}
        {current.entries.filter(e => !e.directory).map(e => <div className="share-file-row" key={e.path}><span>{e.path}</span><Button variant="secondary" onClick={() => void run(async () => {const anchor=document.createElement("a");anchor.href=String(await operationURL("shares.download",{params:{id:current.id},query:{path:e.path}}));anchor.download=e.path.split("/").pop()!;document.body.append(anchor);anchor.click();anchor.remove();})}>{s("download")}</Button>{!current.passwordProtected && <Button variant="secondary" onClick={() => void run(async () => { await navigator.clipboard.writeText(String(await operationURL("shares.hotlink", { params: { id: current.id }, query: { path: e.path } }))); })}>{s("hotlink")}</Button>}</div>)}
        <p>{s("count").replace("{0}", String(current.downloads))}</p>
      </>}
    </>}
    <SettingsGroup>{items.length ? items.map(v => <div className="share-file-row" key={v.id}><Button variant="ghost" onClick={() => void run(async signal => {select(await operationRequest<FileShare>("shares.get",{params:{id:v.id}},{signal}));})}>{v.name}</Button><span>{v.revoked ? s("revoked") : v.expiresAt && v.expiresAt <= Date.now() / 1000 || v.maxDownloads > 0 && v.downloads >= v.maxDownloads ? s("expired") : s("active")}</span><span>{size(v.size)}</span></div>) : <p>{s("empty")}</p>}</SettingsGroup>
  </>}</div></DialogContent></Dialog>;
}

export function PublicFileShare() {
  useSignals();
  const id = location.pathname.split("/")[2] ?? "";
  const [value, setValue] = useState<FileShare>(), [password, setPassword] = useState(""), [unlocked, setUnlocked] = useState(false), [error, setError] = useState(""), [busy, setBusy] = useState(false), [directory, setDirectory] = useState("");
  const controller = useRef<AbortController | null>(null);
  useEffect(() => { const abort = new AbortController(); void operationRequest<FileShare>("shares.public", { params: { id } }, { signal: abort.signal }).then(setValue).catch(e => { if (!abort.signal.aborted) setError(String(e)); }); return () => { abort.abort(); controller.current?.abort(); }; }, [id]);
  async function unlock() { setBusy(true); setError(""); try { setValue(await operationRequest<FileShare>("shares.unlock", { params: { id } }, { body: JSON.stringify({ password }) })); setUnlocked(true); } catch (e) { setError(String(e)); } finally { setBusy(false); } }
  async function download(entry: SharedEntry) { setBusy(true); setError(""); const abort = new AbortController(); controller.current = abort; try { const { operation, path } = await import("../operations").then(m => m.resolveOperation("shares.receive", { params: { id }, query: { path: entry.path } })); const response = await fetch(path, { method: operation.method, credentials: "omit", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ password }), signal: abort.signal }); if (!response.ok) throw Error(s("unavailable")); const blob = await response.blob(), url = URL.createObjectURL(blob), anchor = document.createElement("a"); anchor.href = url; anchor.download = entry.path.split("/").pop()!; document.body.append(anchor); anchor.click(); anchor.remove(); setTimeout(() => URL.revokeObjectURL(url), 60_000); } catch (e) { if (!abort.signal.aborted) setError(String(e)); } finally { setBusy(false); controller.current = null; } }
  const rows = value?.entries.filter(e => e.path.split("/").slice(0, -1).join("/") === directory) ?? [];
  return <ManagementPage registryStyles className="public-file-share"><Card><CardContent><div className="file-shares"><h1>{value?.name || s("title")}</h1>{error && <Alert tone="danger" title={error} />}{value?.passwordProtected && !unlocked ? <form onSubmit={e => { e.preventDefault(); void unlock(); }}><PasswordField label={s("password")} value={password} onChange={e => setPassword(e.target.value)} autoComplete="off" /><Button type="submit" disabled={busy}>{s("unlock")}</Button></form> : value && <><div className="share-file-row"><span>{directory || "/"}</span>{directory && <Button variant="secondary" onClick={() => setDirectory(directory.split("/").slice(0, -1).join("/"))}>{s("back")}</Button>}</div><SettingsGroup>{rows.map(entry => <div className="share-file-row" key={entry.path}><span>{entry.path.split("/").pop()}</span><span>{entry.directory ? s("folder") : size(entry.size)}</span><Button disabled={busy} onClick={() => entry.directory ? setDirectory(entry.path) : void download(entry)}>{entry.directory ? s("open") : s("download")}</Button></div>)}</SettingsGroup><p>{value.expiresAt ? new Date(value.expiresAt * 1000).toLocaleString(locale.value) : s("noExpiry")}</p>{busy && <Button variant="secondary" onClick={() => controller.current?.abort()}>{s("cancel")}</Button>}</>}</div></CardContent></Card></ManagementPage>;
}
