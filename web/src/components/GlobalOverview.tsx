import { Component, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import type { COBEOptions } from "cobe";
import { ArrowUpRight, Globe2, RefreshCw, Server, MapPin } from "lucide-react";
import { operationRequest, type AgentNode, type AgentNodesResponse, type DashboardPreference, type AgentMetricsResponse } from "../api";
import type { Metric, MetricsView } from "../metrics-contract";
import { formatBytes } from "../metric-format";
import { locale, t } from "../i18n";
import locations from "../country-locations.json";
import { ManagementPage, Button, Card, Badge, Alert } from "./ManagementUI";
import { AnimatedCounter } from "./arc/animated-counter/animated-counter";
import { Progress } from "./arc/progress/progress";
import { Skeleton } from "./arc/skeleton/skeleton";
import { EmptyState } from "./arc/empty-state/empty-state";
import { Globe } from "./ui/globe";
import CountryLogo from "./CountryLogo";
import NodeLogo from "./NodeLogo";
import "./GlobalOverview.css";

type Place = { location: number[]; continent: string };
type Point = { node: AgentNode; id: string; code: string; label: string; place: Place };
const countries: Record<string, Place> = locations;
const radians = Math.PI / 180;
// Same camera convention as the local Komari theme's latLngToAngles.
export function frontScore(location: number[], phi: number, theta: number) {
  const target = Math.PI - (location[1] * radians - Math.PI / 2);
  const latitude = location[0] * radians;
  return Math.cos(target - phi) * Math.cos(latitude) * Math.cos(theta) + Math.sin(latitude) * Math.sin(theta);
}
function countryName(code: string) {
  return new Intl.DisplayNames([locale.value], { type: "region" }).of(code) ?? code;
}
function known<T>(metric?: Metric<T>): T | null {
  return metric?.status === "known" && metric.value != null ? metric.value : null;
}
function percent(value: number | null | undefined) {
  return value != null && Number.isFinite(value) && value >= 0 ? Math.min(100, value) : null;
}
class GlobeBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  render() { return this.state.failed ? <div className="global-globe-fallback"><EmptyState icon={<Globe2 />} title={t("global.webglTitle")} description={t("global.webglHint")} /></div> : this.props.children; }
}

export default function GlobalOverview({ onOpenNode }: { onOpenNode: (nodeId: string, section: string) => void }) {
  const host = useRef<HTMLDivElement>(null);
  const [nodes, setNodes] = useState<AgentNode[]>([]), [preferences, setPreferences] = useState<Record<string, DashboardPreference>>({});
  const [ready, setReady] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [epoch, setEpoch] = useState(0), [activeId, setActiveId] = useState("");
  const [samples, setSamples] = useState<Record<string, MetricsView>>({});
  const [colors, setColors] = useState({ dark: 0, base: [0.5, 0.5, 0.5], glow: [1, 1, 1], marker: [0.5, 0.5, 0.5] });
  const clockOffset = useRef(0);
  const now = () => Date.now() + clockOffset.current;
  const online = (node: AgentNode) => node.status === "online" && (!node.leaseValidUntil || Date.parse(node.leaseValidUntil) > now());
  useEffect(() => {
    const controller = new AbortController(); let timer = 0, preferenceReadAt = 0;
    const read = async () => {
      if (document.hidden) { timer = window.setTimeout(read, 5000); return; }
      setBusy(true);
      try {
        const payload = await operationRequest<AgentNodesResponse>("nodes.list", {}, { signal: controller.signal });
        if (controller.signal.aborted) return;
        const server = Date.parse(payload.serverTime); if (Number.isFinite(server)) clockOffset.current = server - Date.now();
        setNodes(payload.nodes); setError("");
        if (Date.now() - preferenceReadAt >= 30000) {
          const pending = [...payload.nodes], loaded: Record<string, DashboardPreference> = {}; let failures = 0;
          await Promise.all(Array.from({ length: Math.min(4, pending.length) }, async () => {
            while (pending.length && !controller.signal.aborted) {
              const node = pending.shift()!;
              try {
                const result = await operationRequest<{ preferences: DashboardPreference[] }>("nodes.preferences", { params: { nodeId: node.nodeId } }, { signal: controller.signal });
                const preference = result.preferences.find(p => p.targetKind === "node");
                if (preference) loaded[node.nodeId] = preference;
              } catch { if (!controller.signal.aborted) failures++; }
            }
          }));
          if (controller.signal.aborted) return;
          setPreferences(previous => {
            const next: Record<string, DashboardPreference> = {};
            for (const node of payload.nodes) if (loaded[node.nodeId] ?? previous[node.nodeId]) next[node.nodeId] = loaded[node.nodeId] ?? previous[node.nodeId];
            return next;
          });
          if (failures) setError(t("global.preferenceError"));
          preferenceReadAt = Date.now();
        }
      } catch (reason) { if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : String(reason)); }
      finally { if (!controller.signal.aborted) { setReady(true); setBusy(false); timer = window.setTimeout(read, 10000); } }
    };
    void read();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [epoch]);
  const visible = useMemo(() => nodes.filter(node => node.status !== "revoked" && preferences[node.nodeId]?.visible !== false), [nodes, preferences]);
  const points = useMemo(() => visible.flatMap(node => {
    const position = node.location?.status === "known" ? node.location.position : undefined;
    const code = (position?.countryCode || (node.country?.status === "known" ? node.country.code : ""))?.toUpperCase() ?? "";
    if (!Object.hasOwn(countries, code)) return [];
    if (position && position.latitude != null && position.longitude != null) {
      const location = [position.latitude, position.longitude];
      const id = `${code}-${location.map(value => value.toFixed(4).replace(/-/g, "n").replace(/\./g, "d")).join("-")}`;
      return [{ node, id, code, label: position.city, place: { ...countries[code], location } }];
    }
    return [{ node, id: code, code, label: countryName(code), place: countries[code] }];
  }), [visible, locale.value]);
  const latest = useRef(points); latest.current = points;
  const selected = visible.find(node => node.nodeId === activeId) ?? points[0]?.node ?? visible[0];
  const selectedPoint = points.find(point => point.node.nodeId === selected?.nodeId);
  const selectedRef = useRef(selected?.nodeId); selectedRef.current = selected?.nodeId;
  const tour = useRef({ code: "", since: 0, checked: 0, cursors: new Map<string, number>(), visited: new Map<string, number>() });
  const locationKeys = [...new Set(points.map(point => point.id))].sort().join(",");
  const markers = useMemo<NonNullable<COBEOptions["markers"]>>(() => locationKeys ? locationKeys.split(",").map(id => ({ location: points.find(point => point.id === id)!.place.location as [number, number], size: 0.06, id: `fleet-${id}` })) : [], [locationKeys]);
  const hub = selectedPoint?.id;
  const arcs = useMemo<COBEOptions["arcs"]>(() => hub ? markers.filter(marker => marker.id !== `fleet-${hub}`).map(marker => ({ from: marker.location, to: markers.find(marker => marker.id === `fleet-${hub}`)!.location, id: `fleet-${marker.id}-${hub}` })) : [], [markers, hub]);
  const labels = useRef(new Map<string, HTMLSpanElement>());
  const onFrame = useCallback((state: { phi: number; theta: number }) => {
    for (const point of latest.current) {
      const label = labels.current.get(point.node.nodeId);
      if (label) label.style.visibility = frontScore(point.place.location, state.phi, state.theta) > 0.1 ? "visible" : "hidden";
    }
    const stamp = performance.now(), memory = tour.current;
    if (stamp - memory.checked < 500 || document.hidden) return;
    memory.checked = stamp;
    const visibleRegions = new Map<string, { point: Point; score: number }>();
    for (const point of latest.current) {
      const score = frontScore(point.place.location, state.phi, state.theta ?? 0.3);
      if (score > 0.1 && !visibleRegions.has(point.id)) visibleRegions.set(point.id, { point, score });
    }
    if (visibleRegions.has(memory.code) && stamp - memory.since < 4500) return;
    // Fairness among visible regions also tours southern nodes that share a
    // longitude with a northern region; proximity alone would starve them.
    const candidate = [...visibleRegions.values()].sort((a, b) => (memory.visited.get(a.point.id) ?? 0) - (memory.visited.get(b.point.id) ?? 0) || b.score - a.score)[0];
    if (!candidate) return;
    const best = candidate.point;
    memory.visited.set(best.id, stamp);
    const group = latest.current.filter(point => point.id === best!.id);
    const index = (memory.cursors.get(best.id) ?? 0) % group.length;
    memory.cursors.set(best.id, index + 1); memory.code = best.id; memory.since = stamp;
    const id = group[index].node.nodeId;
    if (selectedRef.current !== id) { selectedRef.current = id; setActiveId(id); }
  }, []);
  useLayoutEffect(() => {
    const root = host.current?.closest<HTMLElement>(".app-shell"); if (!root) return;
    const readColor = (name: string) => {
      const probe = document.createElement("span"); probe.style.color = `var(${name})`; root.append(probe);
      const values = getComputedStyle(probe).color.match(/[\d.]+/g)?.slice(0, 3).map(Number); probe.remove();
      return values?.length === 3 ? values.map(value => value / 255) : [0.5, 0.5, 0.5];
    };
    const update = () => setColors({ dark: root.dataset.theme === "dark" ? 1 : 0, base: readColor(root.dataset.theme === "dark" ? "--figma-text-tertiary" : "--figma-background-primary"), glow: readColor("--figma-background-primary"), marker: readColor("--accent-fill") });
    update(); const observer = new MutationObserver(update); observer.observe(root, { attributes: true, attributeFilter: ["data-theme"] });
    return () => observer.disconnect();
  }, []);
  const config = useMemo<COBEOptions>(() => ({ width: 800, height: 800, devicePixelRatio: 2, phi: 0, theta: 0.3, dark: colors.dark, diffuse: 0.4, mapSamples: 16000, mapBrightness: 1.2, mapBaseBrightness: 0, baseColor: colors.base as [number, number, number], markerColor: colors.marker as [number, number, number], glowColor: colors.glow as [number, number, number], markers, arcs, arcColor: colors.marker as [number, number, number], arcWidth: 0.5, arcHeight: 0.3, markerElevation: 0.012 }), [colors, markers, arcs]);
  const preloadKey = visible.map(node => `${node.nodeId}:${node.generation}`).sort().join(",");
  useEffect(() => {
    const controller = new AbortController();
    const pending = [...visible];
    void Promise.all(Array.from({ length: Math.min(4, pending.length) }, async () => {
      while (pending.length && !controller.signal.aborted) {
        const node = pending.shift()!;
        try {
          const result = await operationRequest<AgentMetricsResponse>("nodes.metrics", { params: { nodeId: node.nodeId } }, { signal: controller.signal });
          if (!controller.signal.aborted && "metrics" in result) setSamples(current => current[node.nodeId] === result ? current : { ...current, [node.nodeId]: result });
        } catch { /* A selected node retries below; keep any last known sample. */ }
      }
    }));
    return () => controller.abort();
  }, [preloadKey, epoch]);
  useEffect(() => {
    if (!selected?.nodeId) return;
    const controller = new AbortController(); let timer = 0;
    const read = async () => {
      if (!document.hidden) try {
        const result = await operationRequest<AgentMetricsResponse>("nodes.metrics", { params: { nodeId: selected.nodeId } }, { signal: controller.signal });
        if (!controller.signal.aborted && "metrics" in result) setSamples(current => ({ ...current, [selected.nodeId]: result }));
      } catch { /* Preserve the cached card while the next refresh retries. */ }
      if (!controller.signal.aborted) timer = window.setTimeout(read, 5000);
    };
    void read(); return () => { controller.abort(); clearTimeout(timer); };
  }, [selected?.nodeId, selected?.generation, epoch]);
  const sample = selected ? samples[selected.nodeId] : undefined;
  const fresh = !!selected && online(selected) && sample?.nodeId === selected.nodeId && sample.generation === selected.generation && sample.activeGeneration === selected.generation && sample.nodeStatus === "online" && Date.parse(sample.leaseValidUntil) > now();
  const metrics = fresh ? sample?.metrics : undefined;
  const memory = known(metrics?.memory), rootDisk = metrics?.disk.mounts.find(mount => mount.mountpoint === "/");
  const disk = known(rootDisk?.usage), network = known(metrics?.network.summary), uptime = known(metrics?.uptime);
  const preference = selected ? preferences[selected.nodeId] : undefined;
  const title = selected ? preference?.alias || selected.displayName : "";
  const go = (node: AgentNode, section = "overview") => onOpenNode(node.nodeId, section);
  const readings = [{ label: "CPU", value: percent(known(metrics?.cpu.usagePercent)) }, { label: t("global.memory"), value: percent(memory?.usedPercent) }, { label: t("global.disk"), value: percent(disk?.usedPercent) }];
  return <ManagementPage className="global-overview" aria-labelledby="global-title">
    <div ref={host} className="global-page-content">
      <header className="global-heading"><div><h1 id="global-title">{t("global.title")}</h1></div><Button variant="secondary" disabled={busy} onClick={() => setEpoch(value => value + 1)}><RefreshCw size={16} />{t("global.refresh")}</Button></header>
      {error ? <Alert tone="warning" title={t("global.refreshError")}>{error}</Alert> : null}
      {!ready ? <Skeleton label={t("global.loading")} lines={4} /> : <>
        <dl className="global-stats">
          {[{ id: "nodes", label: t("global.nodes"), value: visible.length }, { id: "online", label: t("global.online"), value: visible.filter(online).length }, { id: "countries", label: t("global.countriesShort"), value: new Set(points.map(point => point.code)).size }, { id: "continents", label: t("global.continentsShort"), value: new Set(points.map(point => point.place.continent)).size }].map(stat => <div className="global-stat" key={stat.id}><dt>{stat.label}</dt><dd><AnimatedCounter value={stat.value} animateOnView /></dd></div>)}
        </dl>
        <div className="global-layout">
          <Card className="global-map-card" title={null}
            media={<div className="global-orbit" role="img" aria-label={t("global.globeLabel")} data-marker-count={markers.length} data-label-count={points.length} data-metrics-count={Object.keys(samples).length} data-active-country={selectedPoint?.code ?? ""}><GlobeBoundary><Globe className="global-globe-canvas" config={config} onFrame={onFrame}>{points.map(point => {
              const colocated = points.filter(candidate => candidate.id === point.id);
              const stack = colocated.findIndex(candidate => candidate.node.nodeId === point.node.nodeId);
              return <span key={point.node.nodeId}
                ref={element => { if (element) labels.current.set(point.node.nodeId, element); else labels.current.delete(point.node.nodeId); }}
                className="global-marker-label" data-active={point.node.nodeId === selected?.nodeId || undefined}
                style={{ positionAnchor: `--cobe-fleet-${point.id}`, translate: `-50% ${-12 + stack * 30}px` } as CSSProperties}>
                <Badge>{preferences[point.node.nodeId]?.alias || point.node.displayName}</Badge>
              </span>;
            })}</Globe></GlobeBoundary></div>}
            meta={<span className="global-map-caption"><MapPin size={14} aria-hidden="true" />{selectedPoint ? selectedPoint.label : t("global.waitingLocation")}</span>}
            status={selectedPoint ? t(`global.continent.${selectedPoint.place.continent}`) : undefined}>
          </Card>
          <aside className="global-side" aria-label={t("global.currentNode")}>
            {selected ? <Card className="global-node-card" data-node-id={selected.nodeId}
              title={<span className="global-node-title"><CountryLogo country={selected.country} /><span>{title}</span><NodeLogo icon={preference?.icon} platform={metrics?.system.platform.value} os={metrics?.system.os.value} /></span>}
              description={selectedPoint ? `${selectedPoint.label}${selectedPoint.label === countryName(selectedPoint.code) ? "" : `, ${countryName(selectedPoint.code)}`} · ${t(`global.continent.${selectedPoint.place.continent}`)}` : t("country.unknown")}
              meta={selected.agentVersion || t("global.pending")} status={t("global.following")}
              action={<Button variant="secondary" onClick={() => go(selected)}>{t("global.openNode")}<ArrowUpRight size={16} /></Button>}>
              <div className="global-node-state"><Badge tone={online(selected) ? "success" : "neutral"}>{online(selected) ? t("global.online") : selected.agentId ? t("global.offline") : t("global.pending")}</Badge>{!fresh ? <span>{t("global.noCurrentMetrics")}</span> : null}</div>
              <div className="global-readings">{readings.map(reading => reading.value != null ? <Progress key={reading.label} label={reading.label} value={reading.value} showValue /> : <p key={reading.label} className="global-missing"><span>{reading.label}</span><span>—</span></p>)}</div>
              <dl className="global-node-facts">
                <div><dt>{t("global.memory")}</dt><dd>{formatBytes(memory?.usedBytes)} / {formatBytes(memory?.totalBytes)}</dd></div>
                <div><dt>{t("global.disk")}</dt><dd>{formatBytes(disk?.usedBytes)} / {formatBytes(disk?.totalBytes)}</dd></div>
                <div><dt>{t("global.download")}</dt><dd>{network ? `${formatBytes(network.receivedBytesPerSecond)}/s` : "—"}</dd></div>
                <div><dt>{t("global.upload")}</dt><dd>{network ? `${formatBytes(network.sentBytesPerSecond)}/s` : "—"}</dd></div>
                <div><dt>{t("global.uptime")}</dt><dd>{uptime ? t("global.hours", { "0": Math.floor(uptime.seconds / 3600) }) : "—"}</dd></div>
              </dl>
            </Card> : <EmptyState icon={<Server />} title={t("global.empty")} description={t("global.emptyHint")} />}
            {!points.length && visible.length ? <Alert tone="info" title={t("global.waitingLocation")}>{t("global.locationHint")}</Alert> : null}
            {visible.length > points.length ? <Card title={t("global.unlocated")} description={t("global.locationHint")}><div className="global-unlocated">{visible.filter(node => !points.some(point => point.node.nodeId === node.nodeId)).map(node => <Button key={node.nodeId} variant="ghost" onClick={() => go(node, "settings")}>{preferences[node.nodeId]?.alias || node.displayName}<ArrowUpRight size={14} /></Button>)}</div></Card> : null}
          </aside>
        </div>
      </>}
    </div>
  </ManagementPage>;
}
