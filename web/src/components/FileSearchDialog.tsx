import { useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { useSignals } from "@preact/signals-react/runtime";
import { File, Folder, PanelRight, Pin, PinOff, Search } from "lucide-react";
import type { NodeFileEntry } from "../api";
import { type FileManager } from "../file-manager";
import { locale, t } from "../i18n";
import { Button, Input, Select } from "./ManagementUI";
import { Button as FileButton } from "./ui/button";
import { Dialog, DialogContent } from "./arc/dialog/dialog";
import { SortableDataTable, type DataColumn, type SortState } from "./arc/sortable-data-table/sortable-data-table";
import { Slider } from "./arc/slider/slider";
import { Pagination } from "./arc/pagination/pagination";
import { Alert } from "./arc/alert/alert";
import { EmptyState } from "./arc/empty-state/empty-state";
import { Skeleton } from "./arc/skeleton/skeleton";
import { ScrollArea } from "./arc/scroll-area/scroll-area";
import { ContextMenu, ContextMenuTrigger, ContextMenuContent, ContextMenuItem } from "./ui/context-menu";
import { FilePathCopy, FileAddressBreadcrumb, FileNameText } from "./FilePath";
import FilePreview from "./FilePreview";

const f = (key: string) => t(`files.ui.${key}`);
const s = (key: string) => t(`files.search.${key}`);
const bytes = (value: number) => value < 1024 ? `${value} B` : value < 1048576 ? `${(value / 1024).toFixed(1)} KB` : `${(value / 1048576).toFixed(1)} MB`;
function category(file: NodeFileEntry) {
  if (file.kind === "directory") return "folder";
  if (/\.(png|jpe?g|webp|gif|bmp|avif|svg|ico)$/i.test(file.name)) return "image";
  if (/\.(mp4|webm|mp3|ogg|wav|flac|mkv|mov)$/i.test(file.name)) return "media";
  if (/\.(zip|tar|gz|tgz|bz2|tbz2|xz|7z|rar)$/i.test(file.name)) return "archive";
  if (/\.(txt|sh|ya?ml|jsonc?|toml|ini|conf|env|service|md|log|py|js|ts|html|css|xml)$/i.test(file.name) || /^(Dockerfile|Makefile|\.env)$/i.test(file.name)) return "text";
  return "file";
}

type Props = { open: boolean; onOpenChange: (open: boolean) => void; manager: FileManager; path: string; portal: HTMLElement | null; onOpenFile: (file: NodeFileEntry) => void; onReveal: (file: NodeFileEntry) => void; onAction: (action: string, file: NodeFileEntry) => void; onPickDirectory: (choose: (path: string) => void) => void; children?: ReactNode };

export default function FileSearchDialog({ open, onOpenChange, manager, path, portal, onOpenFile, onReveal, onAction, onPickDirectory, children }: Props) {
  useSignals();
  const [query, setQuery] = useState(""), [minimum, setMinimum] = useState(""), [maximum, setMaximum] = useState("");
  const [scope, setScope] = useState("current"), [root, setRoot] = useState(path), [rootDraft, setRootDraft] = useState(path), [editingRoot, setEditingRoot] = useState(false);
  const [rows, setRows] = useState<NodeFileEntry[]>([]), [selection, setSelection] = useState<string[]>([]), [kind, setKind] = useState("all");
  const [sort, setSort] = useState<SortState>({ key: "name", direction: "asc" });
  const [page, setPage] = useState(1), [busy, setBusy] = useState(false), [error, setError] = useState(""), [searched, setSearched] = useState(false);
  const [preview, setPreview] = useState(false), [pinned, setPinned] = useState(false), [fontSize, setFontSize] = useState(14), [width, setWidth] = useState(700);
  const controller = useRef<AbortController | null>(null), input = useRef<HTMLInputElement>(null);
  const [results, setResults] = useState<HTMLDivElement | null>(null);
  useEffect(() => {
    controller.current?.abort(); setBusy(false);
    if (open) { setScope("current"); setRoot(path); setRootDraft(path); setRows([]); setSelection([]); setSearched(false); setError(""); setEditingRoot(false); setPage(1); }
    return () => controller.current?.abort();
  }, [open, manager]);
  useEffect(() => {
    const node = results;
    if (!node) return;
    const observer = new ResizeObserver(([entry]) => { if (entry) setWidth(entry.contentRect.width); });
    observer.observe(node); return () => observer.disconnect();
  }, [results]);
  function changeRoot(next: string, source = "custom") {
    controller.current?.abort(); setBusy(false); setRoot(next); setRootDraft(next); setScope(source); setRows([]); setSelection([]); setSearched(false); setEditingRoot(false); setError(""); setPage(1);
  }
  function changeScope(value: string) {
    if (value === "folder") onPickDirectory(chosen => changeRoot(chosen));
    else changeRoot(value === "machine" ? "/" : path, value);
  }
  async function search() {
    controller.current?.abort(); const request = new AbortController(); controller.current = request;
    setBusy(true); setError(""); setSearched(true);
    try {
      const found = await manager.search(root, query, { ...(minimum ? { minSize: minimum } : {}), ...(maximum ? { maxSize: maximum } : {}) }, request.signal);
      if (request.signal.aborted) return;
      setRows(found); setSelection([]); setPage(1);
    } catch (reason) { if (!request.signal.aborted) setError(String(reason)); }
    finally { if (controller.current === request && !request.signal.aborted) setBusy(false); }
  }
  const filtered = useMemo(() => rows.filter(row => kind === "all" || (kind === "file" ? row.kind === "file" : category(row) === kind)).sort((a, b) => {
    const left = a[sort.key as keyof NodeFileEntry], right = b[sort.key as keyof NodeFileEntry];
    const order = typeof left === "number" && typeof right === "number" ? left - right : String(left ?? "").localeCompare(String(right ?? ""), locale.value, { numeric: true });
    return sort.direction === "asc" ? order : -order;
  }), [rows, kind, sort, locale.value]);
  const shown = filtered.slice((page - 1) * 50, page * 50);
  const selected = rows.find(row => row.path === selection.at(-1));
  function act(action: string, file: NodeFileEntry) {
    if (!pinned) onOpenChange(false);
    if (action === "open") onOpenFile(file);
    else if (action === "reveal") onReveal(file);
    else onAction(action, file);
  }
  function name(file: NodeFileEntry) {
    return <FileButton type="button" variant="ghost" size="sm" className="file-browser-name-button" data-search-file={file.path} title={file.path} onContextMenuCapture={() => setSelection([file.path])} onClick={() => setSelection([file.path])} onDoubleClick={() => act("open", file)} onKeyDown={event => { if (event.key === "Enter") { event.preventDefault(); act("open", file); } }}>{file.kind === "directory" ? <Folder size={16} /> : <File size={16} />}<FileNameText name={file.name} /></FileButton>;
  }
  const columns: DataColumn<NodeFileEntry>[] = [{ key: "name", label: f("name"), sortable: true, render: (_value, file) => name(file) }, ...(width > 440 ? [{ key: "size", label: f("size"), width: 100, sortable: true, render: (_value: unknown, file: NodeFileEntry) => file.kind === "directory" ? "—" : bytes(file.size) }, { key: "modifiedAt", label: f("modified"), width: 170, sortable: true, render: (value: unknown) => new Date(Number(value) / 1e6).toLocaleString(locale.value) }] : [])];
  return <Dialog open={open} modal={!pinned} onOpenChange={onOpenChange}><DialogContent container={portal} title={s("title")} className="product-arc arc-ui registry-controls file-search-dialog" data-native-controls onOpenAutoFocus={event => { event.preventDefault(); input.current?.focus(); input.current?.select(); }} onInteractOutside={event => { if (pinned) event.preventDefault(); }} onEscapeKeyDown={event => { if (event.target instanceof Element && (event.target.closest('.cm-editor')?.querySelector('.cm-search, .cm-tooltip-autocomplete') || event.target.closest('[role="menu"]'))) event.preventDefault(); }}>
    <div className="file-search" data-testid="file-search" data-pinned={pinned} style={{ "--file-search-font-size": `${fontSize}px` } as CSSProperties}>
      <form className="file-search-query" onSubmit={event => { event.preventDefault(); event.stopPropagation(); void search(); }}><Input ref={input} aria-label={f("search")} placeholder={f("search")} value={query} onChange={event => setQuery(event.target.value)} /><Button type="submit" variant="secondary" aria-label={f("search")} title={f("search")} loading={busy}><Search size={18} /></Button><Select aria-label={s("scope")} value={scope} onValueChange={changeScope} options={[{ value: "current", label: s("current") }, { value: "machine", label: s("machine") }, ...(scope === "custom" ? [{ value: "custom", label: s("selectedFolder") }] : []), { value: "folder", label: s("chooseFolder") }]} /></form>
      <div className="file-search-scope-path">{editingRoot ? <form onSubmit={event => { event.preventDefault(); event.stopPropagation(); changeRoot(rootDraft); }}><Input aria-label={f("path")} value={rootDraft} required autoFocus onChange={event => setRootDraft(event.target.value)} onKeyDown={event => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); setEditingRoot(false); } }} /><Button type="submit" variant="secondary">{f("go")}</Button></form> : <FileAddressBreadcrumb path={root} portal={portal} onNavigate={next => changeRoot(next)} onEdit={() => { setRootDraft(root); setEditingRoot(true); }} />}</div>
      <form className="file-search-filters" onSubmit={event => { event.preventDefault(); event.stopPropagation(); void search(); }}><Select aria-label={s("kind")} value={kind} onValueChange={value => { setKind(value); setPage(1); setSelection([]); }} options={["all", "file", "folder", "text", "image", "media", "archive"].map(value => ({ value, label: s(value) }))} /><Input aria-label={f("minSize")} placeholder={f("minSize")} value={minimum} onChange={event => setMinimum(event.target.value)} /><Input aria-label={f("maxSize")} placeholder={f("maxSize")} value={maximum} onChange={event => setMaximum(event.target.value)} /></form>
      {error && <Alert tone="danger" title={error} />}
      <div className="file-search-body" data-preview={preview}><ContextMenu><ContextMenuTrigger asChild><div ref={setResults} className="file-search-results" onContextMenuCapture={event => { const item = event.target instanceof Element ? event.target.closest("tr")?.querySelector<HTMLElement>("[data-search-file]") : null; if (item?.dataset.searchFile) setSelection([item.dataset.searchFile]); else if (!selected) event.preventDefault(); }}>{busy && !rows.length ? <Skeleton label={t("load.initial")} lines={8} /> : !searched ? <EmptyState title={s("start")} description="" /> : <SortableDataTable fillHeight selectable rows={shown.map(row => ({ ...row }))} rowKey="path" columns={columns} sort={sort} onSortChange={next => { setSort(next); setPage(1); }} selectedKeys={selection} onSelectionChange={setSelection} itemName={{ one: f("items"), other: f("items") }} emptyMessage={s("empty")} />}</div></ContextMenuTrigger><ContextMenuContent className="file-context-menu">{selected && <><ContextMenuItem onSelect={() => act("open", selected)}>{f("open")}</ContextMenuItem><ContextMenuItem onSelect={() => act("reveal", selected)}>{s("reveal")}</ContextMenuItem><ContextMenuItem disabled={selected.kind !== "file"} onSelect={() => act("edit", selected)}>{f("edit")}</ContextMenuItem><ContextMenuItem disabled={selected.kind !== "file"} onSelect={() => act("download", selected)}>{f("download")}</ContextMenuItem><ContextMenuItem onSelect={() => act("info", selected)}>{f("info")}</ContextMenuItem></>}</ContextMenuContent></ContextMenu>{preview && <ScrollArea className="file-search-preview" label={f("preview")} fade={0}>{selected ? <><h3><FileNameText name={selected.name} /></h3>{selected.kind === "directory" ? <dl className="file-info"><dt>{f("permissions")}</dt><dd>{selected.mode.toString(8)}</dd><dt>{f("modified")}</dt><dd>{new Date(selected.modifiedAt / 1e6).toLocaleString(locale.value)}</dd></dl> : <FilePreview file={selected} manager={manager} />}</> : <EmptyState title={s("selectPreview")} description="" />}</ScrollArea>}</div>
      <footer className="file-search-footer"><div className="file-search-selection-path"><FileAddressBreadcrumb path={selected?.path ?? root} portal={portal} onNavigate={next => { if (selected) act("open", { ...selected, path: next, kind: "directory" }); else changeRoot(next); }} onEdit={() => { if (selected) act("reveal", selected); else setEditingRoot(true); }} /><FilePathCopy path={selected?.path ?? root} /></div><div className="file-search-controls"><Pagination page={page} pageCount={Math.ceil(filtered.length / 50)} onPageChange={setPage} label={f("pages")} /><Slider label={f("fontSize")} value={fontSize} min={12} max={22} step={1} onValueChange={setFontSize} className="file-search-font" /><Button type="button" variant={preview ? "primary" : "secondary"} aria-label={s("preview")} title={s("preview")} aria-pressed={preview} onClick={() => setPreview(value => !value)}><PanelRight size={18} /></Button><Button type="button" variant={pinned ? "primary" : "secondary"} aria-label={pinned ? s("unpin") : s("pin")} title={pinned ? s("unpin") : s("pin")} aria-pressed={pinned} onClick={() => setPinned(value => !value)}>{pinned ? <PinOff size={18} /> : <Pin size={18} />}</Button></div></footer>
    </div>{children}
  </DialogContent></Dialog>;
}
