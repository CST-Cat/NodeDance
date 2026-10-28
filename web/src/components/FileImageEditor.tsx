import { useEffect, useRef, useState } from "react";
import { useSignals } from "@preact/signals-react/runtime";
import type { NodeFileEntry } from "../api";
import type { FileManager } from "../file-manager";
import { loadImageEngine, imageDataURL } from "../file-image-engine";
import { t } from "../i18n";
import { Button, Input, Select } from "./ManagementUI";
import { Slider } from "./arc/slider/slider";
import { ColorPicker } from "./arc/color-picker/color-picker";
import { FileUpload } from "./arc/file-upload/file-upload";
import { Alert } from "./arc/alert/alert";
import { Skeleton } from "./arc/skeleton/skeleton";

const f = (key: string) => t(`files.ui.${key}`);
export default function FileImageEditor({ file, manager, onSaved, onDirty }: { file: NodeFileEntry; manager: FileManager; onSaved: (path: string, close: boolean) => void | Promise<void>; onDirty: (value: boolean) => void }) {
  useSignals();
  const host = useRef<HTMLDivElement>(null), engine = useRef<any>(null), active = useRef(true);
  const [ready, setReady] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [head, setHead] = useState("none"), [tail, setTail] = useState("none"), [maskId, setMaskId] = useState<number>();
  const [fontFamily, setFontFamily] = useState("sans-serif"), [decoration, setDecoration] = useState(""), [blendMode, setBlendMode] = useState("multiply");
  const [tool, setTool] = useState("select"), [color, setColor] = useState(""), [brush, setBrush] = useState(4), [opacity, setOpacity] = useState(1);
  const [shape, setShape] = useState("rect"), [icon, setIcon] = useState("arrow"), [fill, setFill] = useState(true);
  const [text, setText] = useState(""), [fontSize, setFontSize] = useState(24), [fontWeight, setFontWeight] = useState("normal"), [fontStyle, setFontStyle] = useState("normal"), [align, setAlign] = useState("left");
  const [width, setWidth] = useState(""), [height, setHeight] = useState(""), [angle, setAngle] = useState("90"), [ratio, setRatio] = useState("free");
  const [filter, setFilter] = useState("grayscale"), [strength, setStrength] = useState(0.2), [format, setFormat] = useState(/\.jpe?g$/i.test(file.name) ? "jpeg" : /\.webp$/i.test(file.name) ? "webp" : "png"), [quality, setQuality] = useState(0.92);
  const [destination, setDestination] = useState(file.path), [selected, setSelected] = useState<number>(), [undo, setUndo] = useState(0), [redo, setRedo] = useState(0), [zoom, setZoom] = useState(1);
  const dirty = useRef(onDirty); dirty.current = onDirty;
  useEffect(() => {
    active.current = true; let instance: any;
    void (async () => {
      const [Constructor, blob] = await Promise.all([loadImageEngine(), manager.image(file.path)]);
      if (!active.current || !host.current) return;
      const tokens = getComputedStyle(host.current), foreground = tokens.getPropertyValue("--text").trim(), edge = tokens.getPropertyValue("--focus").trim();
      setColor(foreground);
      instance = new Constructor(host.current, { cssMaxWidth: Math.max(200, host.current.clientWidth), cssMaxHeight: 460, usageStatistics: false, selectionStyle: { cornerColor: foreground, cornerStrokeColor: edge, borderColor: edge } });
      engine.current = instance; const extension = file.name.split(".").pop()?.toLowerCase(); const url = await imageDataURL(blob, `image/${extension === "jpg" ? "jpeg" : extension ?? "png"}`);
      await instance.loadImageFromURL(url, file.name);
      if (!active.current) return;
      instance.clearUndoStack(); instance.clearRedoStack();
      const dimensions = instance.getCanvasSize(); setWidth(String(dimensions.width)); setHeight(String(dimensions.height));
      instance.on("undoStackChanged", (length: number) => { if (active.current) { setUndo(length); dirty.current(length > 0); } });
      instance.on("redoStackChanged", (length: number) => { if (active.current) setRedo(length); });
      instance.on("objectActivated", (object: { id: number }) => { if (active.current) setSelected(object.id); });
      instance.on("selectionCleared", () => { if (active.current) setSelected(undefined); });
      setReady(true);
    })().catch(reason => { if (active.current) setError(reason instanceof Error ? reason.message : String(reason)); });
    return () => { active.current = false; instance?.destroy(); engine.current = null; };
  }, [file.path, manager]);
  useEffect(() => {
    if (!engine.current || !ready) return;
    engine.current.changeSelectableAll(!busy);
    if (busy) engine.current.stopDrawingMode();
    else if (["draw", "line", "shape", "crop"].includes(tool)) drawing(tool);
  }, [busy, ready]);
  useEffect(() => { if (ready && ["draw", "line", "shape"].includes(tool)) drawing(tool); }, [brush, color, fill, shape, head, tail]);
  async function run(method: string, ...args: any[]) {
    if (!engine.current || busy) return;
    setBusy(true); setError("");
    try {
      const result = await engine.current[method](...args);
      if (active.current && ["resize", "rotate", "setAngle", "crop", "undo", "redo"].includes(method)) { const dimensions = engine.current.getCanvasSize(); setWidth(String(dimensions.width)); setHeight(String(dimensions.height)); }
      return result;
    }
    catch (reason) { if (active.current) setError(reason instanceof Error ? reason.message : String(reason)); }
    finally { if (active.current) setBusy(false); }
  }
  function drawing(value: string) {
    setTool(value); if (!engine.current) return;
    engine.current.stopDrawingMode();
    if (value === "draw" || value === "line") engine.current.startDrawingMode(value === "draw" ? "FREE_DRAWING" : "LINE_DRAWING", { width: brush, color, ...(value === "line" ? { arrowType: { head: head === "none" ? undefined : head, tail: tail === "none" ? undefined : tail } } : {}) });
    else if (value === "crop") { engine.current.startDrawingMode("CROPPER"); if (ratio !== "free") engine.current.setCropzoneRect(Number(ratio)); }
    else if (value === "shape") { engine.current.setDrawingShape(shape, { fill: fill ? color : "transparent", stroke: color, strokeWidth: brush }); engine.current.startDrawingMode("SHAPE"); }
  }
  async function save(closeAfter = false) {
    if (!engine.current) return;
    setBusy(true); setError("");
    try {
      engine.current.stopDrawingMode(); engine.current.discardSelection();
      const data = engine.current.toDataURL({ format, quality }), base64 = data.slice(data.indexOf(",") + 1), bytes = Uint8Array.from(atob(base64), value => value.charCodeAt(0));
      await manager.saveImage(destination, new File([bytes], destination.split("/").pop()!, { type: `image/${format}` }), file.path);
      if (active.current) { engine.current.clearUndoStack(); engine.current.clearRedoStack(); dirty.current(false); await onSaved(destination, closeAfter); }
    } catch (reason) { if (active.current) setError(reason instanceof Error ? reason.message : String(reason)); }
    finally { if (active.current) setBusy(false); }
  }
  const options = (values: string[]) => values.map(value => ({ value, label: f(value) }));
  return <div className="file-image-editor" data-testid="file-image-editor">
    {error && <Alert tone="danger" title={error} />}
    {!ready && !error && <Skeleton label={t("load.initial")} lines={3} />}
    <div className="file-browser-toolbar">
      <Select aria-label={f("tool")} value={tool} onValueChange={drawing} disabled={!ready || busy} options={options(["select", "crop", "draw", "line", "shape", "textTool", "icon", "filter", "mask", "resize"])} />
      <Button type="button" size="sm" variant="secondary" disabled={!ready || !undo || busy} onClick={() => void run("undo")}>{f("undo")}</Button>
      <Button type="button" size="sm" variant="secondary" disabled={!ready || !redo || busy} onClick={() => void run("redo")}>{f("redo")}</Button>
      <Button type="button" size="sm" variant="secondary" disabled={!ready || selected === undefined || busy} onClick={() => void run("removeObject", selected)}>{f("deleteObject")}</Button>
      <Button type="button" size="sm" variant="secondary" disabled={!ready || busy} onClick={() => void run("clearObjects")}>{f("clearObjects")}</Button>
    </div>
    <div ref={host} className="file-image-canvas" data-testid="file-image-canvas" />
    <fieldset disabled={!ready || busy} className="file-image-controls">
      <ColorPicker label={f("color")} value={color} onValueChange={value => { setColor(value); engine.current?.setBrush({ width: brush, color: value }); }} />
      <Slider label={f("brush")} min={1} max={60} value={brush} onValueChange={value => { setBrush(value); engine.current?.setBrush({ width: value, color }); }} />
      <Slider label={f("opacity")} min={0} max={1} step={0.05} value={opacity} onValueChange={setOpacity} onValueCommit={value => { if (selected !== undefined) void run("setObjectProperties", selected, { opacity: value }); }} />
      <Slider label={f("zoom")} min={1} max={5} step={0.1} value={zoom} onValueChange={value => { setZoom(value); engine.current?.zoom({ x: Number(width) / 2, y: Number(height) / 2, zoomLevel: value }); }} />
      <Input label={f("angle")} type="number" value={angle} onChange={event => setAngle(event.target.value)} />
      <div className="file-browser-toolbar"><Button type="button" size="sm" variant="secondary" onClick={() => void run("rotate", Number(angle))}>{f("rotate")}</Button><Button type="button" size="sm" variant="secondary" onClick={() => void run("flipX")}>{f("flipX")}</Button><Button type="button" size="sm" variant="secondary" onClick={() => void run("flipY")}>{f("flipY")}</Button><Button type="button" size="sm" variant="secondary" onClick={() => void run("resetFlip")}>{f("resetFlip")}</Button><Button type="button" size="sm" variant="secondary" onClick={() => { setZoom(1); void run("resetZoom"); }}>{f("resetZoom")}</Button></div>
      {tool === "line" && <><Select label={f("arrowHead")} value={head} onValueChange={setHead} options={options(["none", "chevron", "triangle"])} /><Select label={f("arrowTail")} value={tail} onValueChange={setTail} options={options(["none", "chevron", "triangle"])} /></>}
      {tool === "crop" && <><Select label={f("ratio")} value={ratio} onValueChange={value => { setRatio(value); if (value !== "free") engine.current?.setCropzoneRect(Number(value)); }} options={[{ value: "free", label: f("free") }, ...["1", "1.5", "1.3333333333333333", "1.25", "1.7777777777777777"].map((value, index) => ({ value, label: ["1:1", "3:2", "4:3", "5:4", "16:9"][index]! }))]} /><Button type="button" variant="secondary" onClick={() => { const zone = engine.current?.getCropzoneRect(); if (zone) void run("crop", zone); }}>{f("applyCrop")}</Button></>}
      {tool === "resize" && <><Input label={f("width")} type="number" value={width} onChange={event => setWidth(event.target.value)} /><Input label={f("height")} type="number" value={height} onChange={event => setHeight(event.target.value)} /><Button type="button" variant="secondary" onClick={() => void run("resize", { width: Number(width), height: Number(height) })}>{f("resize")}</Button></>}
      {tool === "shape" && <><Select label={f("shape")} value={shape} onValueChange={value => { setShape(value); engine.current?.setDrawingShape(value, { fill: fill ? color : "transparent", stroke: color, strokeWidth: brush }); }} options={options(["rect", "circle", "triangle"])} /><Select label={f("fill")} value={String(fill)} onValueChange={value => { setFill(value === "true"); engine.current?.setDrawingShape(shape, { fill: value === "true" ? color : "transparent", stroke: color, strokeWidth: brush }); }} options={[{ value: "true", label: f("filled") }, { value: "false", label: f("outline") }]} /><Button type="button" variant="secondary" disabled={selected === undefined} onClick={() => void run("changeShape", selected, { fill: fill ? color : "transparent", stroke: color, strokeWidth: brush })}>{f("updateShape")}</Button></>}
      {tool === "textTool" && <><Input label={f("textTool")} value={text} onChange={event => setText(event.target.value)} /><Input label={f("fontFamily")} value={fontFamily} onChange={event => setFontFamily(event.target.value)} /><Select label={f("decoration")} value={decoration} onValueChange={setDecoration} options={[{ value: "", label: f("normal") }, ...options(["underline", "line-through", "overline"])]} /><Input label={f("fontSize")} type="number" value={fontSize} onChange={event => setFontSize(Number(event.target.value))} /><Select label={f("fontWeight")} value={fontWeight} onValueChange={setFontWeight} options={options(["normal", "bold"])} /><Select label={f("fontStyle")} value={fontStyle} onValueChange={setFontStyle} options={options(["normal", "italic"])} /><Select label={f("align")} value={align} onValueChange={setAlign} options={options(["left", "center", "right"])} /><Button type="button" variant="secondary" onClick={() => void run("addText", text, { styles: { fill: color, fontSize, fontWeight, fontStyle, fontFamily, textDecoration: decoration, textAlign: align } })}>{f("addText")}</Button><Button type="button" variant="secondary" disabled={selected === undefined} onClick={() => { void run("changeText", selected, text).then(() => run("changeTextStyle", selected, { fill: color, fontSize, fontWeight, fontStyle, fontFamily, textDecoration: decoration, textAlign: align })); }}>{f("updateText")}</Button></>}
      {tool === "icon" && <><Select label={f("icon")} value={icon} onValueChange={setIcon} options={options(["arrow", "cancel"])} /><Button type="button" variant="secondary" onClick={() => void run("addIcon", icon, { fill: color })}>{f("addIcon")}</Button><Button type="button" variant="secondary" disabled={selected === undefined} onClick={() => void run("changeIconColor", selected, color)}>{f("updateIcon")}</Button></>}
      {tool === "filter" && <><Select label={f("filter")} value={filter} onValueChange={setFilter} options={options(["grayscale", "sepia", "sepia2", "invert", "blur", "sharpen", "emboss", "brightness", "noise", "pixelate", "removeColor", "colorFilter", "tint", "multiply", "blendColor"])} /><Select label={f("blendMode")} value={blendMode} onValueChange={setBlendMode} options={options(["multiply", "add", "diff", "subtract", "screen", "lighten", "darken", "overlay", "tint"])} /><Slider label={f("strength")} min={0} max={1} step={0.01} value={strength} onValueChange={setStrength} /><Button type="button" variant="secondary" onClick={() => void run("applyFilter", filter, { brightness: strength, blur: strength, noise: strength * 100, blocksize: Math.max(1, Math.round(strength * 30)), distance: strength, alpha: strength, color, mode: blendMode })}>{f("applyFilter")}</Button><Button type="button" variant="secondary" onClick={() => void run("removeFilter", filter)}>{f("removeFilter")}</Button></>}
      {tool === "mask" && <><FileUpload label={f("mask")} accept="image/*" onUpload={async file => { const url = await imageDataURL(file, file.type || "image/png"); const object = await engine.current.addImageObject(url); setMaskId(object.id); }} /><Button type="button" variant="secondary" disabled={maskId === undefined} onClick={() => { void run("applyFilter", "mask", { maskObjId: maskId }).then(() => setMaskId(undefined)); }}>{f("applyMask")}</Button></>}
    </fieldset>
    <form className="file-dialog-form" onSubmit={event => { event.preventDefault(); event.stopPropagation(); void save(); }}>
      <Input label={f("destination")} value={destination} required disabled={!ready || busy} onChange={event => setDestination(event.target.value)} />
      <Select label={f("format")} value={format} onValueChange={setFormat} options={["png", "jpeg", "webp"].map(value => ({ value, label: value.toUpperCase() }))} />
      {format === "jpeg" && <Slider label={f("quality")} min={0.1} max={1} step={0.01} value={quality} onValueChange={setQuality} />}
      <div className="file-dialog-actions"><Button type="submit" disabled={!ready || busy} loading={busy}>{f("save")}</Button><Button type="button" variant="secondary" disabled={!ready || busy} onClick={() => void save(true)}>{f("saveClose")}</Button></div>
    </form>
  </div>;
}
