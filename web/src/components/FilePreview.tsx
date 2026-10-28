import { lazy, Suspense, useEffect, useRef, useState } from "react";
import type { NodeFileEntry } from "../api";
import type { FileManager } from "../file-manager";
import { t } from "../i18n";
import { Button } from "./ManagementUI";
import { Slider } from "./arc/slider/slider";
import { Alert } from "./arc/alert/alert";
import { Skeleton } from "./arc/skeleton/skeleton";
const FileCodeEditor = lazy(() => import("./FileCodeEditor"));
const f = (key: string) => t(`files.ui.${key}`);

export default function FilePreview({ file, manager, text }: { file: NodeFileEntry; manager: FileManager; text?: string }) {
  const [content, setContent] = useState(text ?? ""), [error, setError] = useState(""), [loading, setLoading] = useState(false);
  const passive = /\.(png|jpe?g|webp|gif|bmp|avif|pdf|mp4|webm|mp3|ogg|wav)$/i.test(file.name);
  useEffect(() => {
    setError("");
    if (text !== undefined || passive) { setContent(text ?? ""); setLoading(false); return; }
    const controller = new AbortController(); setContent(""); setLoading(true);
    void manager.previewText(file.path, controller.signal).then(value => { if (!controller.signal.aborted) setContent(value); }).catch(reason => { if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : String(reason)); }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [manager, file.path, file.version, text, passive]);
  const url = manager.previewURL(file.path);
  if (/\.(png|jpe?g|webp|gif|bmp|avif)$/i.test(file.name)) return <img className="file-passive-preview" src={url} alt={file.name} />;
  if (/\.pdf$/i.test(file.name)) return <iframe className="file-document-preview" title={file.name} sandbox="" src={url} />;
  if (/\.(mp4|webm|mp3|ogg|wav)$/i.test(file.name)) return <FileMedia url={url} video={/\.(mp4|webm)$/i.test(file.name)} />;
  if (error) return <Alert tone="danger" title={error} />;
  if (loading) return <Skeleton label={t("load.initial")} lines={8} />;
  return <Suspense fallback={<Skeleton label={t("load.initial")} lines={8} />}><FileCodeEditor path={file.path} value={content} readOnly /></Suspense>;
}
function FileMedia({ url, video }: { url: string; video: boolean }) {
  const media = useRef<HTMLMediaElement>(null), [playing, setPlaying] = useState(false), [position, setPosition] = useState(0), [duration, setDuration] = useState(0), [error, setError] = useState("");
  const attributes = { src: url, onTimeUpdate: () => setPosition(media.current?.currentTime ?? 0), onLoadedMetadata: () => setDuration(media.current?.duration ?? 0), onPlay: () => setPlaying(true), onPause: () => setPlaying(false) };
  return <div className="file-media">{error && <Alert tone="danger" title={error} />}{video ? <video ref={media as React.RefObject<HTMLVideoElement>} {...attributes} /> : <audio ref={media as React.RefObject<HTMLAudioElement>} {...attributes} />}<Button type="button" variant="secondary" onClick={() => { if (playing) media.current?.pause(); else void media.current?.play().catch(reason => setError(reason instanceof Error ? reason.message : String(reason))); }}>{f(playing ? "pause" : "play")}</Button><Slider label={f("position")} value={position} min={0} max={Number.isFinite(duration) && duration > 0 ? duration : 1} onValueChange={value => { if (media.current) media.current.currentTime = value; setPosition(value); }} /><Slider label={f("volume")} min={0} max={1} step={0.05} defaultValue={1} onValueChange={value => { if (media.current) media.current.volume = value; }} /></div>;
}
