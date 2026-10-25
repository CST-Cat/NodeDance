import { createPortal } from "react-dom";
import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useSignals } from "@preact/signals-react/runtime";
import { ArrowLeft, ArrowRight, ArrowUp, Folder, File, RefreshCw, Download, Upload, Search, Maximize } from "lucide-react";
import { createFileManager, fileJoin, fileParent, type FileManager, type FileObservation } from "../file-manager";
import type { NodeFileEntry } from "../api";
import { operationCatalog } from "../operations";
import { locale, t } from "../i18n";
import { ManagementPage, Button, Input, Checkbox, Select } from "./ManagementUI";
import { Dialog, DialogContent } from "./arc/dialog/dialog";
import { DropdownMenu, type DropdownItem } from "./arc/dropdown-menu/dropdown-menu";
import { ContextMenu, ContextMenuTrigger, ContextMenuContent, ContextMenuItem } from "./ui/context-menu";
import { SortableDataTable, type SortState, type DataColumn } from "./arc/sortable-data-table/sortable-data-table";
import { TreeView, type TreeNode } from "./arc/tree-view/tree-view";
import { FileUpload } from "./arc/file-upload/file-upload";
import { Pagination } from "./arc/pagination/pagination";
import SegmentedControl from "./arc/segmented-control/segmented-control";
import { Textarea } from "./arc/textarea/textarea";
import { Alert } from "./arc/alert/alert";
import { Slider } from "./arc/slider/slider";
import { EmptyState } from "./arc/empty-state/empty-state";
import { ScrollArea } from "./arc/scroll-area/scroll-area";
import { Skeleton } from "./arc/skeleton/skeleton";
import { Button as FileButton } from "./ui/button";
import { Breadcrumb, BreadcrumbList, BreadcrumbItem, BreadcrumbLink, BreadcrumbSeparator } from "./ui/breadcrumb";
import { CopyButton } from "./arc/copy-button/copy-button";
import FileImageEditor from "./FileImageEditor";
import ElFinderGrid, { type GridAction } from "./ElFinderGrid";
import "./NodeFiles.css";

export const preloadFileManager = operationCatalog;
const f = (key: string) => t(`files.ui.${key}`);
const imageFile = (path: string) => /\.(png|jpe?g|webp|gif|bmp)$/i.test(path);
const size = (n: number) => n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(1)} MB`;
type Props = { nodeId: string; nodeName: string; path?: string; directoryPicker?: boolean; dialog?: boolean; onClose?: () => void; onPathChange?: (path: string) => void; onDirectoryReady?: (path: string) => void; onChoose?: (path: string) => void; [key: string]: unknown };
type FileModal = { kind: string; files: NodeFileEntry[] };
function FileNameText({ name }: { name: string }) {
  const chars = Array.from(name), split = chars.length > 12 ? chars.length - Math.min(8, Math.floor(chars.length / 2)) : chars.length;
  return <span className="file-name-text" title={name}><span className="file-name-start">{chars.slice(0, split).join("")}</span><span className="file-name-end"><span>{chars.slice(split).join("")}</span></span></span>;
}

function FileAddressBreadcrumb({ path, portal, onNavigate, onEdit }: { path: string; portal: HTMLElement | null; onNavigate: (path: string) => void; onEdit: () => void }) {
  useSignals();
  const labels = ["/", ...path.split("/").filter(Boolean)];
  const host = useRef<HTMLDivElement>(null), measure = useRef<HTMLOListElement>(null), ellipsis = useRef<HTMLSpanElement>(null), space = useRef<HTMLButtonElement>(null);
  const [fold, setFold] = useState<{ path: string; start: number; end: number } | null>(null);
  const activeFold = fold?.path === path ? fold : null;
  useLayoutEffect(() => {
    const container = host.current, list = measure.current, probe = ellipsis.current, blank = space.current;
    if (!container || !list || !probe || !blank) return;
    const update = () => {
      const items = [...list.querySelectorAll<HTMLElement>("[data-crumb-index]")];
      if (items.length !== labels.length) return;
      const widths = items.map(item => item.getBoundingClientRect().width);
      const gap = parseFloat(getComputedStyle(list).columnGap) || 0;
      const separator = list.querySelector<HTMLElement>('[data-slot="breadcrumb-separator"]')?.getBoundingClientRect().width ?? 0;
      const step = separator + gap * 2;
      const style = getComputedStyle(container);
      const available = container.clientWidth - (parseFloat(style.paddingLeft) || 0) - (parseFloat(style.paddingRight) || 0) - (parseFloat(style.columnGap) || 0) - (parseFloat(getComputedStyle(blank).flexBasis) || 0);
      const total = widths.reduce((sum, width) => sum + width, 0) + (widths.length - 1) * step;
      let next: { path: string; start: number; end: number } | null = null;
      if (total > available + .5 && widths.length > 2) {
        const middle = Math.floor((widths.length - 1) / 2), menuWidth = probe.getBoundingClientRect().width;
        let best = -Infinity, balance = Infinity;
        next = { path, start: 1, end: widths.length - 2 };
        for (let start = 1; start <= middle; start++) for (let end = middle; end < widths.length - 1; end++) {
          const hidden = widths.slice(start, end + 1).reduce((sum, width) => sum + width, 0);
          const used = total - hidden + menuWidth + (start - end) * step;
          const distance = Math.abs(start - (widths.length - 1 - end));
          if (used <= available + .5 && (used > best + .5 || (Math.abs(used - best) <= .5 && distance < balance))) { best = used; balance = distance; next = { path, start, end }; }
        }
      }
      setFold(previous => previous?.path === next?.path && previous?.start === next?.start && previous?.end === next?.end ? previous : next);
    };
    const observer = new ResizeObserver(update);
    observer.observe(container); observer.observe(list); observer.observe(probe);
    update();
    document.fonts.addEventListener("loadingdone", update);
    return () => { observer.disconnect(); document.fonts.removeEventListener("loadingdone", update); };
  }, [path, locale.value]);
  const crumb = (index: number, measuring = false) => {
    const current = index === labels.length - 1;
    return <BreadcrumbItem key={index} data-crumb-index={index} className={current && !measuring ? "min-w-0" : "shrink-0"}>{current ? <FileButton type="button" size="sm" variant="ghost" className="file-browser-address-current" aria-label={f("editPath")} aria-current="page" data-address-edit={measuring ? undefined : true} onClick={measuring ? undefined : onEdit}><FileNameText name={labels[index]!} /></FileButton> : <BreadcrumbLink asChild><FileButton type="button" size="sm" variant="ghost" onClick={measuring ? undefined : () => onNavigate(index ? "/" + labels.slice(1, index + 1).join("/") : "/")}>{labels[index]}</FileButton></BreadcrumbLink>}</BreadcrumbItem>;
  };
  const menu = (measuring = false) => <DropdownMenu label="…" triggerProps={{ "aria-label": f("parentDirectories") }} portalContainer={portal} items={measuring || !activeFold ? [] : labels.slice(activeFold.start, activeFold.end + 1).map((label, index) => ({ label, onSelect: () => onNavigate("/" + labels.slice(1, activeFold.start + index + 1).join("/")) }))} />;
  const shown = labels.map((_, index) => index).filter(index => !activeFold || index < activeFold.start || index > activeFold.end);
  const parts: React.ReactNode[] = [];
  for (const index of shown) {
    if (parts.length) parts.push(<BreadcrumbSeparator key={`separator-${index}`} />);
    if (activeFold && index === activeFold.end + 1) parts.push(<BreadcrumbItem key="folded">{menu()}</BreadcrumbItem>, <BreadcrumbSeparator key="after-folded" />);
    parts.push(crumb(index));
  }
  return <div ref={host} className="file-browser-breadcrumb">
    <div className="file-browser-address-measure" aria-hidden="true" inert><Breadcrumb><BreadcrumbList ref={measure} className="flex-nowrap w-max">{labels.map((_, index) => <Fragment key={index}>{index > 0 && <BreadcrumbSeparator />}{crumb(index, true)}</Fragment>)}</BreadcrumbList></Breadcrumb><span ref={ellipsis}>{menu(true)}</span></div>
    <Breadcrumb aria-label={f("path")}><BreadcrumbList className="flex-nowrap">{parts}</BreadcrumbList></Breadcrumb>
    <Button ref={space} type="button" size="sm" variant="ghost" className="file-browser-address-space" aria-hidden="true" tabIndex={-1} onClick={onEdit} />
  </div>;
}


export default function NodeFiles(props: Props) {
  useSignals();
  const [container, setContainer] = useState<HTMLElement | null>(null);
  const returnFocus = useRef(document.activeElement instanceof HTMLElement ? document.activeElement : null);
  const view = <FileBrowser key={props.nodeId} {...props} portal={container} />;
  const content = <ManagementPage embedded registryStyles className="file-browser-boundary">
    {createPortal(<div ref={setContainer} className="product-arc arc-ui registry-controls file-browser-portals" />, document.querySelector(".app-shell") ?? document.body)}
    {props.dialog ? <Dialog open onOpenChange={open => { if (!open) props.onClose?.(); }}>{container && <DialogContent container={container} title={`${f("title")} · ${props.nodeName}`} onEscapeKeyDown={event => { if (document.activeElement?.hasAttribute("data-address-input")) event.preventDefault(); }} onCloseAutoFocus={event => { event.preventDefault(); if (returnFocus.current?.isConnected) returnFocus.current.focus({ preventScroll: true }); }} className="file-browser-dialog">{view}</DialogContent>}</Dialog> : view}
  </ManagementPage>;
  return props.dialog ? createPortal(content, document.querySelector(".app-shell") ?? document.body) : content;
}

function FileBrowser({ nodeId, nodeName, path: initialPath = "/", directoryPicker, onPathChange, onDirectoryReady, onChoose, portal }: Props & { portal: HTMLElement | null }) {
  useSignals();
  const [manager, setManager] = useState<FileManager>();
  const [path, setPath] = useState(initialPath);
  const [editingPath, setEditingPath] = useState(false);
  const addressForm = useRef<HTMLFormElement>(null);
  const [pathDraft, setPathDraft] = useState(initialPath), [imageDirty, setImageDirty] = useState(false);
  const [entries, setEntries] = useState<NodeFileEntry[]>([]);
  const [tree, setTree] = useState<Record<string, NodeFileEntry[]>>({});
  const [confirmedPath, setConfirmedPath] = useState("/");
  const treeRequests = useRef(new Set<string>());
  const [expanded, setExpanded] = useState<string[]>(["/"]);
  const [selection, setSelection] = useState<string[]>([]);
  const [history, setHistory] = useState({ paths: [initialPath], index: 0 });
  const [clipboard, setClipboard] = useState<{ paths: string[]; cut: boolean }>();
  const [loading, setLoading] = useState(true), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [query, setQuery] = useState(""), [minSize, setMinSize] = useState(""), [maxSize, setMaxSize] = useState("");
  const [page, setPage] = useState(1), [view, setView] = useState("list"), [showHidden, setShowHidden] = useState(true);
  const [paneWidth, setPaneWidth] = useState(600);
  const filePane = useRef<HTMLDivElement>(null);
  const fileBody = useRef<HTMLDivElement>(null), footer = useRef<HTMLElement>(null);
  const [paneHeight, setPaneHeight] = useState(240);
  const [sort, setSort] = useState<SortState>({ key: "name", direction: "asc" });
  const [modal, setModal] = useState<FileModal>(), [draft, setDraft] = useState(""), [destination, setDestination] = useState("");
  const [extractFolder, setExtractFolder] = useState(false);
  const [format, setFormat] = useState("zip"), [modalError, setModalError] = useState(""), [modalLoading, setModalLoading] = useState(false);
  const [text, setText] = useState(""), [original, setOriginal] = useState(""), [observations, setObservations] = useState<FileObservation[]>([]);
  const [discard, setDiscard] = useState(false), [fullscreen, setFullscreen] = useState(false);
  const modalEpoch = useRef(0), modalReturn = useRef<HTMLElement | null>(null);
  const root = useRef<HTMLDivElement>(null), generation = useRef(0), lifecycle = useRef(0);
  const pathsRef = useRef({ path, query, minSize, maxSize }); pathsRef.current = { path, query, minSize, maxSize };
  const propsRef = useRef({ onPathChange, onDirectoryReady }); propsRef.current = { onPathChange, onDirectoryReady };
  useEffect(() => {
    let disposed = false; let client: FileManager | undefined;
    lifecycle.current++; generation.current++; setLoading(true); setError(""); setManager(undefined); setEntries([]); setTree({}); setSelection([]); setModal(undefined); setClipboard(undefined); setPath(initialPath); setPathDraft(initialPath); setHistory({ paths: [initialPath], index: 0 }); setQuery("");
    void createFileManager(nodeId, items => { if (!disposed) setObservations(items); }).then(value => {
      client = value; if (disposed) value.dispose(); else setManager(value);
    }).catch(reason => { if (!disposed) { setError(String(reason)); setLoading(false); } });
    return () => { disposed = true; lifecycle.current++; generation.current++; client?.dispose(); propsRef.current.onDirectoryReady?.(""); };
  }, [nodeId]);
  const load = useCallback(async (target: string, filter = "", limits: Record<string, string> = {}) => {
    if (!manager) return;
    const epoch = ++generation.current; setLoading(true); setError("");
    try {
      const info = await manager.stat(target);
      if (info.kind !== "directory") throw Error(t("files.notDirectory"));
      const rows = filter || Object.keys(limits).length ? await manager.search(target, filter, limits) : await manager.list(target);
      if (generation.current !== epoch) return;
      setEntries(rows); setSelection([]); setPage(1); setConfirmedPath(target);
      const chain: string[] = ["/"]; let ancestor = target; while (ancestor !== "/") { chain.push(ancestor); ancestor = fileParent(ancestor); } setExpanded(old => [...new Set([...old, ...chain])]);
      if (!filter && !Object.keys(limits).length) setTree(old => ({ ...old, [target]: rows.filter(row => row.kind === "directory") }));
      propsRef.current.onDirectoryReady?.(target);
    } catch (reason) { if (generation.current === epoch) { setError(String(reason)); propsRef.current.onDirectoryReady?.(""); } }
    finally { if (generation.current === epoch) setLoading(false); }
  }, [manager]);
  useEffect(() => { if (manager) void load(path); }, [manager]);
  useEffect(() => {
    if (manager && !directoryPicker && initialPath !== pathsRef.current.path) { setPath(initialPath); setPathDraft(initialPath); setQuery(""); void load(initialPath); }
  }, [initialPath]);
  useEffect(() => {
    const pane = filePane.current;
    if (!pane) return;
    const observer = new ResizeObserver(([entry]) => { if (entry) setPaneWidth(entry.contentRect.width); });
    observer.observe(pane);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (!editingPath) return;
    const cancel = (event: PointerEvent) => { if (event.target instanceof Node && !addressForm.current?.contains(event.target)) setEditingPath(false); };
    document.addEventListener("pointerdown", cancel);
    return () => document.removeEventListener("pointerdown", cancel);
  }, [editingPath]);
  function editPath() { setPathDraft(path); setEditingPath(true); }
  function finishPathEditing() {
    setEditingPath(false);
    requestAnimationFrame(() => root.current?.querySelector<HTMLElement>("[data-address-edit]")?.focus({ preventScroll: true }));
  }
  useLayoutEffect(() => {
    const body = fileBody.current, container = root.current, bottom = footer.current;
    if (!body || !container || !bottom) return;
    let frame = 0;
    const update = () => { cancelAnimationFrame(frame); frame = requestAnimationFrame(() => { const boundary = container.closest<HTMLElement>('[role="region"]'); const viewportBottom = Math.min(window.innerHeight, boundary?.getBoundingClientRect().bottom ?? window.innerHeight); const available = Math.max(240, Math.floor(viewportBottom - Math.max(16, body.getBoundingClientRect().top) - bottom.getBoundingClientRect().height - 28)); setPaneHeight(previous => previous === available ? previous : available); }); };
    const observer = new ResizeObserver(update); observer.observe(container); observer.observe(bottom);
    window.addEventListener("resize", update); window.addEventListener("scroll", update, { passive: true }); update();
    return () => { cancelAnimationFrame(frame); observer.disconnect(); window.removeEventListener("resize", update); window.removeEventListener("scroll", update); };
  }, [fullscreen]);
  async function refresh() { const value = pathsRef.current; await load(value.path, value.query, { ...(value.minSize ? { minSize: value.minSize } : {}), ...(value.maxSize ? { maxSize: value.maxSize } : {}) }); }
  async function navigate(target: string, index?: number) {
    setEditingPath(false); setPath(target); setPathDraft(target); setQuery(""); setMinSize(""); setMaxSize(""); setEntries([]); setSelection([]);
    setHistory(old => index === undefined ? { paths: [...old.paths.slice(0, old.index + 1), target], index: old.index + 1 } : { ...old, index });
    setExpanded(old => [...new Set([...old, target, fileParent(target)])]);
    if (!directoryPicker) propsRef.current.onPathChange?.(target);
    await load(target);
  }
  const visible = useMemo(() => entries.filter(row => (!directoryPicker || row.kind === "directory") && (showHidden || !row.name.startsWith("."))).sort((a, b) => {
    const left = a[sort.key as keyof NodeFileEntry], right = b[sort.key as keyof NodeFileEntry];
    const result = typeof left === "number" && typeof right === "number" ? left - right : String(left ?? "").localeCompare(String(right ?? ""), locale.value, { numeric: true });
    return sort.direction === "asc" ? result : -result;
  }), [entries, directoryPicker, showHidden, sort, locale.value]);
  const selected = visible.filter(row => selection.includes(row.path));
  const pageRows = visible.slice((page - 1) * 50, page * 50);
  async function expandDirectory(target: string) {
    if (!manager || treeRequests.current.has(target)) return;
    const epoch = lifecycle.current; treeRequests.current.add(target);
    try { const rows = await manager.list(target); if (epoch === lifecycle.current) setTree(old => ({ ...old, [target]: rows.filter(row => row.kind === "directory") })); }
    catch (reason) { if (epoch === lifecycle.current) setError(String(reason)); }
    finally { treeRequests.current.delete(target); }
  }
  const treeNodes = useMemo(() => {
    const chain = new Set<string>(); let ancestor = confirmedPath;
    while (ancestor !== "/") { chain.add(ancestor); ancestor = fileParent(ancestor); }
    const build = (target: string, depth = 0): TreeNode => {
      const children = new Set((tree[target] ?? []).map(row => row.path));
      for (const entry of chain) if (fileParent(entry) === target) children.add(entry);
      return { id: target, label: target === "/" ? "/" : target.split("/").pop()!, icon: <Folder size={16} />,
        ...(depth < 30 ? { children: children.size ? [...children].map(child => build(child, depth + 1)) : tree[target] ? [] : [{ id: `pending:${target}`, label: t("guest.loading") }] } : {}) };
    };
    return [build("/")];
  }, [tree, confirmedPath]);
  const changeSelection = (keys: string[]) => setSelection(old => [...old.filter(key => !pageRows.some(row => row.path === key)), ...keys.filter(key => pageRows.some(row => row.path === key))]);
  async function execute(work: () => Promise<unknown>, inModal = false, closeAfter = true) {
    const epoch = lifecycle.current; setBusy(true); setError(""); setModalError("");
    try { await work(); if (epoch !== lifecycle.current) return; if (inModal && closeAfter) setModal(undefined); await refresh(); }
    catch (reason) { if (epoch === lifecycle.current) (inModal ? setModalError : setError)(String(reason)); }
    finally { if (epoch === lifecycle.current) setBusy(false); }
  }
  async function openModal(kind: string, files = selected) {
    modalReturn.current = (files[0] ? root.current?.querySelector<HTMLElement>(`[data-file-path="${CSS.escape(files[0].path)}"]`) : null) ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null);
    const currentModal = ++modalEpoch.current;
    setModalLoading(false); setExtractFolder(false); setImageDirty(false); setModal({ kind, files }); setModalError(""); setDraft(kind === "rename" ? files[0]?.name ?? "" : kind === "archive" ? "archive.zip" : ""); setDestination(kind === "extract" ? path : files[0]?.path ?? path); setText(""); setOriginal("");
    if ((kind === "edit" && !imageFile(files[0]?.path ?? "")) || (kind === "preview" && !/\.(png|jpe?g|webp|gif|bmp|avif|pdf|mp4|webm|mp3|ogg|wav)$/i.test(files[0]?.path ?? ""))) {
      setModalLoading(true); const epoch = lifecycle.current;
      try { const value = await manager!.text(files[0]!.path); if (epoch === lifecycle.current && currentModal === modalEpoch.current) { setText(value); setOriginal(value); } }
      catch (reason) { if (epoch === lifecycle.current && currentModal === modalEpoch.current) setModalError(String(reason)); }
      finally { if (epoch === lifecycle.current && currentModal === modalEpoch.current) setModalLoading(false); }
    }
  }
  function closeModal() { if (busy) return; if (modal?.kind === "edit" && (text !== original || imageDirty)) setDiscard(true); else { modalEpoch.current++; setModal(undefined); } }
  async function openFile(row: NodeFileEntry) { if (row.kind === "directory") await navigate(row.path); else if (row.kind === "file") await openModal("preview", [row]); }
  function download(files = selected) { for (const file of files) { const link = document.createElement("a"); link.href = manager!.downloadURL(file.path); link.download = file.name; document.body.append(link); link.click(); link.remove(); } }
  async function paste(destination = path) {
    if (!manager || !clipboard) return;
    await execute(async () => { for (const source of clipboard.paths) await manager.action({ action: clipboard.cut ? "move" : "copy", path: source, newPath: fileJoin(destination, source.split("/").pop()!) }); if (clipboard.cut) setClipboard(undefined); });
  }
  function gridAction(event: GridAction) {
    const files = entries.filter(row => event.paths.includes(row.path));
    if (busy || loading) return;
    if (event.action === "open" && files[0]) void openFile(files[0]);
    else if (["copy", "cut"].includes(event.action)) setClipboard({ paths: event.paths, cut: event.action === "cut" });
    else if (event.action === "paste") {
      if (event.paths.length && manager) void execute(async () => { for (const source of event.paths) await manager.action({ action: event.cut ? "move" : "copy", path: source, newPath: fileJoin(event.destination ?? path, source.split("/").pop()!) }); });
      else void paste(event.destination ?? path);
    } else if (event.action === "reload") void refresh();
    else if (event.action === "download") download(files);
    else if (files.length) { const kind = event.action === "quicklook" ? "preview" : event.action === "rm" ? "delete" : event.action; if (["preview", "edit", "rename", "delete", "archive", "extract", "info"].includes(kind)) void openModal(kind, files); }
  }
  const menuItems = (files: NodeFileEntry[]): DropdownItem[] => [
    { label: f("open"), disabled: files.length !== 1, onSelect: () => { void openFile(files[0]!); } },
    ...(!directoryPicker ? [
      { label: f("edit"), disabled: files.length !== 1 || files[0]?.kind !== "file", onSelect: () => { void openModal("edit", files); } },
      { label: f("download"), disabled: files.some(row => row.kind !== "file") || !files.length, onSelect: () => download(files) },
      { label: f("copy"), disabled: !files.length, onSelect: () => setClipboard({ paths: files.map(row => row.path), cut: false }) },
      { label: f("cut"), disabled: !files.length, onSelect: () => setClipboard({ paths: files.map(row => row.path), cut: true }) },
      { label: f("rename"), disabled: files.length !== 1, onSelect: () => { void openModal("rename", files); } },
      { label: f("archive"), disabled: !files.length, onSelect: () => { void openModal("archive", files); } },
      { label: f("extract"), disabled: files.length !== 1 || !/\.(zip|tar|gz|tgz|bz2|tbz2)$/i.test(files[0]?.name ?? ""), onSelect: () => { void openModal("extract", files); } },
      { label: f("delete"), destructive: true, disabled: !files.length, onSelect: () => { void openModal("delete", files); } },
    ] : []),
    { label: f("info"), disabled: files.length !== 1, onSelect: () => { void openModal("info", files); } },
  ];
  async function submit(closeAfter = false) {
    if (!modal || !manager) return;
    const current = modal;
    await execute(async () => {
      if (current.kind === "mkdir" || current.kind === "mkfile") await manager.action({ action: current.kind === "mkdir" ? "mkdir" : "save_text", path: fileJoin(path, draft), ...(current.kind === "mkfile" ? { content: "" } : {}) });
      else if (current.kind === "rename") await manager.action({ action: "rename", path: current.files[0]!.path, newPath: fileJoin(fileParent(current.files[0]!.path), draft) });
      else if (current.kind === "delete") for (const row of current.files) await manager.action({ action: "delete", path: row.path, deleteConfirmed: true, confirmationPath: row.path });
      else if (current.kind === "archive") await manager.action({ action: "archive", path: fileJoin(path, draft), paths: current.files.map(row => row.path), format });
      else if (current.kind === "extract") { if (extractFolder) await manager.action({ action: "mkdir", path: destination }); await manager.action({ action: "extract", path: current.files[0]!.path, newPath: destination }); }
      else if (current.kind === "edit") { await manager.saveText(destination, text, current.files[0]!.path); setOriginal(text); if (destination !== current.files[0]!.path) setModal({ kind: "edit", files: [await manager.stat(destination)] }); }
    }, true, current.kind !== "edit" || closeAfter);
  }
  function keydown(event: React.KeyboardEvent) {
    if (busy || modal) return;
    if (event.key === "Escape" && fullscreen) { event.preventDefault(); event.stopPropagation(); setFullscreen(false); return; }
    if (event.target instanceof HTMLElement && event.target.closest('input:not([type="checkbox"]):not([type="radio"]),textarea,[contenteditable="true"],[role="menu"]')) return;
    if (event.key === "F2" && selected.length === 1 && !directoryPicker) { event.preventDefault(); event.stopPropagation(); void openModal("rename"); }
    if (event.key === "Delete" && selected.length && !directoryPicker) { event.preventDefault(); event.stopPropagation(); void openModal("delete"); }
    if (event.key === "Backspace" && path !== "/") { event.preventDefault(); event.stopPropagation(); void navigate(fileParent(path)); }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "a") { event.preventDefault(); setSelection(visible.map(row => row.path)); }
    if ((event.ctrlKey || event.metaKey) && !directoryPicker) {
      if (["c", "x"].includes(event.key.toLowerCase()) && selected.length) { event.preventDefault(); setClipboard({ paths: selection, cut: event.key.toLowerCase() === "x" }); }
      if (event.key.toLowerCase() === "v" && clipboard) { event.preventDefault(); event.stopPropagation(); void paste(); }
    }
  }
  const fileName = (row: NodeFileEntry) => <div className="file-browser-file-open" draggable={!directoryPicker && !busy} onDragStart={event => { event.dataTransfer.setData("application/x-nodedance-files", JSON.stringify({ nodeId, paths: selection.includes(row.path) ? selection : [row.path] })); event.dataTransfer.effectAllowed = "copyMove"; }} onDragOver={event => { if (row.kind === "directory" && !directoryPicker && event.dataTransfer.types.includes("application/x-nodedance-files")) event.preventDefault(); }} onDrop={event => { event.preventDefault(); if (!manager || directoryPicker || row.kind !== "directory") return; try { const payload = JSON.parse(event.dataTransfer.getData("application/x-nodedance-files")); if (payload.nodeId !== nodeId || !Array.isArray(payload.paths)) return; void execute(async () => { for (const source of payload.paths) await manager.action({ action: event.ctrlKey || event.metaKey ? "copy" : "move", path: source, newPath: fileJoin(row.path, source.split("/").pop()!) }); }); } catch (reason) { setError(String(reason)); } }}><ContextMenu><ContextMenuTrigger asChild><FileButton type="button" variant="ghost" size="sm" className="file-browser-name-button" onClick={() => void openFile(row)} data-file-name={row.name} data-file-path={row.path} aria-label={row.name} disabled={row.kind !== "file" && row.kind !== "directory"}>{row.kind === "directory" ? <Folder className="size-4" /> : <File className="size-4" />}<FileNameText name={row.name} /></FileButton></ContextMenuTrigger><ContextMenuContent aria-label={f("actions")} className="file-context-menu registry-controls">{menuItems([row]).map(item => <ContextMenuItem key={item.label} disabled={item.disabled} variant={item.destructive ? "destructive" : "default"} onSelect={item.onSelect}>{item.label}</ContextMenuItem>)}</ContextMenuContent></ContextMenu></div>;
  const modalContainer = portal;
  const body = <div ref={root} className={`file-browser ${fullscreen ? "file-browser-fullscreen" : ""}`} data-testid="node-files" aria-label={nodeName} aria-busy={loading} onKeyDown={keydown}>
    <div className="file-browser-toolbar" role="toolbar" aria-label={f("title")}>
      <Button type="button" size="sm" variant="secondary" aria-label={f("back")} disabled={busy || history.index === 0} onClick={() => void navigate(history.paths[history.index - 1]!, history.index - 1)}><ArrowLeft size={16} /></Button>
      <Button type="button" size="sm" variant="secondary" aria-label={f("forward")} disabled={busy || history.index >= history.paths.length - 1} onClick={() => void navigate(history.paths[history.index + 1]!, history.index + 1)}><ArrowRight size={16} /></Button>
      <Button type="button" size="sm" variant="secondary" aria-label={f("up")} disabled={busy || path === "/"} onClick={() => void navigate(fileParent(path))}><ArrowUp size={16} /></Button>
      <Button type="button" size="sm" variant="secondary" disabled={loading || busy} onClick={() => void refresh()}><RefreshCw size={16} />{f("refresh")}</Button>
      <Button type="button" size="sm" variant="secondary" disabled={!manager || busy} onClick={() => void openModal("mkdir", [])}>{f("mkdir")}</Button>
      {!directoryPicker && <><Button type="button" size="sm" variant="secondary" disabled={!manager || busy} onClick={() => void openModal("mkfile", [])}>{f("mkfile")}</Button><Button type="button" size="sm" variant="secondary" disabled={!manager || busy} onClick={() => void openModal("upload", [])}><Upload size={16} />{f("upload")}</Button><DropdownMenu label={f("actions")} items={menuItems(selected)} disabled={busy} portalContainer={portal} /><Button type="button" size="sm" variant="secondary" disabled={!clipboard || busy} onClick={() => void paste()}>{f("paste")}</Button></>}
      <DropdownMenu label={f("view")} portalContainer={portal} items={[{ label: f("selectAll"), onSelect: () => setSelection(visible.map(row => row.path)) }, { label: f("selectNone"), onSelect: () => setSelection([]) }, { label: f("invert"), onSelect: () => setSelection(visible.filter(row => !selection.includes(row.path)).map(row => row.path)) }, { label: f("hidden"), onSelect: () => setShowHidden(value => !value) }, ...(["name", "size", "modifiedAt", "mode"] as const).map(key => ({ label: `${f("sortBy")} ${f(key === "modifiedAt" ? "modified" : key === "mode" ? "permissions" : key)}`, onSelect: () => { setSort(old => ({ key, direction: old.key === key && old.direction === "asc" ? "desc" : "asc" })); setPage(1); } })), { label: f("help"), onSelect: () => { void openModal("help", []); } }]} />
      <Button type="button" size="sm" variant="ghost" aria-label={f("fullscreen")} onClick={() => setFullscreen(value => !value)}><Maximize size={16} /></Button>
    </div>
    {editingPath ? <form ref={addressForm} className="file-browser-location" onBlur={event => { if (event.relatedTarget && !event.currentTarget.contains(event.relatedTarget)) setEditingPath(false); }} onSubmit={event => { event.preventDefault(); event.stopPropagation(); if (pathDraft) { void navigate(pathDraft); finishPathEditing(); } }}>
      <Input aria-label={f("path")} data-address-input required autoFocus value={pathDraft} onFocus={event => event.currentTarget.select()} onChange={event => setPathDraft(event.target.value)} onKeyDown={event => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); finishPathEditing(); } }} />
      <Button type="submit" size="sm" variant="secondary">{f("go")}</Button>
    </form> : <div className="file-browser-location">
      <FileAddressBreadcrumb path={path} portal={portal} onNavigate={target => { void navigate(target); }} onEdit={editPath} />
      <CopyButton value={path} label={f("copyPath")} iconOnly />
    </div>}
    {!directoryPicker && <form className="file-browser-search" onSubmit={event => { event.preventDefault(); event.stopPropagation(); void refresh(); }}>
      <Input aria-label={f("search")} placeholder={f("search")} value={query} onChange={event => setQuery(event.target.value)} />
      <Input type="number" aria-label={f("minSize")} placeholder={f("minSize")} value={minSize} onChange={event => setMinSize(event.target.value)} />
      <Input type="number" aria-label={f("maxSize")} placeholder={f("maxSize")} value={maxSize} onChange={event => setMaxSize(event.target.value)} />
      <Button type="submit" size="sm" variant="secondary" disabled={loading || busy}><Search size={16} />{f("search")}</Button>
      <SegmentedControl label={f("view")} value={view} onValueChange={setView} options={[{ value: "list", label: f("list") }, { value: "grid", label: f("grid") }]} />
    </form>}
    {error && <Alert tone="danger" title={error} />}
    <div ref={fileBody} className="file-browser-body" style={{ height: paneHeight }}>
      <div className="file-browser-tree"><TreeView fillHeight maxIndentDepth={3} renderLabel={node => <FileNameText name={node.label} />} aria-label={f("directories")} nodes={treeNodes} expandedIds={expanded} onExpandedChange={values => { setExpanded(values); for (const value of values) if (!tree[value] && !value.startsWith("pending:")) void expandDirectory(value); }} onSelect={node => { if (node.id.startsWith("pending:")) { void expandDirectory(node.id.slice(8)); return; } if (!busy) void navigate(node.id); }} /></div>
      <ScrollArea ref={filePane} className={`file-browser-list ${view === "grid" ? "file-browser-grid-panel" : ""}`} viewportClassName="file-browser-list-viewport" orientation="vertical" fade={0} label={f("title")}>
    {loading && <div data-testid="file-loading"><Skeleton label={t(entries.length ? "load.refreshing" : "load.initial")} lines={entries.length ? 1 : 3} /></div>}
        {view === "list" ? <SortableDataTable fillHeight rows={pageRows.map(row => ({ ...row }))} rowKey="path" caption={f("title")} itemName={{ one: f("items"), other: f("items") }} selectable selectedKeys={selection} onSelectionChange={changeSelection} sort={sort} onSortChange={next => { setSort(next); setPage(1); }} emptyMessage={loading ? t("guest.loading") : f("empty")} columns={([
          { key: "name", label: f("name"), sortable: true, render: (_value, row) => fileName(row) },
          { key: "size", label: f("size"), sortable: true, numeric: true, render: (value, row) => row.kind === "directory" ? "—" : size(Number(value)) },
          { key: "modifiedAt", label: f("modified"), sortable: true, render: value => new Date(Number(value) / 1e6).toLocaleString(locale.value) },
          { key: "mode", label: f("permissions"), render: value => Number(value).toString(8) },
        ] satisfies DataColumn<NodeFileEntry>[]).filter(column => column.key === "name" || (column.key === "size" ? paneWidth >= 340 : paneWidth >= 640))} /> : <ElFinderGrid path={path} rows={pageRows} selection={selection} busy={busy || loading || !!modal || discard} canPaste={!!clipboard} onSelection={changeSelection} onAction={gridAction} onEscape={() => { if (modal) closeModal(); else if (fullscreen) setFullscreen(false); }} />}

      </ScrollArea>
    </div>
    <footer ref={footer} className="file-browser-footer"><span>{visible.length} {f("items")} · {selection.length} {f("selected")}</span><Pagination page={page} pageCount={Math.ceil(visible.length / 50)} onPageChange={setPage} label={f("pages")} />{directoryPicker && onChoose && <Button type="button" disabled={loading || !!error} onClick={() => onChoose(path)} data-testid="compose-directory-use">{t("compose.useDirectory")}</Button>}</footer>
    {observations.length > 0 && <section className="file-browser-tasks" aria-label={f("tasks")}>{observations.map(item => <div key={item.taskId}><span>{item.path} · {item.status}</span><Button type="button" size="sm" variant="secondary" onClick={() => { void execute(() => manager!.observe(item.taskId)); }}>{f("observe")}</Button><Button type="button" size="sm" variant="secondary" onClick={() => { void execute(() => manager!.cancel(item.taskId)); }}>{f("cancelTask")}</Button><Button type="button" size="sm" variant="ghost" onClick={() => manager!.forget(item.taskId)}>{t("operations.forget")}</Button></div>)}</section>}
    <Dialog open={!!modal} onOpenChange={open => { if (!open) closeModal(); }}><DialogContent container={modalContainer} onCloseAutoFocus={event => { event.preventDefault(); const target = modalReturn.current?.isConnected && !modalReturn.current.matches(":disabled") ? modalReturn.current : root.current?.querySelector<HTMLElement>("button:not(:disabled)"); target?.focus({ preventScroll: true }); }} title={modal ? f(modal.kind) : ""} className={modal?.kind === "edit" || modal?.kind === "preview" ? "file-editor-dialog" : ""} onEscapeKeyDown={event => { if (busy) event.preventDefault(); }} onPointerDownOutside={event => { if (busy) event.preventDefault(); }}>
      {modalError && <Alert tone="danger" title={modalError} />}
      {modalLoading && <Skeleton label={t("load.initial")} lines={3} />}
      {modal?.kind === "upload" && <><Input label={f("uploadDirectory")} aria-label={f("uploadDirectory")} type="file" multiple {...({ webkitdirectory: "" } as any)} onChange={event => { const files = Array.from(event.target.files ?? []); void execute(async () => { for (const file of files) await manager!.upload(path, file); }); }} /><FileUpload label={f("upload")} description={path} maxSize={manager?.maxFileSize} onUpload={async (file, options) => { await manager!.upload(path, file, options.signal); await refresh(); }} /></>}
      {modal?.kind === "info" && modal.files.map(row => <dl className="file-info" key={row.path}><dt>{f("path")}</dt><dd><span className="file-full-path">{row.path}</span><CopyButton value={row.path} label={f("copyPath")} iconOnly /></dd><dt>{f("size")}</dt><dd>{size(row.size)}</dd><dt>{f("permissions")}</dt><dd>{row.mode.toString(8)} · {row.ownerUid}:{row.ownerGid}</dd><dt>{f("modified")}</dt><dd>{new Date(row.modifiedAt / 1e6).toLocaleString(locale.value)}</dd><dt>{f("version")}</dt><dd>{row.version}</dd></dl>)}
      {modal?.kind === "help" && <p>{f("shortcuts")}</p>}
      {modal?.kind === "delete" && <div><p>{f("deletePrompt")}</p>{modal.files.map(row => <p key={row.path}>{row.path}</p>)}</div>}
      {modal?.kind === "preview" && manager && <FilePreview file={modal.files[0]!} manager={manager} text={text} />}
      {modal?.kind === "edit" && imageFile(modal.files[0]!.path) && manager && <FileImageEditor key={modal.files[0]!.path} onDirty={setImageDirty} manager={manager} file={modal.files[0]!} onSaved={async (target, closeAfter) => { if (closeAfter) setModal(undefined); else setModal({ kind: "edit", files: [await manager.stat(target)] }); await refresh(); }} />}
      {modal && ["mkdir", "mkfile", "rename", "archive", "extract", "delete", "edit"].includes(modal.kind) && !(modal.kind === "edit" && imageFile(modal.files[0]!.path)) && <form onSubmit={event => { event.preventDefault(); event.stopPropagation(); void submit(); }} className="file-dialog-form">
        {["mkdir", "mkfile", "rename", "archive"].includes(modal.kind) && <Input label={f("name")} aria-label={f("name")} value={draft} required disabled={busy} onChange={event => setDraft(event.target.value)} />}
        {modal.kind === "archive" && <Select label={f("format")} value={format} onValueChange={value => { setFormat(value); setDraft(`archive.${value}`); }} options={(manager?.archiveFormats ?? []).map(value => ({ value, label: value }))} />}
        {modal.kind === "extract" && <Checkbox label={f("extractFolder")} checked={extractFolder} onCheckedChange={value => { setExtractFolder(value === true); if (value) setDestination(fileJoin(path, modal.files[0]!.name.replace(/\.(tar\.gz|tar\.bz2|tbz2|tgz|zip|tar)$/i, ""))); else setDestination(path); }} />}
        {["edit", "extract"].includes(modal.kind) && <Input label={f("destination")} aria-label={f("destination")} value={destination} required disabled={busy} onChange={event => setDestination(event.target.value)} />}
        {modal.kind === "edit" && <Textarea aria-label={f("contents")} value={text} disabled={busy || modalLoading} spellCheck={false} rows={14} onChange={event => setText(event.target.value)} />}
        <div className="file-dialog-actions"><Button variant="secondary" type="button" disabled={busy} onClick={closeModal}>{t("dialog.cancel")}</Button><Button variant={modal.kind === "delete" ? "danger" : "primary"} type="submit" disabled={busy || modalLoading} loading={busy}>{f(modal.kind === "edit" ? "save" : "confirm")}</Button>{modal.kind === "edit" && <Button type="button" variant="secondary" disabled={busy || modalLoading} onClick={() => void submit(true)}>{f("saveClose")}</Button>}</div>
      </form>}
      {modal?.kind === "preview" && <div className="file-dialog-actions"><Button type="button" variant="secondary" onClick={() => download(modal.files)}><Download size={16} />{f("download")}</Button><Button type="button" onClick={() => void openModal("edit", modal.files)}>{f("edit")}</Button></div>}
    </DialogContent></Dialog>
    <Dialog open={discard} onOpenChange={setDiscard}><DialogContent container={modalContainer} title={f("discard")}><div className="file-dialog-actions"><Button type="button" variant="secondary" onClick={() => setDiscard(false)}>{t("dialog.cancel")}</Button><Button type="button" variant="danger" onClick={() => { setDiscard(false); setModal(undefined); }}>{f("discard")}</Button></div></DialogContent></Dialog>
  </div>;
  return body;
}

function FilePreview({ file, manager, text }: { file: NodeFileEntry; manager: FileManager; text: string }) {
  const url = manager.previewURL(file.path);
  if (/\.(png|jpe?g|webp|gif|bmp|avif)$/i.test(file.name)) return <img className="file-passive-preview" src={url} alt={file.name} />;
  if (/\.pdf$/i.test(file.name)) return <iframe className="file-document-preview" title={file.name} sandbox="" src={url} />;
  if (/\.(mp4|webm|mp3|ogg|wav)$/i.test(file.name)) return <FileMedia url={url} video={/\.(mp4|webm)$/i.test(file.name)} />;
  return <Textarea aria-label={f("contents")} readOnly value={text} rows={14} />;
}
function FileMedia({ url, video }: { url: string; video: boolean }) {
  const media = useRef<HTMLMediaElement>(null), [playing, setPlaying] = useState(false), [position, setPosition] = useState(0), [duration, setDuration] = useState(0), [error, setError] = useState("");
  const attributes = { src: url, onTimeUpdate: () => setPosition(media.current?.currentTime ?? 0), onLoadedMetadata: () => setDuration(media.current?.duration ?? 0), onPlay: () => setPlaying(true), onPause: () => setPlaying(false) };
  return <div className="file-media">{error && <Alert tone="danger" title={error} />}{video ? <video ref={media as React.RefObject<HTMLVideoElement>} {...attributes} /> : <audio ref={media as React.RefObject<HTMLAudioElement>} {...attributes} />}<Button type="button" variant="secondary" onClick={() => { if (playing) media.current?.pause(); else void media.current?.play().catch(reason => setError(String(reason))); }}>{f(playing ? "pause" : "play")}</Button><Slider label={f("position")} value={position} min={0} max={Number.isFinite(duration) && duration > 0 ? duration : 1} onValueChange={value => { if (media.current) media.current.currentTime = value; setPosition(value); }} /><Slider label={f("volume")} min={0} max={1} step={0.05} defaultValue={1} onValueChange={value => { if (media.current) media.current.volume = value; }} /></div>;
}
