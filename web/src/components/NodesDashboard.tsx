import UiLoadState from "./UiLoadState";
import { Fragment } from "react";
import {
  api,
  type AgentNode,
  type AgentNodesResponse,
  type ContainerCreateSpec,
  type ContainerTask,
  type ContainerTaskAction,
  type CreateContainerTaskPayload,
  type DashboardPreference,
  type DashboardSettings,
  type DockerInventory,
  type DockerInventoryMessage,
  type NodeStatusResponse,
  type SSHConnectionInfo,
} from "../api";
import { display, locale, t } from "../i18n";
import type { MetricsView } from "../metrics-contract";
import { useViewNavigation } from "../navigation";
import { operationURL } from "../operations";
import {
  __call,
  __classes,
  __each,
  __input,
  __style,
  afterMount,
  beforeDispose,
  cell,
  derived,
  lazyView,
  useController,
  watchState,
} from "../state";
import NodeDetail from "./NodeDetail";
import "./NodesDashboard.css";
import OverviewContext from "./OverviewContext";
import UiIcon from "./UiIcon";
import UiSelect from "./UiSelect";
import VpsCard from "./VpsCard";

const TerminalConsole = lazyView(() => import("./TerminalConsole"));
type Props = { readOnly?: boolean } & { children?: import("react").ReactNode; [key: string]: any };
export default function NodesDashboard(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const terminalNode = cell<AgentNode | null>(null);
      const emit = __emit;
      const preferencesOpen = cell(false);
      const inspectedNodeID = cell("");
      const inspectedNode = derived(() =>
        nodes.value.find((node) => node.nodeId === inspectedNodeID.value),
      );
      function inspectNode(node: AgentNode) {
        inspectedNodeID.value = node.nodeId;
      }
      let inspectFrame = 0;
      function followVisibleNode() {
        if (inspectFrame || selectedNode.value) return;
        inspectFrame = requestAnimationFrame(() => {
          inspectFrame = 0;
          if (window.scrollY < 60) {
            inspectedNodeID.value = "";
            return;
          }
          const anchor = Math.min(innerHeight * 0.3, 240);
          const cards = Array.from(
            document.querySelectorAll<HTMLElement>(
              ".nodes-dashboard .vps-card",
            ),
          )
            .map((element) => ({
              id: element.dataset.nodeId,
              rect: element.getBoundingClientRect(),
            }))
            .filter(
              (item) =>
                item.rect.bottom > 80 && item.rect.top < innerHeight - 80,
            )
            .sort(
              (a, b) =>
                Math.abs(Math.max(a.rect.top, 80) - anchor) -
                Math.abs(Math.max(b.rect.top, 80) - anchor),
            );
          if (cards[0]?.id) inspectedNodeID.value = cards[0].id;
        });
      }
      const nodeSummary = derived(() => [
        {
          label: "dashboard.total",
          value: nodes.value.length,
          kind: "total",
        },
        {
          label: "dashboard.online",
          value: nodes.value.filter(nodeIsOnline).length,
          kind: "online",
        },
        {
          label: "dashboard.offline",
          value: nodes.value.filter(
            (node) =>
              !nodeIsOnline(node) &&
              !isPendingRegistration(node) &&
              node.status !== "revoked",
          ).length,
          kind: "offline",
        },
        {
          label: "dashboard.pending",
          value: nodes.value.filter(isPendingRegistration).length,
          kind: "pending",
        },
      ]);
      interface NodeClock {
        status: string;
        generation: number;
        serverTime: string;
        leaseValidUntil?: string;
        startedAt: number;
      }
      const nodes = cell<AgentNode[]>([]);
      const viewNavigation = useViewNavigation();
      const selectedNodeID = derived(
        () => viewNavigation.route.value.nodeId ?? "",
      );
      const views = cell<Record<string, MetricsView>>({});
      const dockerViews = cell<Record<string, DockerInventory>>({});
      const clocks = cell<Record<string, NodeClock>>({});
      const nodeReasons = cell<Record<string, string>>({});
      const taskViews = cell<Record<string, ContainerTask>>({});
      const taskSubmitting = cell<Record<string, boolean>>({});
      const taskErrors = cell<Record<string, string>>({});
      const preferenceIdentities = cell<Record<string, Record<string, string>>>(
        {},
      );
      const sshUserChoices = cell<Record<string, string>>({});
      const sshConnections = cell<
        Record<
          string,
          {
            info?: SSHConnectionInfo;
            error: string;
            loading: boolean;
            at: number;
            generation: number;
          }
        >
      >({});
      const nodePreferences = cell<Record<string, DashboardPreference[]>>({});
      const dashboardSettings = cell<DashboardSettings>({
        viewMode: "monitor",
        groupBy: "node",
        sortBy: "custom",
        nodeGroupBy: "status",
        nodeSortBy: "custom",
        customFields: ["state", "ports", "health"],
      });
      const serverSearch = cell("");
      const statusFilter = cell<
        "all" | "online" | "pending" | "offline" | "revoked" | "hidden"
      >("all");
      const dashboardSettingsSaving = cell(false);
      const dashboardSettingsMessage = cell("");
      const dashboardSettingsError = cell("");
      const busy = cell(true);
      const nodesReady = cell(false);
      const error = cell("");
      const elapsed = cell(0);
      const wallClockNow = cell(Date.now());
      const socketState = cell<"connecting" | "connected" | "retrying">(
        "connecting",
      );
      let socket: WebSocket | undefined;
      let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
      let refreshTimer: ReturnType<typeof setInterval> | undefined;
      let tickTimer: ReturnType<typeof setInterval> | undefined;
      let taskRefreshTimer: ReturnType<typeof setInterval> | undefined;
      let reconnectDelay = 1000;
      let disposed = false;
      let latestNodeListServerTime = Number.NEGATIVE_INFINITY;
      const selectedNode = derived(
        () =>
          nodes.value.find((node) => node.nodeId === selectedNodeID.value) ??
          null,
      );
      function remainingLease(clock: NodeClock): number {
        if (!clock.leaseValidUntil) return 0;
        const from = Date.parse(clock.serverTime),
          until = Date.parse(clock.leaseValidUntil);
        return Number.isFinite(from) && Number.isFinite(until)
          ? Math.max(0, until - from)
          : 0;
      }
      function nodeIsOnline(node: AgentNode): boolean {
        const clock = clocks.value[node.nodeId];
        if (!clock) return node.status === "online";
        return (
          clock.status === "online" &&
          elapsed.value - clock.startedAt < remainingLease(clock)
        );
      }
      function isPendingRegistration(node: AgentNode): boolean {
        return node.status === "pending" || !node.agentId;
      }
      function dockerIsStale(inventory: DockerInventory): boolean {
        if (inventory.dataStale || !inventory.agentOnline) return true;
        const clock = clocks.value[inventory.nodeId],
          node = nodes.value.find((item) => item.nodeId === inventory.nodeId);
        return (
          !clock ||
          clock.status !== "online" ||
          clock.generation !== inventory.activeGeneration ||
          Boolean(node?.agentId && inventory.agentId !== node.agentId) ||
          elapsed.value - clock.startedAt >= remainingLease(clock)
        );
      }
      function setNodeReason(nodeID: string, reason?: string) {
        if (reason) {
          nodeReasons.value = {
            ...nodeReasons.value,
            [nodeID]: reason,
          };
          return;
        }
        if (!(nodeID in nodeReasons.value)) return;
        const next = {
          ...nodeReasons.value,
        };
        delete next[nodeID];
        nodeReasons.value = next;
      }
      function setClock(
        nodeID: string,
        status: string,
        serverTime: string,
        leaseValidUntil?: string,
        generation = 0,
      ) {
        const incomingTime = Date.parse(serverTime);
        if (!Number.isFinite(incomingTime)) return false;
        const previous = clocks.value[nodeID];
        if (previous) {
          if (generation < previous.generation) return false;
          const previousTime = Date.parse(previous.serverTime);
          if (
            generation === previous.generation &&
            Number.isFinite(previousTime) &&
            incomingTime <= previousTime
          )
            return false;
        }
        clocks.value = {
          ...clocks.value,
          [nodeID]: {
            status,
            serverTime,
            leaseValidUntil,
            generation,
            startedAt: performance.now(),
          },
        };
        return true;
      }
      function mergeNodeList(
        payload: AgentNodesResponse,
      ): AgentNode[] | undefined {
        const listServerTime = Date.parse(payload.serverTime);
        if (
          !Number.isFinite(listServerTime) ||
          listServerTime < latestNodeListServerTime
        )
          return undefined;
        latestNodeListServerTime = listServerTime;
        const previousNodes = new Map(
          nodes.value.map((node) => [node.nodeId, node]),
        );
        const merged = payload.nodes.map((node) => {
          const clock = clocks.value[node.nodeId],
            clockTime = clock
              ? Date.parse(clock.serverTime)
              : Number.NEGATIVE_INFINITY;
          const stale = Boolean(
            clock &&
              (node.generation < clock.generation ||
                (node.generation === clock.generation &&
                  Number.isFinite(clockTime) &&
                  listServerTime <= clockTime)),
          );
          if (!stale) {
            setClock(
              node.nodeId,
              node.status,
              payload.serverTime,
              node.leaseValidUntil,
              node.generation,
            );
            return node;
          }
          return {
            ...node,
            status: clock!.status,
            generation: clock!.generation,
            leaseValidUntil: clock!.leaseValidUntil ?? node.leaseValidUntil,
            lastSeen: previousNodes.get(node.nodeId)?.lastSeen ?? node.lastSeen,
          };
        });
        const included = new Set(merged.map((node) => node.nodeId));
        for (const node of nodes.value)
          if (
            !included.has(node.nodeId) &&
            clocks.value[node.nodeId] &&
            Date.parse(clocks.value[node.nodeId].serverTime) > listServerTime
          )
            merged.push(node);
        return merged;
      }
      async function loadMetrics(node: AgentNode) {
        try {
          acceptMetricsResponse(await api.nodeMetrics(node.nodeId));
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : t("m.433d33e2e1b8");
        }
      }
      async function loadContainers(node: AgentNode) {
        try {
          acceptDockerResponse(await api.nodeContainers(node.nodeId));
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : t("m.f7f31f8592ee");
        }
      }
      async function loadTasks(node: AgentNode) {
        if (__props.readOnly) return;
        try {
          const result = await api.nodeTasks(node.nodeId),
            next = {
              ...taskViews.value,
            };
          for (const [id, task] of Object.entries(next))
            if (task.nodeId === node.nodeId) delete next[id];
          for (const task of result.tasks) next[task.taskId] = task;
          taskViews.value = next;
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : t("m.f63256fb13c3");
        }
      }
      async function loadPreferences(node: AgentNode) {
        try {
          nodePreferences.value = {
            ...nodePreferences.value,
            [node.nodeId]: (await api.nodePreferences(node.nodeId)).preferences,
          };
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : t("m.1e9242469345");
        }
      }
      async function loadSSH(node: AgentNode, force = false) {
        if (__props.readOnly) return;
        const previous = sshConnections.value[node.nodeId];
        if (
          previous?.loading ||
          (!force &&
            previous?.generation === node.generation &&
            Date.now() - previous.at < 60000)
        )
          return;
        sshConnections.value = {
          ...sshConnections.value,
          [node.nodeId]: {
            ...previous,
            error: "",
            loading: true,
            at: Date.now(),
            generation: node.generation ?? 0,
          },
        };
        try {
          const result = await api.sshConnection(node.nodeId);
          if (
            disposed ||
            !nodes.value.some((value) => value.nodeId === node.nodeId)
          )
            return;
          if (
            sshUserChoices.value[node.nodeId] &&
            !result.ssh?.commands?.[sshUserChoices.value[node.nodeId]!]
          )
            delete sshUserChoices.value[node.nodeId];
          sshConnections.value = {
            ...sshConnections.value,
            [node.nodeId]: {
              info: result.ssh,
              error: result.ssh?.reason ?? "",
              loading: false,
              at: Date.now(),
              generation: node.generation ?? 0,
            },
          };
        } catch (reason) {
          if (
            disposed ||
            !nodes.value.some((value) => value.nodeId === node.nodeId)
          )
            return;
          sshConnections.value = {
            ...sshConnections.value,
            [node.nodeId]: {
              ...previous,
              error: reason instanceof Error ? reason.message : String(reason),
              loading: false,
              at: Date.now(),
              generation: node.generation ?? 0,
            },
          };
        }
      }
      function chooseSSHUser(payload: { nodeId: string; user: string }) {
        sshUserChoices.value = {
          ...sshUserChoices.value,
          [payload.nodeId]: payload.user,
        };
      }
      function discoverSSHForNode(node: AgentNode) {
        void loadSSH(node, true);
      }
      async function loadSSHGroup(list: AgentNode[]) {
        const queue = list.filter((node) => node.agentId && nodeIsOnline(node));
        await Promise.all(
          Array.from(
            {
              length: Math.min(4, queue.length),
            },
            async () => {
              while (queue.length && !disposed) {
                const node = queue.shift();
                if (node) await loadSSH(node);
              }
            },
          ),
        );
      }
      async function loadDashboardSettings() {
        try {
          dashboardSettings.value = await api.dashboardSettings();
        } catch (reason) {
          dashboardSettingsError.value =
            reason instanceof Error ? reason.message : t("m.22d4c52f5770");
        }
      }
      async function saveDashboardSettings() {
        dashboardSettingsSaving.value = true;
        dashboardSettingsMessage.value = "";
        dashboardSettingsError.value = "";
        try {
          await api.saveDashboardSettings(dashboardSettings.value);
          dashboardSettingsMessage.value = t("m.5729552b2210");
        } catch (reason) {
          dashboardSettingsError.value =
            reason instanceof Error ? reason.message : t("m.567fd1d27a30");
        } finally {
          dashboardSettingsSaving.value = false;
        }
      }
      async function refreshNodes() {
        if (disposed) return;
        busy.value = true;
        let payload;
        try { payload = await api.nodes(); } finally { if (!disposed) busy.value = false; }
        if (disposed) return;
        const merged = mergeNodeList(payload);
        if (!merged) {
          busy.value = false;
          return;
        }
        nodes.value = merged;
        nodesReady.value = true;
        if (
          selectedNodeID.value &&
          !merged.some((node) => node.nodeId === selectedNodeID.value)
        )
          viewNavigation.go(
            {
              screen: "nodes",
            },
            true,
          );
        busy.value = false;
        const requests: Promise<unknown>[] = [];
        for (const node of merged) {
          const selected = node.nodeId === selectedNodeID.value;
          if (!(node.nodeId in nodePreferences.value))
            requests.push(loadPreferences(node));
          if (!node.agentId) continue;
          if (selected || !(node.nodeId in views.value))
            requests.push(loadMetrics(node));
          if (selected || !(node.nodeId in dockerViews.value))
            requests.push(loadContainers(node));
          requests.push(loadTasks(node));
        }
        await Promise.all([...requests, loadSSHGroup(merged)]);
      }
      function nodePreference(
        node: AgentNode,
      ): DashboardPreference | undefined {
        return nodePreferences.value[node.nodeId]?.find(
          (item) =>
            item.targetKind === "node" &&
            item.identity === `node:${node.nodeId}`,
        );
      }
      function nodeTitle(node: AgentNode): string {
        return nodePreference(node)?.alias || node.displayName;
      }
      function nodeStatusGroup(node: AgentNode): string {
        if (nodeIsOnline(node)) return t("m.b9086662b1df");
        if (isPendingRegistration(node)) return t("m.a890b3be09aa");
        return node.status === "revoked"
          ? t("m.fef96e34f77b")
          : t("m.be1b4f3c6c1c");
      }
      const serverCardGroups = derived(() => {
        const query = serverSearch.value.trim().toLocaleLowerCase();
        const matching = nodes.value.filter((node) => {
          const preference = nodePreference(node),
            hostname = views.value[node.nodeId]?.metrics.system.hostname.value,
            status = nodeStatusGroup(node);
          const hidden = preference?.visible === false;
          if (hidden !== (statusFilter.value === "hidden")) return false;
          if (statusFilter.value === "hidden")
            return (
              !query ||
              [
                node.displayName,
                nodeTitle(node),
                node.nodeId,
                preference?.group,
                preference?.notes,
                hostname,
                status,
              ].some((value) => value?.toLocaleLowerCase().includes(query))
            );
          if (statusFilter.value === "online" && status !== t("m.b9086662b1df"))
            return false;
          if (
            statusFilter.value === "pending" &&
            status !== t("m.a890b3be09aa")
          )
            return false;
          if (
            statusFilter.value === "offline" &&
            status !== t("m.be1b4f3c6c1c")
          )
            return false;
          if (
            statusFilter.value === "revoked" &&
            status !== t("m.fef96e34f77b")
          )
            return false;
          return (
            !query ||
            [
              node.displayName,
              nodeTitle(node),
              node.nodeId,
              preference?.group,
              preference?.notes,
              hostname,
              status,
            ].some((value) => value?.toLocaleLowerCase().includes(query))
          );
        });
        const title = (node: AgentNode) => nodeTitle(node);
        const statusRank = (node: AgentNode) =>
          nodeIsOnline(node)
            ? 0
            : isPendingRegistration(node)
              ? 1
              : node.status === "revoked"
                ? 3
                : 2;
        matching.sort((left, right) => {
          if (dashboardSettings.value.nodeSortBy === "name")
            return title(left).localeCompare(title(right), locale.value);
          if (dashboardSettings.value.nodeSortBy === "status") {
            const rank = statusRank(left) - statusRank(right);
            if (rank) return rank;
          } else {
            const lp = nodePreference(left),
              rp = nodePreference(right);
            if (lp?.pinned !== rp?.pinned) return lp?.pinned ? -1 : 1;
            if ((lp?.sortOrder ?? 0) !== (rp?.sortOrder ?? 0))
              return (lp?.sortOrder ?? 0) - (rp?.sortOrder ?? 0);
          }
          return title(left).localeCompare(title(right), locale.value);
        });
        if (dashboardSettings.value.nodeGroupBy === "none")
          return [
            {
              key: "all",
              label: "",
              nodes: matching,
            },
          ];
        const groups = new Map<string, AgentNode[]>();
        for (const node of matching) {
          const label =
            dashboardSettings.value.nodeGroupBy === "status"
              ? nodeStatusGroup(node)
              : nodePreference(node)?.group.trim() || t("m.e86ea3f280d9");
          groups.set(label, [...(groups.get(label) ?? []), node]);
        }
        const statusOrder = [
          t("m.b9086662b1df"),
          t("m.a890b3be09aa"),
          t("m.be1b4f3c6c1c"),
          t("m.fef96e34f77b"),
        ];
        return [...groups.keys()]
          .sort((a, b) =>
            dashboardSettings.value.nodeGroupBy === "status"
              ? statusOrder.indexOf(a) - statusOrder.indexOf(b)
              : a.localeCompare(b, locale.value),
          )
          .map((label) => ({
            key: label,
            label,
            nodes: groups.get(label) ?? [],
          }));
      });
      function tasksForNode(nodeID: string): ContainerTask[] {
        return Object.values(taskViews.value).filter(
          (task) => task.nodeId === nodeID,
        );
      }
      function chooseNode(node: AgentNode) {
        openNode(
          node,
          dashboardSettings.value.viewMode === "manage" ? "docker" : "overview",
        );
      }
      function openNode(
        node: AgentNode,
        section: "overview" | "docker" | "files",
      ) {
        viewNavigation.go({
          screen: "nodes",
          nodeId: node.nodeId,
          section,
        });
      }
      function openNodeShortcut(payload: {
        node: AgentNode;
        action: "terminal";
      }) {
        const node = nodes.value.find(
          (item) => item.nodeId === payload.node.nodeId,
        );
        if (!node) return;
        terminalNode.value = node;
      }
      function acceptNodeDeletion() {
        const nodeID = selectedNodeID.value;
        nodes.value = nodes.value.filter((node) => node.nodeId !== nodeID);
        for (const cache of [
          views,
          dockerViews,
          clocks,
          nodeReasons,
          nodePreferences,
          preferenceIdentities,
          sshConnections,
          sshUserChoices,
        ])
          delete cache.value[nodeID];
        taskViews.value = Object.fromEntries(
          Object.entries(taskViews.value).filter(
            ([, task]) => task.nodeId !== nodeID,
          ),
        );
        backToNodes();
        void refreshNodes().catch((reason) => {
          error.value =
            reason instanceof Error ? reason.message : String(reason);
        });
      }
      function backToNodes() {
        viewNavigation.go({
          screen: "nodes",
        });
      }
      watchState(
        () => viewNavigation.route.value,
        (route) => {
          terminalNode.value = null;
          const node = nodes.value.find((item) => item.nodeId === route.nodeId);
          if (!node) return;
          error.value = "";
          void loadPreferences(node);
          if (node.agentId)
            void Promise.all([
              loadMetrics(node),
              loadContainers(node),
              loadTasks(node),
            ]);
          else setNodeReason(node.nodeId, "awaiting_agent_registration");
        },
      );
      function acceptSavedPreference(preference: DashboardPreference) {
        delete sshConnections.value[preference.nodeId];
        const sshNode = nodes.value.find(
          (node) => node.nodeId === preference.nodeId,
        );
        if (sshNode && nodeIsOnline(sshNode)) void loadSSH(sshNode, true);
        const existing = nodePreferences.value[preference.nodeId] ?? [];
        const without = existing.filter(
          (item) =>
            !(
              item.targetKind === preference.targetKind &&
              item.identity === preference.identity
            ),
        );
        nodePreferences.value = {
          ...nodePreferences.value,
          [preference.nodeId]: [...without, preference],
        };
      }
      function acceptDockerResponse(result: DockerInventoryMessage) {
        const containers = Array.isArray(result.inventory?.containers)
          ? result.inventory.containers
          : [];
        result = {
          ...result,
          inventory: {
            ...result.inventory,
            containers: containers.map((record) => ({
              ...record,
              container: {
                ...record.container,
                networks: Array.isArray(record.container?.networks)
                  ? record.container.networks
                  : [],
                mounts: Array.isArray(record.container?.mounts)
                  ? record.container.mounts
                  : [],
                ports: Array.isArray(record.container?.ports)
                  ? record.container.ports.map((port) => ({
                      ...port,
                      configured: Array.isArray(port.configured)
                        ? port.configured
                        : [],
                      published: Array.isArray(port.published)
                        ? port.published
                        : [],
                    }))
                  : [],
              },
            })),
          },
        };
        const previous = dockerViews.value[result.nodeId];
        if (previous) {
          if (result.inventory.activeGeneration < previous.activeGeneration)
            return;
          if (result.inventory.activeGeneration === previous.activeGeneration) {
            const incomingTime = Date.parse(result.inventory.serverTime),
              previousTime = Date.parse(previous.serverTime);
            if (
              Number.isFinite(incomingTime) &&
              Number.isFinite(previousTime) &&
              incomingTime < previousTime
            )
              return;
            if (incomingTime === previousTime && incomingTime > 0) {
              const incomingSequence = Math.max(
                  0,
                  ...result.inventory.containers.map((item) => item.sequence),
                ),
                previousSequence = Math.max(
                  0,
                  ...previous.containers.map((item) => item.sequence),
                );
              if (incomingSequence < previousSequence) return;
            }
          }
        }
        dockerViews.value = {
          ...dockerViews.value,
          [result.nodeId]: result.inventory,
        };
        preferenceIdentities.value = {
          ...preferenceIdentities.value,
          [result.nodeId]: result.preferenceIdentities ?? {},
        };
        if (
          setClock(
            result.nodeId,
            result.state.status,
            result.state.serverTime,
            result.state.leaseValidUntil,
            result.state.generation,
          )
        )
          setNodeReason(result.nodeId, result.state.reason);
      }
      function acceptMetricsResponse(result: MetricsView | NodeStatusResponse) {
        if (isNodeStatusResponse(result)) {
          if (
            setClock(
              result.nodeId,
              result.state.status,
              result.state.serverTime,
              result.state.leaseValidUntil,
              result.state.generation,
            )
          )
            setNodeReason(result.nodeId, result.state.reason);
          return;
        }
        const previous = views.value[result.nodeId];
        if (previous) {
          if (result.activeGeneration < previous.activeGeneration) return;
          if (result.activeGeneration === previous.activeGeneration) {
            if (
              result.generation < previous.generation ||
              (result.generation === previous.generation &&
                result.sequence < previous.sequence)
            )
              return;
            const incomingTime = Date.parse(result.serverTime),
              previousTime = Date.parse(previous.serverTime);
            if (
              result.generation === previous.generation &&
              result.sequence === previous.sequence &&
              Number.isFinite(incomingTime) &&
              Number.isFinite(previousTime) &&
              incomingTime <= previousTime
            )
              return;
          }
        }
        const previousClock = clocks.value[result.nodeId];
        if (previousClock && result.activeGeneration < previousClock.generation)
          return;
        if (
          previousClock &&
          result.activeGeneration === previousClock.generation &&
          Date.parse(result.serverTime) < Date.parse(previousClock.serverTime)
        )
          return;
        const normalized: MetricsView = {
          ...result,
          metrics: {
            ...result.metrics,
            network: {
              ...result.metrics.network,
              interfaces: Array.isArray(result.metrics.network?.interfaces)
                ? result.metrics.network.interfaces
                : [],
            },
            disk: {
              ...result.metrics.disk,
              mounts: Array.isArray(result.metrics.disk?.mounts)
                ? result.metrics.disk.mounts
                : [],
            },
          },
        };
        views.value = {
          ...views.value,
          [normalized.nodeId]: normalized,
        };
        setNodeReason(normalized.nodeId);
        setClock(
          normalized.nodeId,
          normalized.nodeStatus,
          normalized.serverTime,
          normalized.leaseValidUntil,
          normalized.activeGeneration,
        );
      }
      function isNodeStatusResponse(
        result: MetricsView | NodeStatusResponse,
      ): result is NodeStatusResponse {
        return (result as NodeStatusResponse).type === "node_status";
      }
      function acceptSocketMessage(raw: string) {
        let event: unknown;
        try {
          event = JSON.parse(raw);
        } catch {
          return;
        }
        if (
          typeof event !== "object" ||
          event === null ||
          !("type" in event) ||
          !("nodeId" in event)
        )
          return;
        const value = event as {
          type?: string;
          nodeId?: string;
          metrics?: MetricsView;
          state?: NodeStatusResponse["state"];
        };
        if (!value.nodeId) return;
        if (value.type === "node_metrics" && value.metrics) {
          acceptMetricsResponse(value.metrics);
          return;
        }
        if (value.type === "node_status" && value.state) {
          if (
            setClock(
              value.nodeId,
              value.state.status,
              value.state.serverTime,
              value.state.leaseValidUntil,
              value.state.generation,
            )
          )
            setNodeReason(value.nodeId, value.state.reason);
          return;
        }
        if (value.type === "node_containers")
          acceptDockerResponse(event as DockerInventoryMessage);
      }
      async function connectDashboard() {
        if (disposed) return;
        socketState.value =
          socketState.value === "connected" ? "retrying" : "connecting";
        let url: URL;
        try {
          url = await operationURL("dashboard.watch");
        } catch {
          scheduleReconnect();
          return;
        }
        if (disposed) return;
        const current = new WebSocket(url);
        socket = current;
        current.onopen = () => {
          if (disposed || socket !== current) return;
          reconnectDelay = 1000;
          socketState.value = "connected";
          void refreshNodes().catch((reason: unknown) => {
            busy.value = false;
            error.value =
              reason instanceof Error ? reason.message : t("m.8fae2c449c13");
          });
        };
        current.onmessage = (message) => {
          if (!disposed && socket === current)
            acceptSocketMessage(String(message.data));
        };
        current.onclose = () => {
          if (socket === current) {
            socket = undefined;
            scheduleReconnect();
          }
        };
        current.onerror = () => {
          if (socket === current) current.close();
        };
      }
      function scheduleReconnect() {
        if (disposed || reconnectTimer !== undefined) return;
        socketState.value = "retrying";
        reconnectTimer = setTimeout(() => {
          reconnectTimer = undefined;
          connectDashboard();
        }, reconnectDelay);
        reconnectDelay = Math.min(30_000, reconnectDelay * 2);
      }
      async function submitContainerAction(
        nodeID: string,
        containerID: string,
        action: ContainerTaskAction,
        options: Partial<CreateContainerTaskPayload> = {},
      ) {
        const node = nodes.value.find((item) => item.nodeId === nodeID),
          inventory = dockerViews.value[nodeID];
        const container = inventory?.containers.find(
          (item) => item.container.id === containerID,
        )?.container;
        const key = `${nodeID}:${containerID}`;
        if (
          !node ||
          !container ||
          !inventory ||
          inventory.dockerAvailability !== "available" ||
          !node.agentId ||
          !nodeIsOnline(node) ||
          dockerIsStale(inventory) ||
          container.stale ||
          taskSubmitting.value[key]
        )
          return;
        if (
          action === "delete" &&
          (container.running ||
            container.paused ||
            container.restarting ||
            options.deleteConfirmationId !== containerID ||
            options.deleteConfirmed !== true)
        )
          return;
        taskSubmitting.value = {
          ...taskSubmitting.value,
          [key]: true,
        };
        const errors = {
          ...taskErrors.value,
        };
        delete errors[key];
        taskErrors.value = errors;
        try {
          const payload = {
            ...options,
            action,
          };
          const accepted = await api.createContainerTask(
            nodeID,
            containerID,
            payload,
          );
          const current = await api.nodeTask(nodeID, accepted.taskId);
          taskViews.value = {
            ...taskViews.value,
            [current.taskId]: current,
          };
        } catch (reason) {
          taskErrors.value = {
            ...taskErrors.value,
            [key]:
              reason instanceof Error ? reason.message : t("m.e9b2af7934ab"),
          };
        } finally {
          taskSubmitting.value = {
            ...taskSubmitting.value,
            [key]: false,
          };
        }
      }
      function submitCardContainerAction(payload: {
        node: AgentNode;
        record: DockerInventory["containers"][number];
        action: ContainerTaskAction;
      }) {
        void submitContainerAction(
          payload.node.nodeId,
          payload.record.container.id,
          payload.action,
        );
      }
      function submitDetailContainerAction(payload: {
        nodeId: string;
        containerId: string;
        task: CreateContainerTaskPayload;
      }) {
        void submitContainerAction(
          payload.nodeId,
          payload.containerId,
          payload.task.action,
          payload.task,
        );
      }
      async function submitContainerCreateTask(payload: {
        nodeId: string;
        spec: ContainerCreateSpec;
      }) {
        const { nodeId, spec } = payload;
        const node = nodes.value.find((item) => item.nodeId === nodeId),
          inventory = dockerViews.value[nodeId];
        const key = `${nodeId}:container-create`;
        if (
          !node ||
          !inventory ||
          inventory.dockerAvailability !== "available" ||
          !node.agentId ||
          !nodeIsOnline(node) ||
          dockerIsStale(inventory) ||
          taskSubmitting.value[key]
        )
          return;
        taskSubmitting.value = {
          ...taskSubmitting.value,
          [key]: true,
        };
        const errors = {
          ...taskErrors.value,
        };
        delete errors[key];
        taskErrors.value = errors;
        try {
          const accepted = await api.createContainerCreateTask(nodeId, spec);
          const current = await api.nodeTask(nodeId, accepted.taskId);
          taskViews.value = {
            ...taskViews.value,
            [current.taskId]: current,
          };
        } catch (reason) {
          taskErrors.value = {
            ...taskErrors.value,
            [key]:
              reason instanceof Error ? reason.message : t("m.61aabbe69bf5"),
          };
        } finally {
          taskSubmitting.value = {
            ...taskSubmitting.value,
            [key]: false,
          };
        }
      }
      afterMount(() => {
        window.addEventListener("scroll", followVisibleNode, {
          passive: true,
        });
        void loadDashboardSettings();
        void refreshNodes().catch((reason: unknown) => {
          busy.value = false;
          error.value =
            reason instanceof Error ? reason.message : t("m.8fae2c449c13");
        });
        connectDashboard();
        refreshTimer = setInterval(() => {
          void refreshNodes().catch((reason: unknown) => {
            error.value =
              reason instanceof Error ? reason.message : t("m.4de430beaf29");
          });
        }, 10_000);
        tickTimer = setInterval(() => {
          elapsed.value = performance.now();
          wallClockNow.value = Date.now();
        }, 1000);
        taskRefreshTimer = setInterval(() => {
          const active = new Set(
            Object.values(taskViews.value)
              .filter(
                (task) => task.status === "queued" || task.status === "running",
              )
              .map((task) => task.nodeId),
          );
          for (const node of nodes.value)
            if (active.has(node.nodeId)) void loadTasks(node);
        }, 1200);
      });
      beforeDispose(() => {
        disposed = true;
        window.removeEventListener("scroll", followVisibleNode);
        if (inspectFrame) cancelAnimationFrame(inspectFrame);
        if (reconnectTimer !== undefined) clearTimeout(reconnectTimer);
        if (refreshTimer !== undefined) clearInterval(refreshTimer);
        if (tickTimer !== undefined) clearInterval(tickTimer);
        if (taskRefreshTimer !== undefined) clearInterval(taskRefreshTimer);
        socket?.close();
      });
      return () => (
        <>
          {!selectedNode.value ? (
            <section
              aria-labelledby="nodes-title"
              className={__classes("nodes-dashboard workspace-page")}
              data-v-73fde8e5=""
            >
              <header className={__classes("nodes-heading")} data-v-73fde8e5="">
                <div data-v-73fde8e5="">
                  <h1 id="nodes-title" data-v-73fde8e5="">
                    {t("nav.nodes")}
                  </h1>
                </div>
                <span
                  data-state={socketState.value as any}
                  role="status"
                  className={__classes("stream-state")}
                  data-v-73fde8e5=""
                >
                  <i aria-hidden="true" data-v-73fde8e5=""></i>
                  {socketState.value === "connected"
                    ? t("m.a1b56411cc63")
                    : socketState.value === "retrying"
                      ? t("m.7a58d3d9d3e3")
                      : t("m.694186911ae3")}
                </span>
              </header>
              <div className={__classes("overview-layout")} data-v-73fde8e5="">
                <div className={__classes("overview-main ui-load-region")} data-v-73fde8e5="">
                  <div
                    className={__classes("dashboard-overview-controls")}
                    data-v-73fde8e5=""
                  >
                    <div
                      aria-label={t("dashboard.total") as any}
                      className={__classes("node-summary")}
                      data-v-73fde8e5=""
                    >
                      <Fragment>
                        {__each(nodeSummary.value, (item) => (
                          <article
                            key={item.kind as any}
                            data-kind={item.kind as any}
                            data-v-73fde8e5=""
                          >
                            <span data-v-73fde8e5="">{t(item.label)}</span>
                            <strong data-v-73fde8e5="">
                              {busy.value && !nodes.value.length
                                ? "—"
                                : item.value}
                            </strong>
                            <i aria-hidden="true" data-v-73fde8e5=""></i>
                          </article>
                        ))}
                      </Fragment>
                    </div>
                    {error.value ? (
                      <div
                        role="alert"
                        className={__classes("nodes-error")}
                        data-v-73fde8e5=""
                      >
                        {display(error.value)}
                      </div>
                    ) : null}
                    <div
                      className={__classes("dashboard-toolbar")}
                      data-v-73fde8e5=""
                    >
                      <label
                        className={__classes("node-search")}
                        data-v-73fde8e5=""
                      >
                        <UiIcon name="nodes" data-v-73fde8e5=""></UiIcon>
                        <input
                          type="search"
                          placeholder={t("m.800588838c17") as any}
                          aria-label={t("m.0191c754eec0") as any}
                          {...__input(
                            serverSearch.value,
                            (__nextValue: any) => {
                              serverSearch.value = __nextValue;
                            },
                            "search",
                            undefined,
                            [],
                          )}
                          data-v-73fde8e5=""
                        ></input>
                      </label>
                      <div
                        className={__classes("dashboard-toolbar-actions")}
                        data-v-73fde8e5=""
                      >
                        <button
                          type="button"
                          aria-expanded={preferencesOpen.value as any}
                          aria-controls="node-view-preferences"
                          className={__classes(
                            "secondary-button view-preferences-button",
                          )}
                          onClick={(event: any) => {
                            preferencesOpen.value = !preferencesOpen.value;
                          }}
                          data-v-73fde8e5=""
                        >
                          <UiIcon name="settings" data-v-73fde8e5=""></UiIcon>
                          {t("dashboard.configure")}
                        </button>
                        <button
                          type="button"
                          className={__classes("primary-button")}
                          onClick={(event: any) => {
                            emit("enroll");
                          }}
                          data-v-73fde8e5=""
                        >
                          <UiIcon name="add" data-v-73fde8e5=""></UiIcon>
                          {t("nav.enrollment")}
                        </button>
                      </div>
                    </div>
                  </div>
                  <section
                    aria-label={t("m.8a3dfec291d0") as any}
                    data-testid="multi-vps-dashboard"
                    className={__classes("vps-dashboard")}
                    data-v-73fde8e5=""
                  >
                    <div
                      id="node-view-preferences"
                      aria-label={t("m.3fa2b5eb40e0") as any}
                      className={__classes("server-view-controls")}
                      style={__style({
                        display: preferencesOpen.value ? undefined : "none",
                      })}
                      data-v-73fde8e5=""
                    >
                      <label data-v-73fde8e5="">
                        {t("m.9a9acd39e536")}
                        <UiSelect
                          modelValue={statusFilter.value}
                          onUpdateModelValue={(__nextValue: any) => {
                            statusFilter.value = __nextValue;
                          }}
                          modelModifiers={{}}
                          data-v-73fde8e5=""
                        >
                          <option value="all" data-v-73fde8e5="">
                            {t("m.2b13e491c90b")}
                          </option>
                          <option value="online" data-v-73fde8e5="">
                            {t("m.b9086662b1df")}
                          </option>
                          <option value="pending" data-v-73fde8e5="">
                            {t("m.a890b3be09aa")}
                          </option>
                          <option value="offline" data-v-73fde8e5="">
                            {t("m.be1b4f3c6c1c")}
                          </option>
                          <option value="revoked" data-v-73fde8e5="">
                            {t("m.fef96e34f77b")}
                          </option>
                          <option value="hidden" data-v-73fde8e5="">
                            {t("m.58f1163e3fc6")}
                          </option>
                        </UiSelect>
                      </label>
                      <label data-v-73fde8e5="">
                        {t("m.88eac6c5df32")}
                        <UiSelect
                          modelValue={dashboardSettings.value.nodeGroupBy}
                          onUpdateModelValue={(__nextValue: any) => {
                            dashboardSettings.value.nodeGroupBy = __nextValue;
                          }}
                          modelModifiers={{}}
                          data-v-73fde8e5=""
                        >
                          <option value="status" data-v-73fde8e5="">
                            {t("m.b8f945ea49ff")}
                          </option>
                          <option value="group" data-v-73fde8e5="">
                            {t("m.c08b046b5470")}
                          </option>
                          <option value="none" data-v-73fde8e5="">
                            {t("m.fcd40ee3f05a")}
                          </option>
                        </UiSelect>
                      </label>
                      <label data-v-73fde8e5="">
                        {t("m.13fffdc852b8")}
                        <UiSelect
                          modelValue={dashboardSettings.value.nodeSortBy}
                          onUpdateModelValue={(__nextValue: any) => {
                            dashboardSettings.value.nodeSortBy = __nextValue;
                          }}
                          modelModifiers={{}}
                          data-v-73fde8e5=""
                        >
                          <option value="custom" data-v-73fde8e5="">
                            {t("m.0f59d3742e06")}
                          </option>
                          <option value="name" data-v-73fde8e5="">
                            {t("m.a98585871c53")}
                          </option>
                          <option value="status" data-v-73fde8e5="">
                            {t("m.b8f945ea49ff")}
                          </option>
                        </UiSelect>
                      </label>

                      <button
                        type="button"
                        disabled={dashboardSettingsSaving.value as any}
                        className={__classes("container-action")}
                        onClick={(event: any) => {
                          void saveDashboardSettings();
                        }}
                        data-v-73fde8e5=""
                      >
                        {dashboardSettingsSaving.value
                          ? t("m.ff509c9ba052")
                          : t("m.b290bfe6352c")}
                      </button>
                      {dashboardSettingsMessage.value ? (
                        <span role="status" data-v-73fde8e5="">
                          {display(dashboardSettingsMessage.value)}
                        </span>
                      ) : null}
                      {dashboardSettingsError.value ? (
                        <span role="alert" data-v-73fde8e5="">
                          {display(dashboardSettingsError.value)}
                        </span>
                      ) : null}
                    </div>
                    <UiLoadState loading={busy.value} ready={nodesReady.value} failed={!!error.value} />
                    {nodesReady.value && nodes.value.length === 0 ? (
                      <div
                        className={__classes("dashboard-welcome")}
                        data-v-73fde8e5=""
                      >
                        <span
                          className={__classes("welcome-icon")}
                          data-v-73fde8e5=""
                        >
                          <UiIcon name="nodes" data-v-73fde8e5=""></UiIcon>
                        </span>
                        <h2 data-v-73fde8e5="">{t("dashboard.empty.title")}</h2>
                        <p data-v-73fde8e5="">
                          {t("dashboard.empty.description")}
                        </p>
                        <button
                          type="button"
                          className={__classes("primary-button")}
                          onClick={(event: any) => {
                            emit("enroll");
                          }}
                          data-v-73fde8e5=""
                        >
                          <UiIcon name="add" data-v-73fde8e5=""></UiIcon>
                          {t("dashboard.add")}
                        </button>
                      </div>
                    ) : serverCardGroups.value.every(
                        (group) => group.nodes.length === 0,
                      ) ? (
                      <p
                        className={__classes("dashboard-empty")}
                        data-v-73fde8e5=""
                      >
                        {t("m.1af4c85a8053")}
                      </p>
                    ) : null}
                    <Fragment>
                      {__each(serverCardGroups.value, (group) => (
                        <section
                          key={group.key as any}
                          data-group={group.key as any}
                          className={__classes("vps-card-group")}
                          data-v-73fde8e5=""
                        >
                          {group.label ? (
                            <h2
                              className={__classes("vps-group-heading")}
                              data-v-73fde8e5=""
                            >
                              {group.label}
                            </h2>
                          ) : null}
                          <div
                            className={__classes("vps-card-grid")}
                            data-v-73fde8e5=""
                          >
                            <Fragment>
                              {__each(group.nodes, (node) => (
                                <VpsCard
                                  key={node.nodeId as any}
                                  node={node as any}
                                  inspected={
                                    inspectedNodeID.value ===
                                    (node.nodeId as any)
                                  }
                                  metrics={views.value[node.nodeId] as any}
                                  inventory={
                                    dockerViews.value[node.nodeId] as any
                                  }
                                  nodePreference={nodePreference(node) as any}
                                  sshUserChoice={
                                    sshUserChoices.value[node.nodeId] as any
                                  }
                                  sshConnection={
                                    sshConnections.value[node.nodeId]
                                      ?.info as any
                                  }
                                  sshLoading={
                                    sshConnections.value[node.nodeId]
                                      ?.loading as any
                                  }
                                  sshError={
                                    sshConnections.value[node.nodeId]
                                      ?.error as any
                                  }
                                  containerPreferences={
                                    nodePreferences.value[node.nodeId] ??
                                    ([] as any)
                                  }
                                  preferenceIdentities={
                                    preferenceIdentities.value[node.nodeId] ??
                                    ({} as any)
                                  }
                                  online={nodeIsOnline(node) as any}
                                  pending={isPendingRegistration(node) as any}
                                  dockerStale={
                                    dockerViews.value[node.nodeId]
                                      ? dockerIsStale(
                                          dockerViews.value[node.nodeId],
                                        )
                                      : (!nodeIsOnline(node) as any)
                                  }
                                  currentTime={wallClockNow.value as any}
                                  tasks={tasksForNode(node.nodeId) as any}
                                  taskSubmitting={taskSubmitting.value as any}
                                  taskErrors={taskErrors.value as any}
                                  onInspect={(event: any) => {
                                    __call(inspectNode, event);
                                  }}
                                  onChooseSshUser={(event: any) => {
                                    __call(chooseSSHUser, event);
                                  }}
                                  onDiscoverSsh={(event: any) => {
                                    __call(discoverSSHForNode, event);
                                  }}
                                  onView={(event: any) => {
                                    __call(chooseNode, event);
                                  }}
                                  onQuickAction={(event: any) => {
                                    __call(openNodeShortcut, event);
                                  }}
                                  onContainerAction={(event: any) => {
                                    __call(submitCardContainerAction, event);
                                  }}
                                  data-v-73fde8e5=""
                                ></VpsCard>
                              ))}
                            </Fragment>
                          </div>
                        </section>
                      ))}
                    </Fragment>
                  </section>
                </div>
                <OverviewContext
                  node={inspectedNode.value as any}
                  metrics={
                    inspectedNode.value
                      ? views.value[inspectedNode.value.nodeId]
                      : (undefined as any)
                  }
                  onCore={(event: any) => {
                    inspectedNodeID.value = "";
                  }}
                  onAgent={(event: any) => {
                    if (!inspectedNode.value && nodes.value[0])
                      inspectedNodeID.value = nodes.value[0].nodeId;
                  }}
                  onMetrics={(event: any) => {
                    __call(acceptMetricsResponse, event);
                  }}
                  data-v-73fde8e5=""
                ></OverviewContext>
              </div>
            </section>
          ) : (
            <NodeDetail
              readOnly={__props.readOnly}
              key={selectedNode.value.nodeId as any}
              node={selectedNode.value as any}
              metrics={views.value[selectedNode.value.nodeId] as any}
              inventory={dockerViews.value[selectedNode.value.nodeId] as any}
              preferences={
                nodePreferences.value[selectedNode.value.nodeId] ?? ([] as any)
              }
              preferenceIdentities={
                preferenceIdentities.value[selectedNode.value.nodeId] ??
                ({} as any)
              }
              online={nodeIsOnline(selectedNode.value) as any}
              pending={isPendingRegistration(selectedNode.value) as any}
              dockerStale={
                dockerViews.value[selectedNode.value.nodeId]
                  ? dockerIsStale(dockerViews.value[selectedNode.value.nodeId])
                  : (!nodeIsOnline(selectedNode.value) as any)
              }
              nodeReason={nodeReasons.value[selectedNode.value.nodeId] as any}
              tasks={tasksForNode(selectedNode.value.nodeId) as any}
              taskSubmitting={taskSubmitting.value as any}
              taskErrors={taskErrors.value as any}
              dashboardSettings={dashboardSettings.value as any}
              dashboardSettingsSaving={dashboardSettingsSaving.value as any}
              dashboardSettingsMessage={dashboardSettingsMessage.value as any}
              dashboardSettingsError={dashboardSettingsError.value as any}
              currentTime={wallClockNow.value as any}
              onDeleted={(event: any) => {
                __call(acceptNodeDeletion, event);
              }}
              onRefreshTasks={(event: any) => {
                __call((id) => {
                  const node = nodes.value.find((item) => item.nodeId === id);
                  if (node) void loadTasks(node);
                }, event);
              }}
              onPreferenceSaved={(event: any) => {
                __call(acceptSavedPreference, event);
              }}
              onContainerAction={(event: any) => {
                __call(submitDetailContainerAction, event);
              }}
              onContainerCreate={(event: any) => {
                __call(submitContainerCreateTask, event);
              }}
              onSettingsChange={(event: any) => {
                dashboardSettings.value = event;
              }}
              onSaveSettings={(event: any) => {
                void saveDashboardSettings();
              }}
              data-v-73fde8e5=""
            ></NodeDetail>
          )}
          {terminalNode.value ? (
            <TerminalConsole
              key={terminalNode.value.nodeId as any}
              nodeId={terminalNode.value.nodeId as any}
              targetKind="host"
              targetLabel={nodeTitle(terminalNode.value) as any}
              onClose={(event: any) => {
                terminalNode.value = null;
              }}
              data-v-73fde8e5=""
            ></TerminalConsole>
          ) : null}
        </>
      );
    },
    input,
    ["readOnly"],
    [],
    {},
    true,
  );
}
