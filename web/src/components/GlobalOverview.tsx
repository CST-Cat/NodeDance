import { Component, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import type { COBEOptions } from "cobe";
import { Globe2, Server } from "lucide-react";
import { operationRequest, type AgentNode, type AgentNodesResponse, type DashboardPreference, type AgentMetricsResponse } from "../api";
import type { Metric, MetricsView } from "../metrics-contract";
import { formatBytes } from "../metric-format";
import { locale, t } from "../i18n";
import locations from "../country-locations.json";
import { ManagementPage, Card, Alert } from "./ManagementUI";
import { AnimatedCounter } from "./arc/animated-counter/animated-counter";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { motionTokens } from "./arc/motion-tokens";
import { Separator } from "./ui/separator";
import { ScrollArea } from "./arc/scroll-area/scroll-area";
import { Skeleton } from "./arc/skeleton/skeleton";
import { EmptyState } from "./arc/empty-state/empty-state";
import { Globe } from "./ui/globe";
import CountryLogo from "./CountryLogo";
import { Progress } from "./arc/progress/progress";
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
const countryShorts: Record<string, string> = {
  "AW": "ABW", "AF": "AFG", "AO": "AGO", "AI": "AIA", "AX": "ALA", "AL": "ALB", "AD": "AND", "AE": "ARE",
  "AR": "ARG", "AM": "ARM", "AS": "ASM", "AQ": "ATA", "TF": "ATF", "AG": "ATG", "AU": "AUS", "AT": "AUT",
  "AZ": "AZE", "BI": "BDI", "BE": "BEL", "BJ": "BEN", "BF": "BFA", "BD": "BGD", "BG": "BGR", "BH": "BHR",
  "BS": "BHS", "BA": "BIH", "BL": "BLM", "SH": "SHN", "BY": "BLR", "BZ": "BLZ", "BM": "BMU", "BO": "BOL",
  "BQ": "BES", "BR": "BRA", "BB": "BRB", "BN": "BRN", "BT": "BTN", "BV": "BVT", "BW": "BWA", "CF": "CAF",
  "CA": "CAN", "CC": "CCK", "CH": "CHE", "CL": "CHL", "CN": "CHN", "CI": "CIV", "CM": "CMR", "CD": "COD",
  "CG": "COG", "CK": "COK", "CO": "COL", "KM": "COM", "CV": "CPV", "CR": "CRI", "CU": "CUB", "CW": "CUW",
  "CX": "CXR", "KY": "CYM", "CY": "CYP", "CZ": "CZE", "DE": "DEU", "DJ": "DJI", "DM": "DMA", "DK": "DNK",
  "DO": "DOM", "DZ": "DZA", "EC": "ECU", "EG": "EGY", "ER": "ERI", "EH": "ESH", "ES": "ESP", "EE": "EST",
  "ET": "ETH", "FI": "FIN", "FJ": "FJI", "FK": "FLK", "FR": "FRA", "FO": "FRO", "FM": "FSM", "GA": "GAB",
  "GB": "UK", "GE": "GEO", "GG": "GGY", "GH": "GHA", "GI": "GIB", "GN": "GIN", "GP": "GLP", "GM": "GMB",
  "GW": "GNB", "GQ": "GNQ", "GR": "GRC", "GD": "GRD", "GL": "GRL", "GT": "GTM", "GF": "GUF", "GU": "GUM",
  "GY": "GUY", "HK": "HKG", "HM": "HMD", "HN": "HND", "HR": "HRV", "HT": "HTI", "HU": "HUN", "ID": "IDN",
  "IM": "IMN", "IN": "IND", "IO": "IOT", "IE": "IRL", "IR": "IRN", "IQ": "IRQ", "IS": "ISL", "IL": "ISR",
  "IT": "ITA", "JM": "JAM", "JE": "JEY", "JO": "JOR", "JP": "JPN", "KZ": "KAZ", "KE": "KEN", "KG": "KGZ",
  "KH": "KHM", "KI": "KIR", "KN": "KNA", "KR": "KOR", "XK": "XK", "KW": "KWT", "LA": "LAO", "LB": "LBN",
  "LR": "LBR", "LY": "LBY", "LC": "LCA", "LI": "LIE", "LK": "LKA", "LS": "LSO", "LT": "LTU", "LU": "LUX",
  "LV": "LVA", "MO": "MAC", "MF": "MAF", "MA": "MAR", "MC": "MCO", "MD": "MDA", "MG": "MDG", "MV": "MDV",
  "MX": "MEX", "MH": "MHL", "MK": "MKD", "ML": "MLI", "MT": "MLT", "MM": "MMR", "ME": "MNE", "MN": "MNG",
  "MP": "MNP", "MZ": "MOZ", "MR": "MRT", "MS": "MSR", "MQ": "MTQ", "MU": "MUS", "MW": "MWI", "MY": "MYS",
  "YT": "MYT", "NA": "NAM", "NC": "NCL", "NE": "NER", "NF": "NFK", "NG": "NGA", "NI": "NIC", "NU": "NIU",
  "NL": "NLD", "NO": "NOR", "NP": "NPL", "NR": "NRU", "NZ": "NZL", "OM": "OMN", "PK": "PAK", "PA": "PAN",
  "PN": "PCN", "PE": "PER", "PH": "PHL", "PW": "PLW", "PG": "PNG", "PL": "POL", "PR": "PRI", "KP": "PRK",
  "PT": "PRT", "PY": "PRY", "PS": "PSE", "PF": "PYF", "QA": "QAT", "RE": "REU", "RO": "ROU", "RU": "RUS",
  "RW": "RWA", "SA": "SAU", "SD": "SDN", "SN": "SEN", "SG": "SGP", "GS": "SGS", "SJ": "SJM", "SB": "SLB",
  "SL": "SLE", "SV": "SLV", "SM": "SMR", "SO": "SOM", "PM": "SPM", "RS": "SRB", "SS": "SSD", "ST": "STP",
  "SR": "SUR", "SK": "SVK", "SI": "SVN", "SE": "SWE", "SZ": "SWZ", "SX": "SXM", "SC": "SYC", "SY": "SYR",
  "TC": "TCA", "TD": "TCD", "TG": "TGO", "TH": "THA", "TJ": "TJK", "TK": "TKL", "TM": "TKM", "TL": "TLS",
  "TO": "TON", "TT": "TTO", "TN": "TUN", "TR": "TUR", "TV": "TUV", "TW": "TWN", "TZ": "TZA", "UG": "UGA",
  "UA": "UKR", "UM": "UMI", "UY": "URY", "US": "USA", "UZ": "UZB", "VA": "VAT", "VC": "VCT", "VE": "VEN",
  "VG": "VGB", "VI": "VIR", "VN": "VNM", "VU": "VUT", "WF": "WLF", "WS": "WSM", "YE": "YEM", "ZA": "ZAF",
  "ZM": "ZMB", "ZW": "ZWE"
};
function countryShort(code: string) {
  return countryShorts[code] ?? code;
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
  const [ready, setReady] = useState(false), [error, setError] = useState("");
  const [activeId, setActiveId] = useState("");
  const matrixGrid = useRef<HTMLDivElement>(null);
  const [rowBudget, setRowBudget] = useState(0), [matrixMax, setMatrixMax] = useState(640);
  const [samples, setSamples] = useState<Record<string, MetricsView>>({});
  const [colors, setColors] = useState({ dark: 0, base: [0.5, 0.5, 0.5], glow: [1, 1, 1], marker: [0.5, 0.5, 0.5] });
  const reduced = useReducedMotion();
  const metricReads = useRef(new Map<string, { controller: AbortController; promise: Promise<AgentMetricsResponse> }>());
  const readMetrics = useCallback((node: AgentNode) => {
    const key = `${node.nodeId}:${node.generation}`;
    const existing = metricReads.current.get(key);
    if (existing) return existing.promise;
    const controller = new AbortController();
    const promise = operationRequest<AgentMetricsResponse>("nodes.metrics", { params: { nodeId: node.nodeId } }, { signal: controller.signal }).finally(() => {
      if (metricReads.current.get(key)?.promise === promise) metricReads.current.delete(key);
    });
    metricReads.current.set(key, { controller, promise });
    return promise;
  }, []);
  useEffect(() => () => {
    for (const read of metricReads.current.values()) read.controller.abort();
    metricReads.current.clear();
  }, []);
  const clockOffset = useRef(0);
  const now = () => Date.now() + clockOffset.current;
  const online = (node: AgentNode) => node.status === "online" && (!node.leaseValidUntil || Date.parse(node.leaseValidUntil) > now());
  useEffect(() => {
    const controller = new AbortController(); let timer = 0, preferenceReadAt = 0;
    const read = async () => {
      if (document.hidden) { timer = window.setTimeout(read, 5000); return; }
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
      finally { if (!controller.signal.aborted) { setReady(true); timer = window.setTimeout(read, 10000); } }
    };
    void read();
    return () => { controller.abort(); clearTimeout(timer); };
  }, []);
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
  const scores = useRef(new Map<string, number>());
  const [frontIds, setFrontIds] = useState<string[]>([]);
  const arcs = useMemo<COBEOptions["arcs"]>(() => {
    if (!hub || !selectedPoint) return [];
    const frontPlaces = new Map<string, [number, number]>();
    for (const id of frontIds) { const point = points.find(candidate => candidate.node.nodeId === id); if (point && point.id !== hub) frontPlaces.set(point.id, point.place.location as [number, number]); }
    return [...frontPlaces].map(([id, location]) => ({ from: location, to: selectedPoint.place.location as [number, number], id: `fleet-${id}-${hub}` }));
  }, [points, frontIds, hub, selectedPoint]);
  useEffect(() => {
    const update = () => setFrontIds(current => {
      const pointIds = new Set(latest.current.map(point => point.node.nodeId));
      const members = new Set(current.filter(id => pointIds.has(id)));
      const pinned = selectedRef.current && pointIds.has(selectedRef.current) ? selectedRef.current : "";
      const raw = (id: string) => scores.current.get(id) ?? 0;
      const ranked = latest.current.map(point => point.node.nodeId).filter(id => raw(id) > 0.1 || (members.has(id) && raw(id) > 0.02)).sort((a, b) => raw(b) + (members.has(b) ? .05 : 0) - raw(a) - (members.has(a) ? .05 : 0));
      const chosen = new Set(ranked);
      if (pinned) chosen.add(pinned);
      const ordered = [...chosen].sort((a, b) => raw(b) - raw(a));
      return ordered.join() === current.join() ? current : ordered;
    });
    update();
    const timer = window.setInterval(update, 700);
    return () => window.clearInterval(timer);
  }, [locationKeys]);
  const labels = useRef(new Map<string, HTMLSpanElement>());
  const onFrame = useCallback((state: { phi: number; theta: number }) => {
    for (const point of latest.current) {
      const label = labels.current.get(point.node.nodeId);
      if (label) label.style.visibility = frontScore(point.place.location, state.phi, state.theta) > 0.1 ? "visible" : "hidden";
    }
    const stamp = performance.now(), memory = tour.current;
    if (stamp - memory.checked < 500 || document.hidden) return;
    memory.checked = stamp;
    for (const point of latest.current) scores.current.set(point.node.nodeId, frontScore(point.place.location, state.phi, state.theta ?? 0.3));
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
          const result = await readMetrics(node);
          if (!controller.signal.aborted && "metrics" in result) setSamples(current => current[node.nodeId] === result ? current : { ...current, [node.nodeId]: result });
        } catch { /* A selected node retries below; keep any last known sample. */ }
      }
    }));
    return () => controller.abort();
  }, [preloadKey, readMetrics]);
  useEffect(() => {
    if (!selected?.nodeId) return;
    const controller = new AbortController(); let timer = 0;
    const read = async () => {
      if (!document.hidden) try {
        const result = await readMetrics(selected);
        if (!controller.signal.aborted && "metrics" in result) setSamples(current => current[selected.nodeId] === result ? current : ({ ...current, [selected.nodeId]: result }));
      } catch { /* Preserve the cached card while the next refresh retries. */ }
      if (!controller.signal.aborted) timer = window.setTimeout(read, 5000);
    };
    void read(); return () => { controller.abort(); clearTimeout(timer); };
  }, [selected?.nodeId, selected?.generation, readMetrics]);
  const go = (node: AgentNode, section = "overview") => onOpenNode(node.nodeId, section);
  const freshSample = (node: AgentNode) => {
    const sample = samples[node.nodeId];
    return online(node) && sample?.nodeId === node.nodeId && sample.generation === node.generation && sample.activeGeneration === node.generation && sample.nodeStatus === "online" && Date.parse(sample.leaseValidUntil) > now() ? sample : undefined;
  };
  const buildReadings = (node: AgentNode) => {
    const metrics = freshSample(node)?.metrics;
    const memory = known(metrics?.memory), rootDisk = metrics?.disk.mounts.find(mount => mount.mountpoint === "/");
    const disk = known(rootDisk?.usage);
    return [
      { resource: "cpu", label: "CPU", value: percent(known(metrics?.cpu.usagePercent)), detail: `${known(metrics?.cpu.logicalCores) ?? "—"} ${t("overview.cores")}` },
      { resource: "memory", label: t("global.memory"), value: percent(memory?.usedPercent), detail: `${formatBytes(memory?.usedBytes)} / ${formatBytes(memory?.totalBytes)}` },
      { resource: "disk", label: t("global.disk"), value: percent(disk?.usedPercent), detail: `${formatBytes(disk?.usedBytes)} / ${formatBytes(disk?.totalBytes)}` },
    ];
  };
  const frontPoints = frontIds.map(id => points.find(point => point.node.nodeId === id)).filter(point => point != null);
  useEffect(() => {
    const measure = () => {
      const grid = matrixGrid.current, scroll = host.current?.querySelector(".global-matrix-scroll"), cellEl = grid?.querySelector(".global-cell");
      if (!grid || !scroll || !cellEl) return;
      const cellHeight = (cellEl as HTMLElement).offsetHeight;
      if (!cellHeight) return;
      const top = (scroll as HTMLElement).getBoundingClientRect().top + window.scrollY;
      const available = Math.max(160, window.innerHeight - top - 24);
      setMatrixMax(available);
      setRowBudget(Math.max(1, Math.floor((available + 12) / (cellHeight + 12))));
    };
    const frame = requestAnimationFrame(measure);
    window.addEventListener("resize", measure);
    return () => { cancelAnimationFrame(frame); window.removeEventListener("resize", measure); };
  }, [frontPoints.length, ready]);
  const pairRows = rowBudget ? Math.min(Math.max(frontPoints.length - rowBudget, 0), Math.floor(frontPoints.length / 2)) : 0;
  const soloCount = frontPoints.length - pairRows * 2;
  return <ManagementPage className="global-overview" aria-label={t("global.title")}>
    <div ref={host} className="global-page-content">
      {error ? <Alert tone="warning" title={t("global.refreshError")}>{error}</Alert> : null}
      {!ready ? <Skeleton label={t("global.loading")} lines={4} /> : <>
        <div className="global-layout">
          <div className="global-map">
            <div className="global-orbit" role="img" aria-label={t("global.globeLabel")} data-marker-count={markers.length} data-label-count={points.length} data-metrics-count={Object.keys(samples).length} data-active-country={selectedPoint?.code ?? ""}><GlobeBoundary><Globe className="global-globe-canvas" config={config} onFrame={onFrame}>{points.map(point => {
              const colocated = points.filter(candidate => candidate.id === point.id);
              const stack = colocated.findIndex(candidate => candidate.node.nodeId === point.node.nodeId);
              return <span key={point.node.nodeId}
                ref={element => { if (element) labels.current.set(point.node.nodeId, element); else labels.current.delete(point.node.nodeId); }}
                className="global-marker-label" data-active={point.node.nodeId === selected?.nodeId || undefined}
                style={{ positionAnchor: `--cobe-fleet-${point.id}`, translate: `-50% ${-12 + stack * 30}px` } as CSSProperties}>
                <span>{preferences[point.node.nodeId]?.alias || point.node.displayName}</span>
              </span>;
            })}</Globe></GlobeBoundary></div>
          </div>
          <aside className="global-side" aria-label={t("global.currentNode")}>
            {points.length ? <Card className="global-node-card" title={null}>
              <dl className="global-stats">
                {[{ id: "nodes", label: t("global.nodes"), value: visible.length }, { id: "online", label: t("global.online"), value: visible.filter(online).length }, { id: "countries", label: t("global.countriesShort"), value: new Set(points.map(point => point.code)).size }, { id: "continents", label: t("global.continentsShort"), value: new Set(points.map(point => point.place.continent)).size }].map(stat => <div className="global-stat" key={stat.id}><dt>{stat.label}</dt><dd><AnimatedCounter value={stat.value} animateOnView /></dd></div>)}
              </dl>
              <Separator className="global-stats-separator" />
              <ScrollArea className="global-matrix-scroll" maxHeight={matrixMax} fade={0}>
              <div className="global-matrix" ref={matrixGrid}>
                <AnimatePresence mode="popLayout">
                  {frontPoints.map((point, cellIndex) => {
                    const node = point.node, preference = preferences[node.nodeId];
                    return <motion.section key={node.nodeId} className={cellIndex < soloCount ? "global-cell global-cell-solo" : "global-cell"} data-node-id={node.nodeId} data-focused={node.nodeId === selected?.nodeId || undefined}
                      layout={!reduced} initial={{ opacity: 0, scale: .96, y: 8 }} animate={{ opacity: 1, scale: 1, y: 0 }} exit={{ opacity: 0, scale: .96, transition: { duration: motionTokens.duration.exit, ease: [...motionTokens.ease.exit] as [number, number, number, number] } }}
                      transition={{ layout: motionTokens.spring.smooth, duration: motionTokens.duration.standard, ease: [...motionTokens.ease.enter] as [number, number, number, number] }}>
                      <div className="global-cell-head"><CountryLogo country={node.country} /><button type="button" className="global-cell-name" onClick={() => go(node)}>{preference?.alias || node.displayName}</button></div>
                      <p className="global-cell-desc">{point.label === countryName(point.code) ? countryShort(point.code) : `${point.label}, ${countryShort(point.code)}`}</p>
                      <div className="global-readings">{buildReadings(node).map(reading => { const pct = reading.value !== null && Number.isFinite(reading.value) && reading.value >= 0 ? Math.min(100, reading.value) : null; return <div key={reading.resource} className="global-bar" data-resource={reading.resource} data-state={pct === null ? "unknown" : "known"}><div className="global-bar-head"><span>{reading.label}</span><strong>{pct === null ? "—" : pct.toFixed(1) + "%"}</strong></div><Progress value={pct ?? 0} /><small>{reading.detail}</small></div>; })}</div>
                    </motion.section>;
                  })}
                </AnimatePresence>
              </div>
              </ScrollArea>
            </Card> : <EmptyState icon={<Server />} title={t("global.empty")} description={t("global.emptyHint")} />}
            {!points.length && visible.length ? <Alert tone="info" title={t("global.waitingLocation")}>{t("global.locationHint")}</Alert> : null}
          </aside>
        </div>
      </>}
    </div>
  </ManagementPage>;
}
