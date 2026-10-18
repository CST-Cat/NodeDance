import { Fragment } from "react";
import {
  api,
  type AgentNode,
  type ContainerCreateSpec,
  type ContainerTask,
  type ContainerTaskAction,
  type DashboardPreference,
  type DashboardSettings,
  type DockerContainerRecord,
  type DockerInventory,
  type MetricHistory,
  type ServiceProbe,
  type TaskAuditEvent,
} from "../api";
import { requestConfirm, requestPrompt } from "../dialogs";
import { display, locale, t } from "../i18n";
import type { MetricsView } from "../metrics-contract";
import { nodeNavigationKey, useViewTab } from "../navigation";
import {
  __call,
  __classes,
  __each,
  __indeterminate,
  __input,
  beforeDispose,
  cell,
  consumeValue,
  derived,
  lazyView,
  trackEffect,
  useController,
  watchState,
} from "../state";
import AgentEnrollment from "./AgentEnrollment";
import ComposeProjects from "./ComposeProjects";
import ContainerCreateForm from "./ContainerCreateForm";
import ContainerRebuildWizard from "./ContainerRebuildWizard";
import ContainerStreams from "./ContainerStreams";
import DockerActionButtons from "./DockerActionButtons";
import ImagesPanel from "./ImagesPanel";
import MetricsPanel from "./MetricsPanel";
import NodeAssetSettings from "./NodeAssetSettings";
import "./NodeDetail.css";
import NodeServiceProbes from "./NodeServiceProbes";
import PreferenceEditor from "./PreferenceEditor";
import UiSelect from "./UiSelect";

const HistoricalMetrics = lazyView(() => import("./HistoricalMetrics"));
const TerminalConsole = lazyView(() => import("./TerminalConsole"));
const NodeFiles = lazyView(() => import("./NodeFiles"));
type Props = {
  readOnly?: boolean;
  node: AgentNode;
  metrics?: MetricsView;
  inventory?: DockerInventory;
  preferences: DashboardPreference[];
  preferenceIdentities: Record<string, string>;
  online: boolean;
  pending: boolean;
  dockerStale: boolean;
  nodeReason?: string;
  tasks: ContainerTask[];
  taskSubmitting: Record<string, boolean>;
  taskErrors: Record<string, string>;
  dashboardSettings: DashboardSettings;
  dashboardSettingsSaving: boolean;
  dashboardSettingsMessage: string;
  dashboardSettingsError: string;
  currentTime: number;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function NodeDetail(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const emit = __emit;
      const deletingNode = cell(false);
      const nodeActionError = cell("");
      const enrollmentOpen = cell(false);
      function toggleEnrollment() {
        if (activeSection.value === "settings") {
          selectSection("overview");
          enrollmentOpen.value = true;
        } else enrollmentOpen.value = !enrollmentOpen.value;
      }
      async function deleteNode() {
        if (deletingNode.value) return;
        deletingNode.value = true;
        nodeActionError.value = "";
        try {
          if (
            !(await requestConfirm(
              t("node.deleteConfirm", {
                "0": props.node.displayName,
              }),
            ))
          )
            return;
          await api.deleteNode(props.node.nodeId);
          emit("deleted");
        } catch (reason) {
          nodeActionError.value =
            reason instanceof Error ? reason.message : String(reason);
        } finally {
          deletingNode.value = false;
        }
      }
      watchState(
        () => props.pending,
        (pending) => {
          if (!pending) enrollmentOpen.value = false;
        },
      );
      const sectionTabs = derived(
        () =>
          [
            {
              id: "overview",
              icon: "host",
              label: t("m.a33db5730556"),
            },
            {
              id: "docker",
              icon: "nodes",
              label: t("m.eee901e4cc16"),
            },
            {
              id: "compose",
              icon: "operations",
              label: t("m.5f67dd75b01f"),
            },
            {
              id: "probes",
              icon: "discovery",
              label: t("m.3fefdc07fa14"),
            },
            {
              id: "images",
              icon: "file",
              label: t("m.176c09844e0f"),
            },
            {
              id: "rebuild",
              icon: "refresh",
              label: t("m.474490ab5dc3"),
            },
            {
              id: "files",
              icon: "folder",
              label: t("m.b3bd5ac7cc4d"),
            },
            {
              id: "history",
              icon: "history",
              label: t("m.bfb41404e88b"),
            },
            {
              id: "events",
              icon: "alerts",
              label: t("m.a700a47380fe"),
            },
            {
              id: "settings",
              icon: "settings",
              label: t("m.0a09fda5e2a6"),
            },
          ].filter(tab => !props.readOnly || ["overview", "history"].includes(tab.id)),
      );
      const activeSection = useViewTab("section", "overview", () =>
        sectionTabs.value.map((tab) => tab.id),
      );
      const sidebarNavigation = consumeValue(nodeNavigationKey);
      const sidebarOwner = Symbol("node-detail");
      beforeDispose(() => {
        if (sidebarNavigation?.value?.owner === sidebarOwner)
          sidebarNavigation.value = null;
      });
      const overviewProbes = cell<ServiceProbe[]>([]);
      const probeSummaryError = cell("");
      const probeSummaryLoadedAt = cell<Date>();
      let probeSummaryEpoch = 0;
      const probeCounts = derived(() => {
        const enabled = overviewProbes.value.filter((probe) => probe.enabled);
        const healthy = props.online
          ? enabled.filter((probe) => probe.status === "healthy").length
          : 0;
        const unhealthy = props.online
          ? enabled.filter((probe) => probe.status === "unhealthy").length
          : 0;
        return {
          total: overviewProbes.value.length,
          healthy,
          unhealthy,
          unknown: enabled.length - healthy - unhealthy,
          disabled: overviewProbes.value.length - enabled.length,
        };
      });
      watchState(
        [() => props.node.nodeId, activeSection],
        async ([nodeId, section]) => {
          const epoch = ++probeSummaryEpoch;
          overviewProbes.value = [];
          probeSummaryError.value = "";
          probeSummaryLoadedAt.value = undefined;
          if (section !== "overview") return;
          try {
            const response = await api.serviceProbes();
            if (epoch === probeSummaryEpoch) {
              overviewProbes.value = response.probes.filter(
                (probe) => probe.nodeId === nodeId,
              );
              probeSummaryLoadedAt.value = new Date();
            }
          } catch (reason) {
            if (epoch === probeSummaryEpoch)
              probeSummaryError.value =
                reason instanceof Error ? reason.message : t("m.ebf63ef228d2");
          }
        },
        {
          immediate: true,
        },
      );
      const customFieldOptions = derived(() => [
        {
          id: "state",
          label: t("m.e46b0272bd64"),
        },
        {
          id: "ports",
          label: t("m.e71ac32b544b"),
        },
        {
          id: "health",
          label: t("m.85788192fbeb"),
        },
        {
          id: "image",
          label: t("m.176c09844e0f"),
        },
        {
          id: "uptime",
          label: t("m.5986e9d976e0"),
        },
      ]);
      const containerSearch = cell("");
      const containerState = cell("all");
      const pageSize = cell(20);
      const containerPage = cell(1);
      const selectedContainerIDs = cell<string[]>([]);
      const selectionBusy = cell(false);
      const detailPanel = cell<
        "info" | "streams" | "preferences" | "audit" | ""
      >("");
      const historyView = cell<MetricHistory | null>(null);
      const historyLoading = cell(false);
      const historyError = cell("");
      const historyRange = cell<"6h" | "24h" | "7d" | "30d" | "1y">("24h");
      const taskAuditViews = cell<Record<string, TaskAuditEvent[]>>({});
      const taskAuditVisible = cell<Record<string, boolean>>({});
      const taskAuditLoading = cell<Record<string, boolean>>({});
      const taskAuditErrors = cell<Record<string, string>>({});
      const activeTerminal = cell<{
        nodeId: string;
        targetKind: "host" | "container";
        containerId?: string;
        targetLabel: string;
      } | null>(null);
      const historyResolution = derived(() =>
        historyRange.value === "1y" ? "hour" : "minute",
      );
      const nodePreference = derived(() =>
        props.preferences.find(
          (item) =>
            item.targetKind === "node" &&
            item.identity === `node:${props.node.nodeId}`,
        ),
      );
      const nodeTitle = derived(
        () => nodePreference.value?.alias || props.node.displayName,
      );
      trackEffect(() => {
        if (sidebarNavigation)
          sidebarNavigation.value = {
            owner: sidebarOwner,
            nodeId: props.node.nodeId,
            title: nodeTitle.value,
            activeSection: activeSection.value,
            sections: sectionTabs.value,
          };
      });
      const selectedTasks = derived(() =>
        [...props.tasks].sort(
          (left, right) =>
            Date.parse(right.createdAt) - Date.parse(left.createdAt),
        ),
      );
      const selectedRebuildContainerID = cell("");
      const rebuildCandidates = derived(() =>
        orderedContainers.value.filter(
          (record) =>
            !record.container.compose &&
            !rollbackTaskForContainer(record.container.id),
        ),
      );
      const selectedRebuildContainer = derived(
        () =>
          rebuildCandidates.value.find(
            (record) =>
              record.container.id === selectedRebuildContainerID.value,
          ) ?? rebuildCandidates.value[0],
      );
      const createTaskKey = derived(
        () => `${props.node.nodeId}:container-create`,
      );
      const createTasks = derived(() =>
        selectedTasks.value
          .filter((task) => task.action === "container_create")
          .slice(0, 3),
      );
      const canCreateContainers = derived(() =>
        Boolean(
          props.inventory?.dockerAvailability === "available" &&
            props.online &&
            props.node.agentId &&
            !props.dockerStale,
        ),
      );
      const createSubmitting = derived(() =>
        Boolean(props.taskSubmitting[createTaskKey.value]),
      );
      const createError = derived(
        () => props.taskErrors[createTaskKey.value] || "",
      );
      const viewMode = settingsField("viewMode");
      const nodeGroupBy = settingsField("nodeGroupBy");
      const nodeSortBy = settingsField("nodeSortBy");
      const groupBy = settingsField("groupBy");
      const sortBy = settingsField("sortBy");
      const featuredLimit = settingsField("featuredLimit");
      const customFields = derived({
        get: () => props.dashboardSettings.customFields,
        set: (value: string[]) =>
          emit("settingsChange", {
            ...props.dashboardSettings,
            customFields: value,
          }),
      });
      function settingsField<K extends keyof DashboardSettings>(key: K) {
        return derived({
          get: () => props.dashboardSettings[key],
          set: (value: DashboardSettings[K]) =>
            emit("settingsChange", {
              ...props.dashboardSettings,
              [key]: value,
            }),
        });
      }
      function preferenceForContainer(
        record: DockerInventory["containers"][number],
      ): DashboardPreference | undefined {
        const identity = props.preferenceIdentities[record.container.id];
        if (!identity) return undefined;
        const kind = record.container.compose ? "compose_service" : "container";
        return props.preferences.find(
          (item) => item.targetKind === kind && item.identity === identity,
        );
      }
      function containerTitle(
        record: DockerInventory["containers"][number],
      ): string {
        return (
          preferenceForContainer(record)?.alias ||
          record.container.name ||
          record.container.id.slice(0, 12)
        );
      }
      function containerGroupName(record: DockerContainerRecord): string {
        if (groupBy.value === "compose")
          return record.container.compose?.project || "";
        if (groupBy.value === "state") return record.container.state || "";
        return "";
      }
      const orderedContainers = derived(() =>
        [...(props.inventory?.containers ?? [])].sort((left, right) => {
          const group = containerGroupName(left).localeCompare(
            containerGroupName(right),
            locale.value,
          );
          if (group) return group;
          if (sortBy.value === "state") {
            const state = (left.container.state || "").localeCompare(
              right.container.state || "",
              locale.value,
            );
            if (state) return state;
          }
          if (sortBy.value === "custom") {
            const lp = preferenceForContainer(left),
              rp = preferenceForContainer(right);
            if (lp?.pinned !== rp?.pinned) return lp?.pinned ? -1 : 1;
            const order = (lp?.sortOrder ?? 0) - (rp?.sortOrder ?? 0);
            if (order) return order;
          }
          return containerTitle(left).localeCompare(
            containerTitle(right),
            locale.value,
          );
        }),
      );
      const sortedContainers = derived(() => {
        const query = containerSearch.value.trim().toLocaleLowerCase();
        return orderedContainers.value.filter((record) => {
          const c = record.container;
          if (
            containerState.value === "running" &&
            (!c.running || c.paused || c.restarting)
          )
            return false;
          if (containerState.value === "paused" && !c.paused) return false;
          if (
            containerState.value === "stopped" &&
            (c.running || c.paused || c.restarting)
          )
            return false;
          if (containerState.value === "restarting" && !c.restarting)
            return false;
          if (!query) return true;
          const preference = preferenceForContainer(record);
          return [
            containerTitle(record),
            record.container.name,
            record.container.image,
            record.container.id,
            record.container.compose?.project,
            record.container.compose?.service,
            preference?.notes,
          ].some((value) => value?.toLocaleLowerCase().includes(query));
        });
      });
      const pageCount = derived(() =>
        Math.max(1, Math.ceil(sortedContainers.value.length / pageSize.value)),
      );
      const pageContainers = derived(() =>
        sortedContainers.value.slice(
          (containerPage.value - 1) * pageSize.value,
          containerPage.value * pageSize.value,
        ),
      );
      const selectedContainers = derived(() =>
        orderedContainers.value.filter((record) =>
          selectedContainerIDs.value.includes(record.container.id),
        ),
      );
      const singleContainer = derived(() =>
        selectedContainers.value.length === 1
          ? selectedContainers.value[0]
          : undefined,
      );
      const allPageSelected = derived(
        () =>
          pageContainers.value.length > 0 &&
          pageContainers.value.every((record) =>
            selectedContainerIDs.value.includes(record.container.id),
          ),
      );
      const somePageSelected = derived(
        () =>
          !allPageSelected.value &&
          pageContainers.value.some((record) =>
            selectedContainerIDs.value.includes(record.container.id),
          ),
      );
      function toggleContainer(id: string, selected: boolean) {
        selectedContainerIDs.value = selected
          ? [...new Set([...selectedContainerIDs.value, id])]
          : selectedContainerIDs.value.filter((value) => value !== id);
      }
      function togglePage(selected: boolean) {
        const ids = new Set(
          pageContainers.value.map((record) => record.container.id),
        );
        selectedContainerIDs.value = selected
          ? [...new Set([...selectedContainerIDs.value, ...ids])]
          : selectedContainerIDs.value.filter((id) => !ids.has(id));
      }
      function inspectContainer(record: DockerContainerRecord) {
        selectedContainerIDs.value = [record.container.id];
        detailPanel.value = "info";
      }
      function closeMoreMenu(event: MouseEvent | KeyboardEvent) {
        if (
          event.type !== "keydown" &&
          !(event.target as HTMLElement).closest("button:not(:disabled), a")
        )
          return;
        const menu = (event.currentTarget as HTMLElement).closest("details");
        menu?.removeAttribute("open");
        menu?.querySelector("summary")?.focus();
      }
      function showPanel(panel: typeof detailPanel.value) {
        if (!singleContainer.value) return;
        detailPanel.value = panel;
        if (panel === "audit") {
          const task = taskForContainer(singleContainer.value.container.id);
          if (task && !taskAuditVisible.value[task.taskId])
            void toggleTaskAudit(task);
        }
      }
      watchState([containerSearch, containerState, pageSize], () => {
        containerPage.value = 1;
      });
      watchState(sortedContainers, (records) => {
        // Hidden or removed rows cannot remain targets of the toolbar.
        const ids = new Set(records.map((record) => record.container.id));
        const remaining = selectedContainerIDs.value.filter((id) =>
          ids.has(id),
        );
        if (remaining.length !== selectedContainerIDs.value.length)
          selectedContainerIDs.value = remaining;
        containerPage.value = Math.min(containerPage.value, pageCount.value);
      });
      watchState(
        selectedContainerIDs,
        () => {
          detailPanel.value = "";
        },
        {
          flush: "sync",
        },
      );
      function dockerAvailabilityText(inventory: DockerInventory): string {
        if (
          inventory.staleReason === "docker_capability_unavailable" ||
          inventory.dockerAvailability === "unavailable"
        )
          return t("m.d3e431f9d10b");
        if (inventory.dockerAvailability === "available") {
          if (inventory.health?.errorKind === "api_incompatible")
            return t("m.d56e55420ff6");
          if (props.dockerStale) return t("m.1a6858956811");
          if (inventory.health?.reason) return t("m.04dea0459ab7");
          return t("m.81e93ba5b0a1");
        }
        return t("m.c12a50c262ca");
      }
      function dockerStatusReason(inventory: DockerInventory): string {
        return inventory.health?.reason || inventory.staleReason || "";
      }
      function containerHealthText(
        container: DockerInventory["containers"][number]["container"],
      ): string {
        if (!container.healthcheckConfigured || container.health === "none")
          return t("m.5fac1c671a0d");
        return (
          (
            {
              healthy: t("m.aa18b6dcd893"),
              unhealthy: t("m.428fb8bfeecf"),
              starting: t("m.33439d263173"),
              unknown: t("m.4d8c1c5b4283"),
            } as Record<string, string>
          )[container.health] ?? container.health
        );
      }
      function portText(
        port: DockerInventory["containers"][number]["container"]["ports"][number],
      ): string {
        const published = Array.isArray(port.published) ? port.published : [];
        const configured = Array.isArray(port.configured)
          ? port.configured
          : [];
        if (published.length)
          return published
            .map((binding) => {
              const rawHost = binding.ip || "*";
              const host =
                rawHost.includes(":") && !rawHost.startsWith("[")
                  ? `[${rawHost}]`
                  : rawHost;
              const scope =
                rawHost === "*" || rawHost === "0.0.0.0" || rawHost === "::"
                  ? t("m.a54cd64d75d5")
                  : rawHost === "::1" || /^127(?:\.\d{1,3}){3}$/.test(rawHost)
                    ? t("m.b07e34088b42")
                    : "";
              return `${host}:${binding.port} → ${port.containerPort}/${port.protocol}${scope}`;
            })
            .join("，");
        if (configured.length)
          return t("m.639b66233cf4", {
            "0": port.containerPort,
            "1": port.protocol,
          });
        if (port.exposed)
          return t("m.c39c143634dc", {
            "0": port.containerPort,
            "1": port.protocol,
          });
        return `${port.containerPort}/${port.protocol}`;
      }
      function containerNetworkText(
        container: DockerInventory["containers"][number]["container"],
      ): string {
        if (container.hostNetwork) return t("m.471522ad66e5");
        const networks = container.networks ?? [];
        if (!networks.length) return t("m.6537857befe0");
        return networks
          .map((network) => {
            const addresses = [
              network.ipv4 && `IPv4 ${network.ipv4}`,
              network.ipv6 && `IPv6 ${network.ipv6}`,
            ].filter(Boolean);
            return t("m.838f6df66524", {
              "0": network.name,
              "1": addresses.length
                ? addresses.join(" · ")
                : t("m.9bf769e4c1eb"),
            });
          })
          .join("；");
      }
      function containerUptime(
        record: DockerInventory["containers"][number],
      ): string {
        if (!record.container.running || !record.container.startedAt)
          return t("m.4d8c1c5b4283");
        const started = Date.parse(record.container.startedAt);
        if (!Number.isFinite(started) || started > props.currentTime)
          return t("m.4d8c1c5b4283");
        const minutes = Math.floor((props.currentTime - started) / 60_000);
        const days = Math.floor(minutes / 1440),
          hours = Math.floor((minutes % 1440) / 60),
          remaining = minutes % 60;
        return days
          ? t("m.295f367bb72a", {
              "0": days,
              "1": hours,
            })
          : hours
            ? t("m.3cde3f0ae16c", {
                "0": hours,
                "1": remaining,
              })
            : t("m.c65627ead130", {
                "0": remaining,
              });
      }
      function safeServiceURL(
        record: DockerInventory["containers"][number],
      ): string {
        const candidate = preferenceForContainer(record)?.serviceUrl;
        if (!candidate) return "";
        try {
          const url = new URL(candidate);
          return ["http:", "https:"].includes(url.protocol) &&
            !url.username &&
            !url.password &&
            !url.hash
            ? url.href
            : "";
        } catch {
          return "";
        }
      }
      function preferenceForEditor(
        record: DockerInventory["containers"][number],
      ): DashboardPreference | undefined {
        const identity = props.preferenceIdentities[record.container.id];
        if (!identity) return undefined;
        const existing = preferenceForContainer(record);
        if (existing) return existing;
        return {
          nodeId: props.node.nodeId,
          targetKind: record.container.compose
            ? "compose_service"
            : "container",
          identity,
          alias: "",
          icon: "",
          notes: "",
          serviceUrl: "",
          sshHost: "",
          sshPort: 22,
          sshUser: "",
          group: "",
          sortOrder:
            props.inventory?.containers.findIndex(
              (item) => item.container.id === record.container.id,
            ) ?? 0,
          visible: true,
          pinned: false,
        };
      }
      function nodePreferenceForEditor(): DashboardPreference {
        return (
          nodePreference.value ?? {
            nodeId: props.node.nodeId,
            targetKind: "node",
            identity: `node:${props.node.nodeId}`,
            alias: "",
            icon: "server",
            notes: "",
            serviceUrl: "",
            sshHost: "",
            sshPort: 22,
            sshUser: "",
            group: "",
            sortOrder: -1,
            visible: true,
            pinned: true,
          }
        );
      }
      function taskKey(containerID: string): string {
        return `${props.node.nodeId}:${containerID}`;
      }
      function taskForContainer(
        containerID: string,
      ): ContainerTask | undefined {
        return [...props.tasks]
          .filter(
            (task) =>
              task.targetId === containerID ||
              (task.action === "rebuild" &&
                task.status === "succeeded" &&
                task.result.resourceRevision === containerID),
          )
          .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))[0];
      }
      function rollbackTaskForContainer(
        containerID: string,
      ): ContainerTask | undefined {
        return props.tasks
          .filter(
            (task) =>
              task.action === "rebuild" &&
              task.status === "succeeded" &&
              task.targetId === containerID &&
              Boolean(task.result.resourceRevision) &&
              task.result.resourceRevision !== containerID,
          )
          .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))[0];
      }
      function taskStatusLabel(task: ContainerTask): string {
        if (task.status === "queued") return t("m.3d531ad52ee4");
        if (task.status === "running")
          return t("m.472b61dd983a", {
            "0": task.progress.phase,
          });
        if (task.status === "succeeded")
          return t("m.52a8bda11dff", {
            "0": task.result.observedState || t("m.74a52b93c4f9"),
          });
        if (task.status === "unknown") return t("m.2629197ac89c");
        if (task.status === "timed_out") return t("m.7112c0bda6de");
        if (task.status === "canceled") return t("m.6f96c2add214");
        return t("m.e6bbfaf0e706", {
          "0": task.result.code || t("m.fb2425b0c8e1"),
        });
      }
      function canOperate(
        record: DockerInventory["containers"][number],
      ): boolean {
        return Boolean(
          props.inventory?.dockerAvailability === "available" &&
            props.online &&
            props.node.agentId &&
            !props.dockerStale &&
            !record.container.stale &&
            !props.taskSubmitting[taskKey(record.container.id)],
        );
      }
      function actionReady(
        record: DockerContainerRecord,
        action: ContainerTaskAction,
      ): boolean {
        if (
          !canOperate(record) ||
          rollbackTaskForContainer(record.container.id)
        )
          return false;
        const c = record.container;
        if (action === "rename") return !c.compose;
        if (action === "delete" || action === "start")
          return !c.running && !c.paused && !c.restarting;
        if (action === "resume") return c.paused;
        if (action === "stop") return c.running && !c.paused && !c.restarting;
        return c.running && !c.paused && !c.restarting;
      }
      function selectionCan(action: ContainerTaskAction): boolean {
        return (
          !selectionBusy.value &&
          selectedContainers.value.length > 0 &&
          (action !== "rename" || selectedContainers.value.length === 1) &&
          selectedContainers.value.every((record) =>
            actionReady(record, action),
          )
        );
      }
      async function submitSelection(action: ContainerTaskAction) {
        if (!selectionCan(action)) return;
        const ids = selectedContainers.value.map(
          (record) => record.container.id,
        );
        selectionBusy.value = true;
        try {
          let newName: string | undefined;
          if (action === "rename") {
            const value = await requestPrompt(
              t("m.19efbadaa133"),
              selectedContainers.value[0].container.name,
            );
            if (value === null) return;
            newName = value.trim();
          }
          if (action === "delete") {
            const value = await requestPrompt(
              ids.length === 1
                ? t("m.cd8d2b02a176", {
                    "0": ids[0],
                  })
                : t("docker.confirmDelete", {
                    "0": ids.length,
                    "1": ids.join(", "),
                  }),
            );
            if (value === null) return;
            const confirmed = value.split(/[\s,]+/).filter(Boolean);
            if (
              confirmed.length !== ids.length ||
              new Set(confirmed).size !== ids.length ||
              !ids.every((id) => confirmed.includes(id))
            )
              return;
          }
          // Refresh may change state while a confirmation dialog is open.
          const records = ids.map((id) =>
            props.inventory?.containers.find(
              (record) => record.container.id === id,
            ),
          );
          if (
            !records.every((record): record is DockerContainerRecord =>
              Boolean(record && actionReady(record, action)),
            )
          )
            return;
          for (const record of records) {
            emit("containerAction", {
              nodeId: props.node.nodeId,
              containerId: record.container.id,
              task: {
                action,
                ...(newName !== undefined
                  ? {
                      newName,
                    }
                  : {}),
                ...(action === "delete"
                  ? {
                      deleteConfirmed: true,
                      deleteConfirmationId: record.container.id,
                    }
                  : {}),
              },
            });
          }
        } finally {
          selectionBusy.value = false;
        }
      }
      function openSelectedRebuild() {
        if (!singleContainer.value) return;
        selectedRebuildContainerID.value = singleContainer.value.container.id;
        activeSection.value = "rebuild";
      }
      function submitContainerCreate(spec: ContainerCreateSpec) {
        if (!canCreateContainers.value || createSubmitting.value) return;
        emit("containerCreate", {
          nodeId: props.node.nodeId,
          spec,
        });
      }
      function openHostTerminal() {
        activeTerminal.value = {
          nodeId: props.node.nodeId,
          targetKind: "host",
          targetLabel: t("m.d4c37e398335", {
            "0": nodeTitle.value,
          }),
        };
      }
      function openContainerTerminal(
        record: DockerInventory["containers"][number],
      ) {
        activeTerminal.value = {
          nodeId: props.node.nodeId,
          targetKind: "container",
          containerId: record.container.id,
          targetLabel: `${nodeTitle.value} · ${record.container.name || record.container.id.slice(0, 12)}`,
        };
      }
      async function loadHistory() {
        historyLoading.value = true;
        historyError.value = "";
        const durations = {
          "6h": 6,
          "24h": 24,
          "7d": 168,
          "30d": 720,
          "1y": 8760,
        };
        try {
          historyView.value = await api.nodeHistory(
            props.node.nodeId,
            historyResolution.value,
            new Date(Date.now() - durations[historyRange.value] * 3_600_000),
            new Date(),
          );
        } catch (reason) {
          historyView.value = null;
          historyError.value =
            reason instanceof Error ? reason.message : t("m.7b26031b0a1a");
        } finally {
          historyLoading.value = false;
        }
      }
      function selectSection(section: typeof activeSection.value) {
        activeSection.value = section;
      }
      watchState(
        activeSection,
        (section) => {
          activeTerminal.value = null;
          selectedContainerIDs.value = [];
          if (section === "history") void loadHistory();
          if (section === "events") emit("refreshTasks", props.node.nodeId);
        },
        {
          immediate: true,
        },
      );
      function taskStatusText(task: ContainerTask): string {
        return taskStatusLabel(task);
      }
      async function toggleTaskAudit(task: ContainerTask) {
        const showing = Boolean(taskAuditVisible.value[task.taskId]);
        taskAuditVisible.value = {
          ...taskAuditVisible.value,
          [task.taskId]: !showing,
        };
        if (
          showing ||
          taskAuditViews.value[task.taskId] ||
          taskAuditLoading.value[task.taskId]
        )
          return;
        taskAuditLoading.value = {
          ...taskAuditLoading.value,
          [task.taskId]: true,
        };
        try {
          taskAuditViews.value = {
            ...taskAuditViews.value,
            [task.taskId]: (await api.nodeTaskAudit(task.nodeId, task.taskId))
              .events,
          };
        } catch (reason) {
          taskAuditErrors.value = {
            ...taskAuditErrors.value,
            [task.taskId]:
              reason instanceof Error ? reason.message : t("m.df3eec8deda0"),
          };
        } finally {
          taskAuditLoading.value = {
            ...taskAuditLoading.value,
            [task.taskId]: false,
          };
        }
      }
      function acceptPreference(value: DashboardPreference) {
        emit("preferenceSaved", value);
      }
      watchState(
        rebuildCandidates,
        (records) => {
          if (
            !records.some(
              (record) =>
                record.container.id === selectedRebuildContainerID.value,
            )
          ) {
            selectedRebuildContainerID.value = records[0]?.container.id ?? "";
          }
        },
        {
          immediate: true,
        },
      );
      beforeDispose(() => {
        probeSummaryEpoch++;
      });
      return () => (
        <>
          <section
            aria-labelledby="detail-title"
            data-testid="node-detail"
            className={__classes("node-detail-page workspace-page")}
            data-v-98aa91ae=""
          >
            <header className={__classes("detail-heading")} data-v-98aa91ae="">
              <div data-v-98aa91ae="">
                <h1 id="detail-title" data-v-98aa91ae="">
                  {nodeTitle.value}
                </h1>
                {nodeTitle.value !== __props.node.displayName ? (
                  <p data-v-98aa91ae="">{__props.node.displayName}</p>
                ) : null}
                {nodePreference.value?.notes ? (
                  <p
                    className={__classes("node-detail-note")}
                    data-v-98aa91ae=""
                  >
                    {nodePreference.value.notes}
                  </p>
                ) : null}
              </div>
              {activeSection.value !== "overview" ? (
              <div
                className={__classes("detail-heading-status")}
                data-v-98aa91ae=""
              >
                <span
                  data-online={__props.online as any}
                  className={__classes("node-detail-status")}
                  data-v-98aa91ae=""
                >
                  {__props.pending
                    ? t("m.a890b3be09aa")
                    : __props.online
                      ? t("m.b9086662b1df")
                      : t("m.be1b4f3c6c1c")}
                </span>
                <span
                  data-available={
                    __props.inventory?.dockerAvailability || ("unknown" as any)
                  }
                  data-stale={__props.dockerStale as any}
                  className={__classes("docker-state")}
                  data-v-98aa91ae=""
                >
                  {__props.inventory
                    ? dockerAvailabilityText(__props.inventory)
                    : __props.online
                      ? t("m.c12a50c262ca")
                      : t("m.8a6501e8198f")}
                </span>
              </div>
              ) : null}
            </header>
            {__props.pending ? (
              <div
                data-testid="node-management-actions"
                className={__classes("node-management-actions")}
                data-v-98aa91ae=""
              >
                {__props.pending ? (
                  <button
                    type="button"
                    aria-expanded={
                      enrollmentOpen.value &&
                      activeSection.value !== ("settings" as any)
                    }
                    data-testid="node-install-command"
                    className={__classes("secondary-button")}
                    onClick={(event: any) => {
                      __call(toggleEnrollment, event);
                    }}
                    data-v-98aa91ae=""
                  >
                    {t("node.installCommand")}
                  </button>
                ) : null}
              </div>
            ) : null}
            {__props.pending &&
            enrollmentOpen.value &&
            activeSection.value !== "settings" ? (
              <AgentEnrollment
                compact={true}
                nodeId={__props.node.nodeId as any}
                className={__classes("node-enrollment-panel")}
                data-v-98aa91ae=""
              ></AgentEnrollment>
            ) : null}
            {activeSection.value === "overview" && probeCounts.value.total ? (
              <div
                data-testid="probe-overview-summary"
                className={__classes("probe-overview-summary")}
                data-v-98aa91ae=""
              >
                <div data-v-98aa91ae="">
                  <p data-v-98aa91ae="">
                    {t("node.probes.summary", {
                      "0": probeCounts.value.total,
                      "1": probeCounts.value.healthy,
                      "2": probeCounts.value.unhealthy,
                      "3": probeCounts.value.unknown,
                      "4": probeCounts.value.disabled,
                    })}
                  </p>
                  {probeSummaryLoadedAt.value ? (
                    <small data-v-98aa91ae="">
                      {t("node.probes.loaded", {
                        "0": probeSummaryLoadedAt.value.toLocaleString(
                          locale.value,
                        ),
                      })}
                    </small>
                  ) : null}
                </div>
                <button
                  type="button"
                  className={__classes("secondary-button")}
                  onClick={(event: any) => {
                    selectSection("probes");
                  }}
                  data-v-98aa91ae=""
                >
                  {t("node.probes.open")}
                </button>
              </div>
            ) : null}
            {activeSection.value === "overview" && probeSummaryError.value ? (
              <p
                role="alert"
                className={__classes("probe-summary-error")}
                data-v-98aa91ae=""
              >
                {display(probeSummaryError.value)}
              </p>
            ) : null}
            {activeSection.value === "overview" && __props.metrics ? (
              <MetricsPanel
                key={__props.metrics.nodeId as any}
                view={__props.metrics as any}
                data-v-98aa91ae=""
              ></MetricsPanel>
            ) : activeSection.value === "overview" ? (
              <div
                data-testid="metrics-waiting"
                className={__classes("metrics-waiting")}
                data-v-98aa91ae=""
              >
                <span className={__classes("eyebrow")} data-v-98aa91ae="">
                  {__props.node.displayName}
                </span>
                <h2 data-v-98aa91ae="">
                  {__props.pending
                    ? t("m.270c0a7750e1")
                    : __props.online
                      ? t("m.78a4908b8949")
                      : t("m.f339ad3bc760")}
                </h2>
                {__props.pending ? (
                  <p data-v-98aa91ae="">{t("m.31483a081b8c")}</p>
                ) : __props.online ? (
                  <p data-v-98aa91ae="">{t("m.ad2b360a6731")}</p>
                ) : (
                  <p data-v-98aa91ae="">
                    {t("m.fcca199d47b9")}
                    {__props.nodeReason
                      ? t("m.fee0ec31e108", {
                          "0": __props.nodeReason,
                        })
                      : ""}
                  </p>
                )}
                <span
                  data-online={__props.online as any}
                  className={__classes("node-detail-status")}
                  data-v-98aa91ae=""
                >
                  {__props.pending
                    ? t("m.a890b3be09aa")
                    : __props.online
                      ? t("m.b9086662b1df")
                      : t("m.be1b4f3c6c1c")}
                </span>
              </div>
            ) : null}
            {activeSection.value === "docker" ? (
              <Fragment>
                <div
                  className={__classes("node-terminal-entry")}
                  data-v-98aa91ae=""
                >
                  <div data-v-98aa91ae="">
                    <strong data-v-98aa91ae="">{t("m.725457c84556")}</strong>
                    <small data-v-98aa91ae="">{t("m.a7c1e29fcce5")}</small>
                  </div>
                  <button
                    type="button"
                    disabled={!__props.online as any}
                    onClick={(event: any) => {
                      __call(openHostTerminal, event);
                    }}
                    data-v-98aa91ae=""
                  >
                    {t("m.b89a0e4584f2")}
                  </button>
                </div>
                <section
                  aria-labelledby="docker-title"
                  data-testid="docker-inventory"
                  className={__classes("docker-panel")}
                  data-v-98aa91ae=""
                >
                  <header
                    className={__classes("docker-heading")}
                    data-v-98aa91ae=""
                  >
                    <div data-v-98aa91ae="">
                      <span className={__classes("eyebrow")} data-v-98aa91ae="">
                        {t("label.c039e720422e")}
                      </span>
                      <h2 id="docker-title" data-v-98aa91ae="">
                        {t("m.7cabcc483eb4")}
                      </h2>
                    </div>
                    {__props.inventory ? (
                      <span
                        data-available={
                          __props.inventory.dockerAvailability as any
                        }
                        data-stale={__props.dockerStale as any}
                        className={__classes("docker-state")}
                        data-v-98aa91ae=""
                      >
                        {dockerAvailabilityText(__props.inventory)}
                      </span>
                    ) : null}
                  </header>
                  <ContainerCreateForm
                    disabled={!canCreateContainers.value as any}
                    submitting={createSubmitting.value as any}
                    error={createError.value as any}
                    onSubmit={(event: any) => {
                      __call(submitContainerCreate, event);
                    }}
                    data-v-98aa91ae=""
                  ></ContainerCreateForm>
                  {createTasks.value.length ? (
                    <section
                      aria-label={t("m.7084b942d42e") as any}
                      className={__classes("container-create-results")}
                      data-v-98aa91ae=""
                    >
                      <h3 data-v-98aa91ae="">{t("m.964c43bb9238")}</h3>
                      <Fragment>
                        {__each(createTasks.value, (task) => (
                          <article key={task.taskId as any} data-v-98aa91ae="">
                            <div data-v-98aa91ae="">
                              <strong data-v-98aa91ae="">
                                {taskStatusLabel(task)}
                              </strong>
                              <time
                                dateTime={task.createdAt as any}
                                data-v-98aa91ae=""
                              >
                                {new Date(task.createdAt).toLocaleString(
                                  locale.value,
                                )}
                              </time>
                            </div>
                            {task.status === "succeeded" &&
                            task.result.resourceRevision ? (
                              <code data-v-98aa91ae="">
                                {"Container ID："}
                                {task.result.resourceRevision}
                              </code>
                            ) : task.status === "failed" ? (
                              <span
                                className={__classes("container-task-error")}
                                data-v-98aa91ae=""
                              >
                                {task.result.code || "creation_failed"}
                              </span>
                            ) : null}
                          </article>
                        ))}
                      </Fragment>
                    </section>
                  ) : null}
                  <div
                    className={__classes("docker-view-controls")}
                    data-v-98aa91ae=""
                  >
                    <label data-v-98aa91ae="">
                      {t("m.44ce7ae909bb")}
                      <input
                        type="search"
                        placeholder={t("m.5a5d5c8b732a") as any}
                        aria-label={t("m.8db3ff343ebc") as any}
                        {...__input(
                          containerSearch.value,
                          (__nextValue: any) => {
                            containerSearch.value = __nextValue;
                          },
                          "search",
                          undefined,
                          [],
                        )}
                        data-v-98aa91ae=""
                      ></input>
                    </label>
                    <label data-v-98aa91ae="">
                      {t("docker.state")}
                      <UiSelect
                        modelValue={containerState.value}
                        onUpdateModelValue={(__nextValue: any) => {
                          containerState.value = __nextValue;
                        }}
                        modelModifiers={{}}
                        data-v-98aa91ae=""
                      >
                        <option value="all" data-v-98aa91ae="">
                          {t("docker.allStates")}
                        </option>
                        <option value="running" data-v-98aa91ae="">
                          {t("docker.running")}
                        </option>
                        <option value="stopped" data-v-98aa91ae="">
                          {t("docker.stopped")}
                        </option>
                        <option value="paused" data-v-98aa91ae="">
                          {t("docker.paused")}
                        </option>
                        <option value="restarting" data-v-98aa91ae="">
                          {t("docker.restarting")}
                        </option>
                      </UiSelect>
                    </label>
                    <label data-v-98aa91ae="">
                      {t("m.a96c9a854190")}
                      <UiSelect
                        modelValue={sortBy.value}
                        onUpdateModelValue={(__nextValue: any) => {
                          sortBy.value = __nextValue;
                        }}
                        modelModifiers={{}}
                        data-v-98aa91ae=""
                      >
                        <option value="custom" data-v-98aa91ae="">
                          {t("m.4eafa9e925b3")}
                        </option>
                        <option value="name" data-v-98aa91ae="">
                          {t("m.a98585871c53")}
                        </option>
                        <option value="state" data-v-98aa91ae="">
                          {t("m.e46b0272bd64")}
                        </option>
                      </UiSelect>
                    </label>
                  </div>
                  <div
                    className={__classes("docker-selection-bar")}
                    data-v-98aa91ae=""
                  >
                    <label data-v-98aa91ae="">
                      <input
                        type="checkbox"
                        checked={allPageSelected.value as any}
                        {...__indeterminate(somePageSelected.value)}
                        disabled={
                          !pageContainers.value.length ||
                          (selectionBusy.value as any)
                        }
                        onChange={(event: any) => {
                          togglePage(
                            (event.target as HTMLInputElement).checked,
                          );
                        }}
                        data-v-98aa91ae=""
                      ></input>
                      {t("docker.selectPage")}
                    </label>
                    <span role="status" data-v-98aa91ae="">
                      {t("docker.selected", {
                        "0": selectedContainers.value.length,
                        "1": sortedContainers.value.length,
                      })}
                    </span>
                    <button
                      type="button"
                      disabled={
                        !selectedContainers.value.length ||
                        (selectionBusy.value as any)
                      }
                      className={__classes("container-action")}
                      onClick={(event: any) => {
                        selectedContainerIDs.value = [];
                      }}
                      data-v-98aa91ae=""
                    >
                      {t("docker.clearSelection")}
                    </button>
                  </div>
                  <div
                    role="group"
                    aria-label={t("docker.toolbar") as any}
                    data-testid="docker-toolbar"
                    className={__classes("docker-toolbar")}
                    data-v-98aa91ae=""
                  >
                    <DockerActionButtons
                      can={selectionCan as any}
                      onAction={(event: any) => {
                        void submitSelection(event);
                      }}
                      data-v-98aa91ae=""
                    ></DockerActionButtons>
                    <details
                      className={__classes("docker-more")}
                      onKeyDown={(event: any) => {
                        event.preventDefault();
                        __call(closeMoreMenu, event);
                      }}
                      data-v-98aa91ae=""
                    >
                      <summary data-v-98aa91ae="">{t("docker.more")}</summary>
                      <div
                        className={__classes("docker-more-menu")}
                        onClick={(event: any) => {
                          __call(closeMoreMenu, event);
                        }}
                        data-v-98aa91ae=""
                      >
                        <button
                          type="button"
                          disabled={!selectionCan("pause") as any}
                          className={__classes("container-action")}
                          onClick={(event: any) => {
                            void submitSelection("pause");
                          }}
                          data-v-98aa91ae=""
                        >
                          {t("m.8d12fc0d4eb2")}
                        </button>
                        <button
                          type="button"
                          disabled={!selectionCan("resume") as any}
                          className={__classes("container-action")}
                          onClick={(event: any) => {
                            void submitSelection("resume");
                          }}
                          data-v-98aa91ae=""
                        >
                          {t("m.e0534b8a4e46")}
                        </button>
                        <button
                          type="button"
                          disabled={
                            !singleContainer.value ||
                            (!selectionCan("restart") as any)
                          }
                          className={__classes("container-action")}
                          onClick={(event: any) => {
                            singleContainer.value &&
                              openContainerTerminal(singleContainer.value);
                          }}
                          data-v-98aa91ae=""
                        >
                          {t("m.d0d07d153161")}
                        </button>
                        <button
                          type="button"
                          disabled={!singleContainer.value as any}
                          className={__classes("container-action")}
                          onClick={(event: any) => {
                            showPanel("info");
                          }}
                          data-v-98aa91ae=""
                        >
                          {t("docker.details")}
                        </button>
                        <button
                          type="button"
                          disabled={
                            !singleContainer.value ||
                            (Boolean(
                              rollbackTaskForContainer(
                                singleContainer.value.container.id,
                              ),
                            ) as any)
                          }
                          className={__classes("container-action")}
                          onClick={(event: any) => {
                            showPanel("streams");
                          }}
                          data-v-98aa91ae=""
                        >
                          {t("docker.streams")}
                        </button>
                        <button
                          type="button"
                          disabled={
                            !singleContainer.value ||
                            !preferenceForEditor(singleContainer.value) ||
                            (Boolean(
                              rollbackTaskForContainer(
                                singleContainer.value.container.id,
                              ),
                            ) as any)
                          }
                          className={__classes("container-action")}
                          onClick={(event: any) => {
                            showPanel("preferences");
                          }}
                          data-v-98aa91ae=""
                        >
                          {t("m.8299fda55003")}
                        </button>
                        <button
                          type="button"
                          disabled={
                            !singleContainer.value ||
                            (!taskForContainer(
                              singleContainer.value.container.id,
                            ) as any)
                          }
                          className={__classes("container-action")}
                          onClick={(event: any) => {
                            showPanel("audit");
                          }}
                          data-v-98aa91ae=""
                        >
                          {t("m.97f395dec8ce")}
                        </button>
                        <button
                          type="button"
                          disabled={
                            !singleContainer.value ||
                            !canOperate(singleContainer.value) ||
                            Boolean(singleContainer.value.container.compose) ||
                            (Boolean(
                              rollbackTaskForContainer(
                                singleContainer.value.container.id,
                              ),
                            ) as any)
                          }
                          className={__classes("container-action")}
                          onClick={(event: any) => {
                            __call(openSelectedRebuild, event);
                          }}
                          data-v-98aa91ae=""
                        >
                          {t("docker.rebuild")}
                        </button>
                        {singleContainer.value &&
                        safeServiceURL(singleContainer.value) ? (
                          <a
                            href={safeServiceURL(singleContainer.value) as any}
                            target="_blank"
                            rel="noopener noreferrer"
                            className={__classes(
                              "container-action service-link",
                            )}
                            data-v-98aa91ae=""
                          >
                            {t("m.e4fd01edc24f")}
                          </a>
                        ) : null}
                      </div>
                    </details>
                  </div>
                  <p
                    className={__classes("docker-selection-help")}
                    data-v-98aa91ae=""
                  >
                    {t("docker.selectionHelp")}
                  </p>
                  {singleContainer.value && detailPanel.value ? (
                    <section
                      data-testid="container-detail"
                      className={__classes("container-detail")}
                      data-v-98aa91ae=""
                    >
                      <header data-v-98aa91ae="">
                        <h3 data-v-98aa91ae="">
                          {containerTitle(singleContainer.value)}
                        </h3>
                        <button
                          type="button"
                          className={__classes("container-action")}
                          onClick={(event: any) => {
                            detailPanel.value = "";
                          }}
                          data-v-98aa91ae=""
                        >
                          {t("docker.closeDetails")}
                        </button>
                      </header>
                      {detailPanel.value === "info" ? (
                        <Fragment>
                          <dl data-v-98aa91ae="">
                            <dt data-v-98aa91ae="">{"ID"}</dt>
                            <dd data-v-98aa91ae="">
                              <code data-v-98aa91ae="">
                                {singleContainer.value.container.id}
                              </code>
                            </dd>
                            <dt data-v-98aa91ae="">{t("docker.image")}</dt>
                            <dd data-v-98aa91ae="">
                              {singleContainer.value.container.image}
                            </dd>
                            <dt data-v-98aa91ae="">{t("docker.network")}</dt>
                            <dd data-v-98aa91ae="">
                              {containerNetworkText(
                                singleContainer.value.container,
                              )}
                            </dd>
                            <dt data-v-98aa91ae="">{t("docker.observed")}</dt>
                            <dd data-v-98aa91ae="">
                              {new Date(
                                singleContainer.value.receivedAt,
                              ).toLocaleString(locale.value)}
                            </dd>
                          </dl>
                          {preferenceForContainer(singleContainer.value)
                            ?.notes ? (
                            <p data-v-98aa91ae="">
                              {
                                preferenceForContainer(singleContainer.value)
                                  ?.notes
                              }
                            </p>
                          ) : null}
                          {singleContainer.value.container.unavailableReason ? (
                            <p
                              className={__classes("container-task-error")}
                              data-v-98aa91ae=""
                            >
                              {
                                singleContainer.value.container
                                  .unavailableReason
                              }
                            </p>
                          ) : null}
                          {rollbackTaskForContainer(
                            singleContainer.value.container.id,
                          ) ? (
                            <p
                              className={__classes("container-rollback-note")}
                              data-v-98aa91ae=""
                            >
                              {t("m.322728a307b5")}
                            </p>
                          ) : null}
                        </Fragment>
                      ) : null}
                      {detailPanel.value === "streams" ? (
                        <ContainerStreams
                          key={singleContainer.value.container.id as any}
                          nodeId={__props.node.nodeId as any}
                          containerId={
                            singleContainer.value.container.id as any
                          }
                          disabled={
                            !__props.online ||
                            __props.dockerStale ||
                            (singleContainer.value.container.stale as any)
                          }
                          statsAvailable={
                            singleContainer.value.container.running &&
                            !singleContainer.value.container.paused &&
                            (!singleContainer.value.container.restarting as any)
                          }
                          data-v-98aa91ae=""
                        ></ContainerStreams>
                      ) : null}
                      {detailPanel.value === "preferences" &&
                      preferenceForEditor(singleContainer.value) ? (
                        <PreferenceEditor
                          key={singleContainer.value.container.id as any}
                          preference={
                            preferenceForEditor(singleContainer.value)! as any
                          }
                          title={containerTitle(singleContainer.value) as any}
                          onSaved={(event: any) => {
                            __call(acceptPreference, event);
                          }}
                          data-v-98aa91ae=""
                        ></PreferenceEditor>
                      ) : null}
                      {detailPanel.value === "audit" &&
                      taskForContainer(singleContainer.value.container.id) ? (
                        <Fragment>
                          {taskAuditErrors.value[
                            taskForContainer(
                              singleContainer.value.container.id,
                            )!.taskId
                          ] ? (
                            <p
                              role="alert"
                              className={__classes("container-task-error")}
                              data-v-98aa91ae=""
                            >
                              {display(
                                taskAuditErrors.value[
                                  taskForContainer(
                                    singleContainer.value.container.id,
                                  )!.taskId
                                ],
                              )}
                            </p>
                          ) : null}
                          <ol
                            className={__classes("container-task-audit")}
                            data-v-98aa91ae=""
                          >
                            {taskAuditLoading.value[
                              taskForContainer(
                                singleContainer.value.container.id,
                              )!.taskId
                            ] ? (
                              <li data-v-98aa91ae="">{t("m.cf62890bdb61")}</li>
                            ) : null}
                            <Fragment>
                              {__each(
                                taskAuditViews.value[
                                  taskForContainer(
                                    singleContainer.value.container.id,
                                  )!.taskId
                                ] ?? [],
                                (event) => (
                                  <li key={event.id as any} data-v-98aa91ae="">
                                    {event.event}
                                    {" · "}
                                    {event.fromStatus || t("m.d2bb025a2e51")}
                                    {" → "}
                                    {event.toStatus || t("m.fb7e71452c04")}
                                    {" · "}
                                    {new Date(event.occurredAt).toLocaleString(
                                      locale.value,
                                    )}
                                  </li>
                                ),
                              )}
                            </Fragment>
                          </ol>
                        </Fragment>
                      ) : null}
                    </section>
                  ) : null}
                  {!__props.inventory ? (
                    <p className={__classes("docker-empty")} data-v-98aa91ae="">
                      {t("m.8a4a01f575e0")}
                    </p>
                  ) : __props.inventory.dockerAvailability === "unavailable" ||
                    __props.inventory.staleReason ===
                      "docker_capability_unavailable" ? (
                    <p className={__classes("docker-empty")} data-v-98aa91ae="">
                      {t("m.758a85e024fd")}
                      {dockerStatusReason(__props.inventory)
                        ? t("m.8504c2226f79", {
                            "0": dockerStatusReason(__props.inventory),
                          })
                        : ""}
                    </p>
                  ) : dockerStatusReason(__props.inventory) ? (
                    <p
                      data-testid="docker-health-reason"
                      className={__classes("docker-empty")}
                      data-v-98aa91ae=""
                    >
                      {t("m.369cbdaf8c6d")}
                      {dockerStatusReason(__props.inventory)}
                    </p>
                  ) : null}
                  {__props.inventory && !__props.inventory.containers.length ? (
                    <p className={__classes("docker-empty")} data-v-98aa91ae="">
                      {__props.dockerStale
                        ? t("m.647742c56530")
                        : t("m.0e149c0e416b")}
                    </p>
                  ) : __props.inventory && !sortedContainers.value.length ? (
                    <p className={__classes("docker-empty")} data-v-98aa91ae="">
                      {t("m.b8e005f5fbdf")}
                    </p>
                  ) : null}
                  {pageContainers.value.length ? (
                    <div
                      className={__classes("docker-table-scroll")}
                      data-v-98aa91ae=""
                    >
                      <table
                        data-testid="docker-table"
                        className={__classes("container-table")}
                        data-v-98aa91ae=""
                      >
                        <caption
                          className={__classes("visually-hidden")}
                          data-v-98aa91ae=""
                        >
                          {t("docker.list")}
                        </caption>
                        <thead data-v-98aa91ae="">
                          <tr data-v-98aa91ae="">
                            <th scope="col" data-v-98aa91ae="">
                              <span
                                className={__classes("visually-hidden")}
                                data-v-98aa91ae=""
                              >
                                {t("docker.selection")}
                              </span>
                            </th>
                            <th scope="col" data-v-98aa91ae="">
                              {t("docker.name")}
                            </th>
                            {__props.dashboardSettings.customFields.includes(
                              "image",
                            ) ? (
                              <th scope="col" data-v-98aa91ae="">
                                {t("docker.image")}
                              </th>
                            ) : null}
                            {__props.dashboardSettings.customFields.includes(
                              "state",
                            ) ||
                            __props.dashboardSettings.customFields.includes(
                              "health",
                            ) ? (
                              <th scope="col" data-v-98aa91ae="">
                                {t("docker.state")}
                              </th>
                            ) : null}
                            {__props.dashboardSettings.customFields.includes(
                              "ports",
                            ) ? (
                              <th scope="col" data-v-98aa91ae="">
                                {t("docker.ports")}
                              </th>
                            ) : null}
                            {__props.dashboardSettings.customFields.includes(
                              "uptime",
                            ) ? (
                              <th scope="col" data-v-98aa91ae="">
                                {t("docker.uptime")}
                              </th>
                            ) : null}
                            <th scope="col" data-v-98aa91ae="">
                              {t("docker.task")}
                            </th>
                          </tr>
                        </thead>
                        <tbody data-v-98aa91ae="">
                          <Fragment>
                            {__each(pageContainers.value, (record) => (
                              <tr
                                key={record.container.id as any}
                                data-container-id={record.container.id as any}
                                data-selected={
                                  selectedContainerIDs.value.includes(
                                    record.container.id,
                                  ) as any
                                }
                                data-stale={
                                  __props.dockerStale ||
                                  (record.container.stale as any)
                                }
                                className={__classes("docker-row")}
                                data-v-98aa91ae=""
                              >
                                <td
                                  className={__classes("container-select")}
                                  data-v-98aa91ae=""
                                >
                                  <input
                                    type="checkbox"
                                    checked={
                                      selectedContainerIDs.value.includes(
                                        record.container.id,
                                      ) as any
                                    }
                                    disabled={selectionBusy.value as any}
                                    aria-label={
                                      t("docker.selectContainer", {
                                        "0": containerTitle(record),
                                      }) as any
                                    }
                                    onChange={(event: any) => {
                                      toggleContainer(
                                        record.container.id,
                                        (event.target as HTMLInputElement)
                                          .checked,
                                      );
                                    }}
                                    data-v-98aa91ae=""
                                  ></input>
                                </td>
                                <td
                                  data-label={t("docker.name") as any}
                                  data-v-98aa91ae=""
                                >
                                  <button
                                    type="button"
                                    className={__classes("container-name")}
                                    onClick={(event: any) => {
                                      inspectContainer(record);
                                    }}
                                    data-v-98aa91ae=""
                                  >
                                    {containerTitle(record)}
                                  </button>
                                  <small data-v-98aa91ae="">
                                    {record.container.id.slice(0, 12)}
                                    {containerTitle(record) !==
                                    record.container.name ? (
                                      <Fragment>
                                        {" · "}
                                        {record.container.name}
                                      </Fragment>
                                    ) : null}
                                  </small>
                                  {record.container.compose ? (
                                    <small data-v-98aa91ae="">
                                      {record.container.compose.project}
                                      {"/"}
                                      {record.container.compose.service}
                                    </small>
                                  ) : null}
                                </td>
                                {__props.dashboardSettings.customFields.includes(
                                  "image",
                                ) ? (
                                  <td
                                    data-label={t("docker.image") as any}
                                    data-v-98aa91ae=""
                                  >
                                    <code data-v-98aa91ae="">
                                      {record.container.image ||
                                        t("m.45c82e228c16")}
                                    </code>
                                  </td>
                                ) : null}
                                {__props.dashboardSettings.customFields.includes(
                                  "state",
                                ) ||
                                __props.dashboardSettings.customFields.includes(
                                  "health",
                                ) ? (
                                  <td
                                    data-label={t("docker.state") as any}
                                    data-v-98aa91ae=""
                                  >
                                    {rollbackTaskForContainer(
                                      record.container.id,
                                    ) ? (
                                      <span
                                        className={__classes(
                                          "container-state container-rollback-state",
                                        )}
                                        data-v-98aa91ae=""
                                      >
                                        {t("m.557f68233fbd")}
                                      </span>
                                    ) : __props.dashboardSettings.customFields.includes(
                                        "state",
                                      ) ? (
                                      <span
                                        data-running={
                                          record.container.running as any
                                        }
                                        className={__classes("container-state")}
                                        data-v-98aa91ae=""
                                      >
                                        {record.container.state ||
                                          t("m.4d8c1c5b4283")}
                                      </span>
                                    ) : null}
                                    {__props.dashboardSettings.customFields.includes(
                                      "health",
                                    ) ? (
                                      <small
                                        data-health={
                                          record.container.health as any
                                        }
                                        data-v-98aa91ae=""
                                      >
                                        {containerHealthText(record.container)}
                                      </small>
                                    ) : null}
                                    {__props.dockerStale ||
                                    record.container.stale ? (
                                      <small
                                        className={__classes("container-stale")}
                                        data-v-98aa91ae=""
                                      >
                                        {t("m.6fe221cef061")}
                                      </small>
                                    ) : null}
                                  </td>
                                ) : null}
                                {__props.dashboardSettings.customFields.includes(
                                  "ports",
                                ) ? (
                                  <td
                                    data-label={t("docker.ports") as any}
                                    data-v-98aa91ae=""
                                  >
                                    <Fragment>
                                      {__each(
                                        record.container.ports ?? [],
                                        (port, index) => (
                                          <span
                                            key={index as any}
                                            className={__classes(
                                              "container-port",
                                            )}
                                            data-v-98aa91ae=""
                                          >
                                            {portText(port)}
                                          </span>
                                        ),
                                      )}
                                    </Fragment>
                                    {!record.container.ports?.length ? (
                                      <span data-v-98aa91ae="">{"—"}</span>
                                    ) : null}
                                  </td>
                                ) : null}
                                {__props.dashboardSettings.customFields.includes(
                                  "uptime",
                                ) ? (
                                  <td
                                    data-label={t("docker.uptime") as any}
                                    data-v-98aa91ae=""
                                  >
                                    {containerUptime(record)}
                                  </td>
                                ) : null}
                                <td
                                  data-label={t("docker.task") as any}
                                  data-v-98aa91ae=""
                                >
                                  {__props.taskSubmitting[
                                    taskKey(record.container.id)
                                  ] ? (
                                    <span role="status" data-v-98aa91ae="">
                                      {t("m.911ee461c36a")}
                                    </span>
                                  ) : taskForContainer(record.container.id) ? (
                                    <span
                                      data-status={
                                        taskForContainer(record.container.id)
                                          ?.status as any
                                      }
                                      className={__classes(
                                        "container-task-status",
                                      )}
                                      data-v-98aa91ae=""
                                    >
                                      {taskStatusLabel(
                                        taskForContainer(record.container.id)!,
                                      )}
                                    </span>
                                  ) : (
                                    <span data-v-98aa91ae="">{"—"}</span>
                                  )}
                                  {__props.taskErrors[
                                    taskKey(record.container.id)
                                  ] ? (
                                    <small
                                      role="alert"
                                      className={__classes(
                                        "container-task-error",
                                      )}
                                      data-v-98aa91ae=""
                                    >
                                      {display(
                                        __props.taskErrors[
                                          taskKey(record.container.id)
                                        ],
                                      )}
                                    </small>
                                  ) : null}
                                </td>
                              </tr>
                            ))}
                          </Fragment>
                        </tbody>
                      </table>
                    </div>
                  ) : null}
                  {sortedContainers.value.length ? (
                    <nav
                      aria-label={t("docker.pagination") as any}
                      className={__classes("docker-pagination")}
                      data-v-98aa91ae=""
                    >
                      <label data-v-98aa91ae="">
                        {t("docker.pageSize")}
                        <UiSelect
                          modelValue={pageSize.value}
                          onUpdateModelValue={(__nextValue: any) => {
                            pageSize.value = __nextValue;
                          }}
                          modelModifiers={{ number: true }}
                          data-v-98aa91ae=""
                        >
                          <option value={20 as any} data-v-98aa91ae="">
                            {"20"}
                          </option>
                          <option value={50 as any} data-v-98aa91ae="">
                            {"50"}
                          </option>
                          <option value={100 as any} data-v-98aa91ae="">
                            {"100"}
                          </option>
                        </UiSelect>
                      </label>
                      <span data-v-98aa91ae="">
                        {t("docker.page", {
                          "0": containerPage.value,
                          "1": pageCount.value,
                        })}
                      </span>
                      <button
                        type="button"
                        disabled={(containerPage.value <= 1) as any}
                        className={__classes("container-action")}
                        onClick={(event: any) => {
                          containerPage.value--;
                        }}
                        data-v-98aa91ae=""
                      >
                        {t("docker.previous")}
                      </button>
                      <button
                        type="button"
                        disabled={
                          (containerPage.value >= pageCount.value) as any
                        }
                        className={__classes("container-action")}
                        onClick={(event: any) => {
                          containerPage.value++;
                        }}
                        data-v-98aa91ae=""
                      >
                        {t("docker.next")}
                      </button>
                    </nav>
                  ) : null}
                </section>
              </Fragment>
            ) : null}
            {activeSection.value === "images" ? (
              <ImagesPanel
                key={__props.node.nodeId as any}
                nodeId={__props.node.nodeId as any}
                data-v-98aa91ae=""
              ></ImagesPanel>
            ) : null}
            {activeSection.value === "compose" ? (
              <ComposeProjects
                key={__props.node.nodeId as any}
                nodeId={__props.node.nodeId as any}
                data-v-98aa91ae=""
              ></ComposeProjects>
            ) : null}
            {activeSection.value === "rebuild" ? (
              <section
                aria-label={t("m.e5634738490a") as any}
                data-testid="container-rebuild-page"
                className={__classes("section-panel container-rebuild-page")}
                data-v-98aa91ae=""
              >
                <header
                  className={__classes("section-toolbar")}
                  data-v-98aa91ae=""
                >
                  <div data-v-98aa91ae="">
                    <span className={__classes("eyebrow")} data-v-98aa91ae="">
                      {t("label.08671c7919d1")}
                    </span>
                    <h2 data-v-98aa91ae="">{t("m.e5634738490a")}</h2>
                  </div>
                </header>
                <p
                  className={__classes("rebuild-safety-note")}
                  data-v-98aa91ae=""
                >
                  {t("m.acc2b82becc5")}
                </p>
                {!rebuildCandidates.value.length ? (
                  <p className={__classes("docker-empty")} data-v-98aa91ae="">
                    {t("m.59042e079f41")}
                  </p>
                ) : (
                  <Fragment>
                    <label
                      className={__classes("rebuild-container-picker")}
                      data-v-98aa91ae=""
                    >
                      {t("m.4210fc8f913a")}
                      <UiSelect
                        modelValue={selectedRebuildContainerID.value}
                        onUpdateModelValue={(__nextValue: any) => {
                          selectedRebuildContainerID.value = __nextValue;
                        }}
                        modelModifiers={{}}
                        data-v-98aa91ae=""
                      >
                        <Fragment>
                          {__each(rebuildCandidates.value, (record) => (
                            <option
                              key={record.container.id as any}
                              value={record.container.id as any}
                              data-v-98aa91ae=""
                            >
                              {containerTitle(record)}
                              {" · "}
                              {record.container.id.slice(0, 12)}
                            </option>
                          ))}
                        </Fragment>
                      </UiSelect>
                    </label>
                    {selectedRebuildContainer.value ? (
                      <ContainerRebuildWizard
                        key={selectedRebuildContainer.value.container.id as any}
                        nodeId={__props.node.nodeId as any}
                        container={
                          selectedRebuildContainer.value.container as any
                        }
                        stale={
                          !canOperate(selectedRebuildContainer.value) as any
                        }
                        tasks={__props.tasks as any}
                        data-v-98aa91ae=""
                      ></ContainerRebuildWizard>
                    ) : null}
                  </Fragment>
                )}
              </section>
            ) : null}
            {activeSection.value === "files" ? (
              <NodeFiles
                key={__props.node.nodeId as any}
                nodeId={__props.node.nodeId as any}
                nodeName={__props.node.displayName as any}
                data-v-98aa91ae=""
              ></NodeFiles>
            ) : null}
            {activeSection.value === "history" ? (
              <section
                aria-label={t("m.792a0100ee53") as any}
                className={__classes("section-panel")}
                data-v-98aa91ae=""
              >
                <div
                  className={__classes("section-toolbar")}
                  data-v-98aa91ae=""
                >
                  <div data-v-98aa91ae="">
                    <span className={__classes("eyebrow")} data-v-98aa91ae="">
                      {t("label.8f7889f6a879")}
                    </span>
                    <h2 data-v-98aa91ae="">
                      {t("m.fdc402785198")}
                      {nodeTitle.value}
                    </h2>
                  </div>
                  <div
                    className={__classes("history-controls")}
                    data-v-98aa91ae=""
                  >
                    <label data-v-98aa91ae="">
                      {t("m.ae72b532f4b8")}
                      <UiSelect
                        modelValue={historyRange.value}
                        onUpdateModelValue={(__nextValue: any) => {
                          historyRange.value = __nextValue;
                        }}
                        modelModifiers={{}}
                        data-v-98aa91ae=""
                      >
                        <option value="6h" data-v-98aa91ae="">
                          {t("m.320e9ce290e8")}
                        </option>
                        <option value="24h" data-v-98aa91ae="">
                          {t("m.ab2c18802e55")}
                        </option>
                        <option value="7d" data-v-98aa91ae="">
                          {t("m.2b39b90c0558")}
                        </option>
                        <option value="30d" data-v-98aa91ae="">
                          {t("m.c5f1c28c0d5f")}
                        </option>
                        <option value="1y" data-v-98aa91ae="">
                          {t("m.80c3d880f104")}
                        </option>
                      </UiSelect>
                    </label>
                    <button
                      type="button"
                      disabled={historyLoading.value as any}
                      className={__classes("container-action")}
                      onClick={(event: any) => {
                        void loadHistory();
                      }}
                      data-v-98aa91ae=""
                    >
                      {t("m.bcd6771e08ec")}
                    </button>
                  </div>
                </div>
                <HistoricalMetrics
                  history={historyView.value as any}
                  loading={historyLoading.value as any}
                  error={historyError.value as any}
                  nodeOnline={__props.online as any}
                  data-v-98aa91ae=""
                ></HistoricalMetrics>
                <p className={__classes("retention-note")} data-v-98aa91ae="">
                  {t("m.b3b9fb911d3a")}
                </p>
              </section>
            ) : null}
            {activeSection.value === "events" ? (
              <section
                aria-label={t("m.5661bd931598") as any}
                data-testid="node-event-history"
                className={__classes("section-panel")}
                data-v-98aa91ae=""
              >
                <header
                  className={__classes("section-toolbar")}
                  data-v-98aa91ae=""
                >
                  <div data-v-98aa91ae="">
                    <span className={__classes("eyebrow")} data-v-98aa91ae="">
                      {"RECENT OPERATIONS"}
                    </span>
                    <h2 data-v-98aa91ae="">
                      {t("m.7d2e3cfc51fb")}
                      {nodeTitle.value}
                    </h2>
                  </div>
                  <button
                    type="button"
                    className={__classes("container-action")}
                    onClick={(event: any) => {
                      emit("refreshTasks", __props.node.nodeId);
                    }}
                    data-v-98aa91ae=""
                  >
                    {t("m.aee887434131")}
                  </button>
                </header>
                <p
                  className={__classes("event-source-note")}
                  data-v-98aa91ae=""
                >
                  {t("m.30a9a37d5d32")}
                </p>
                {selectedTasks.value.length === 0 ? (
                  <p className={__classes("docker-empty")} data-v-98aa91ae="">
                    {t("m.a9d063f3f3a9")}
                  </p>
                ) : null}
                <Fragment>
                  {__each(selectedTasks.value, (task) => (
                    <article
                      key={task.taskId as any}
                      data-status={task.status as any}
                      className={__classes("event-card")}
                      data-v-98aa91ae=""
                    >
                      <div data-v-98aa91ae="">
                        <strong data-v-98aa91ae="">
                          {task.action}
                          {" · "}
                          {task.targetId.slice(0, 12)}
                        </strong>
                        <span
                          data-status={task.status as any}
                          className={__classes("container-task-status")}
                          data-v-98aa91ae=""
                        >
                          {taskStatusText(task)}
                        </span>
                        <time
                          dateTime={task.createdAt as any}
                          data-v-98aa91ae=""
                        >
                          {new Date(task.createdAt).toLocaleString(
                            locale.value,
                          )}
                        </time>
                      </div>
                      <button
                        type="button"
                        className={__classes("container-action")}
                        onClick={(event: any) => {
                          toggleTaskAudit(task);
                        }}
                        data-v-98aa91ae=""
                      >
                        {taskAuditVisible.value[task.taskId]
                          ? t("m.54e74c1e7504")
                          : t("m.97f395dec8ce")}
                      </button>
                      {taskAuditErrors.value[task.taskId] ? (
                        <p
                          role="alert"
                          className={__classes("container-task-error")}
                          data-v-98aa91ae=""
                        >
                          {display(taskAuditErrors.value[task.taskId])}
                        </p>
                      ) : null}
                      {taskAuditVisible.value[task.taskId] ? (
                        <ol
                          className={__classes("container-task-audit")}
                          data-v-98aa91ae=""
                        >
                          {taskAuditLoading.value[task.taskId] ? (
                            <li data-v-98aa91ae="">{t("m.cf62890bdb61")}</li>
                          ) : null}
                          <Fragment>
                            {__each(
                              taskAuditViews.value[task.taskId] ?? [],
                              (event) => (
                                <li key={event.id as any} data-v-98aa91ae="">
                                  {event.event}
                                  {" · "}
                                  {event.fromStatus || t("m.d2bb025a2e51")}
                                  {" → "}
                                  {event.toStatus || t("m.fb7e71452c04")}
                                  {" · "}
                                  {new Date(event.occurredAt).toLocaleString(
                                    locale.value,
                                  )}
                                </li>
                              ),
                            )}
                          </Fragment>
                        </ol>
                      ) : null}
                    </article>
                  ))}
                </Fragment>
              </section>
            ) : null}
            {activeSection.value === "settings" ? (
              <section
                aria-label={t("m.0a09fda5e2a6") as any}
                data-testid="node-settings"
                className={__classes("section-panel node-settings-panel")}
                data-v-98aa91ae=""
              >
                <header
                  className={__classes("section-toolbar")}
                  data-v-98aa91ae=""
                >
                  <div data-v-98aa91ae="">
                    <span className={__classes("eyebrow")} data-v-98aa91ae="">
                      {t("label.26ae0bd221f2")}
                    </span>
                    <h2 data-v-98aa91ae="">
                      {t("m.1b8b4303f8ba")}
                      {nodeTitle.value}
                    </h2>
                  </div>
                </header>
                <div
                  className={__classes("node-settings-status")}
                  data-v-98aa91ae=""
                >
                  <div data-v-98aa91ae="">
                    <span data-v-98aa91ae="">{t("m.f5514ad14eaa")}</span>
                    <strong data-v-98aa91ae="">
                      {__props.pending
                        ? t("m.a890b3be09aa")
                        : __props.online
                          ? t("m.b9086662b1df")
                          : t("m.be1b4f3c6c1c")}
                    </strong>
                  </div>
                  <div data-v-98aa91ae="">
                    <span data-v-98aa91ae="">{"Docker Engine"}</span>
                    <strong data-v-98aa91ae="">
                      {__props.inventory
                        ? dockerAvailabilityText(__props.inventory)
                        : t("m.4d8c1c5b4283")}
                    </strong>
                  </div>
                  <div data-v-98aa91ae="">
                    <span data-v-98aa91ae="">{t("m.e31132b9de5a")}</span>
                    <strong data-v-98aa91ae="">
                      {__props.node.agentVersion || t("m.4d8c1c5b4283")}
                    </strong>
                  </div>
                  <div data-v-98aa91ae="">
                    <span data-v-98aa91ae="">{t("m.bf04f29ea311")}</span>
                    <strong data-v-98aa91ae="">{__props.node.nodeId}</strong>
                  </div>
                </div>
                <PreferenceEditor
                  preference={nodePreferenceForEditor() as any}
                  title={
                    t("m.b8d1ded2a785", {
                      "0": __props.node.displayName,
                    }) as any
                  }
                  onSaved={(event: any) => {
                    __call(acceptPreference, event);
                  }}
                  data-v-98aa91ae=""
                ></PreferenceEditor>
                <div
                  className={__classes("dashboard-settings-editor")}
                  data-v-98aa91ae=""
                >
                  <h3 data-v-98aa91ae="">{t("m.f917a66dfc4f")}</h3>
                  <div
                    className={__classes("dashboard-settings-grid")}
                    data-v-98aa91ae=""
                  >
                    <label data-v-98aa91ae="">
                      {t("m.b91569c6e33a")}
                      <UiSelect
                        modelValue={viewMode.value}
                        onUpdateModelValue={(__nextValue: any) => {
                          viewMode.value = __nextValue;
                        }}
                        modelModifiers={{}}
                        data-v-98aa91ae=""
                      >
                        <option value="monitor" data-v-98aa91ae="">
                          {t("m.fdc390061698")}
                        </option>
                        <option value="manage" data-v-98aa91ae="">
                          {t("m.bb6d995724f4")}
                        </option>
                      </UiSelect>
                    </label>
                    <label data-v-98aa91ae="">
                      {t("m.65cd544c6486")}
                      <UiSelect
                        modelValue={nodeGroupBy.value}
                        onUpdateModelValue={(__nextValue: any) => {
                          nodeGroupBy.value = __nextValue;
                        }}
                        modelModifiers={{}}
                        data-v-98aa91ae=""
                      >
                        <option value="status" data-v-98aa91ae="">
                          {t("m.b8f945ea49ff")}
                        </option>
                        <option value="group" data-v-98aa91ae="">
                          {t("m.c08b046b5470")}
                        </option>
                        <option value="none" data-v-98aa91ae="">
                          {t("m.fcd40ee3f05a")}
                        </option>
                      </UiSelect>
                    </label>
                    <label data-v-98aa91ae="">
                      {t("m.dd74dd92d820")}
                      <UiSelect
                        modelValue={nodeSortBy.value}
                        onUpdateModelValue={(__nextValue: any) => {
                          nodeSortBy.value = __nextValue;
                        }}
                        modelModifiers={{}}
                        data-v-98aa91ae=""
                      >
                        <option value="custom" data-v-98aa91ae="">
                          {t("m.0f59d3742e06")}
                        </option>
                        <option value="name" data-v-98aa91ae="">
                          {t("m.a98585871c53")}
                        </option>
                        <option value="status" data-v-98aa91ae="">
                          {t("m.b8f945ea49ff")}
                        </option>
                      </UiSelect>
                    </label>
                    <label data-v-98aa91ae="">
                      {t("m.f143cf3535b6")}
                      <UiSelect
                        modelValue={groupBy.value}
                        onUpdateModelValue={(__nextValue: any) => {
                          groupBy.value = __nextValue;
                        }}
                        modelModifiers={{}}
                        data-v-98aa91ae=""
                      >
                        <option value="node" data-v-98aa91ae="">
                          {t("m.2410d860f093")}
                        </option>
                        <option value="compose" data-v-98aa91ae="">
                          {t("m.5f67dd75b01f")}
                        </option>
                        <option value="state" data-v-98aa91ae="">
                          {t("m.e46b0272bd64")}
                        </option>
                        <option value="none" data-v-98aa91ae="">
                          {t("m.fcd40ee3f05a")}
                        </option>
                      </UiSelect>
                    </label>
                    <label data-v-98aa91ae="">
                      {t("m.30d908c60571")}
                      <UiSelect
                        modelValue={sortBy.value}
                        onUpdateModelValue={(__nextValue: any) => {
                          sortBy.value = __nextValue;
                        }}
                        modelModifiers={{}}
                        data-v-98aa91ae=""
                      >
                        <option value="custom" data-v-98aa91ae="">
                          {t("m.0f59d3742e06")}
                        </option>
                        <option value="name" data-v-98aa91ae="">
                          {t("m.a98585871c53")}
                        </option>
                        <option value="state" data-v-98aa91ae="">
                          {t("m.e46b0272bd64")}
                        </option>
                      </UiSelect>
                    </label>
                    <label data-v-98aa91ae="">
                      {t("m.70006f6c5230")}
                      <input
                        type="number"
                        min="1"
                        max="20"
                        {...__input(
                          featuredLimit.value,
                          (__nextValue: any) => {
                            featuredLimit.value = __nextValue;
                          },
                          "number",
                          undefined,
                          ["number"],
                        )}
                        data-v-98aa91ae=""
                      ></input>
                    </label>
                  </div>
                  <fieldset
                    className={__classes("custom-fields")}
                    data-v-98aa91ae=""
                  >
                    <legend data-v-98aa91ae="">{t("m.25ebfb95e8d6")}</legend>
                    <Fragment>
                      {__each(customFieldOptions.value, (field) => (
                        <label key={field.id as any} data-v-98aa91ae="">
                          <input
                            type="checkbox"
                            value={field.id as any}
                            {...__input(
                              customFields.value,
                              (__nextValue: any) => {
                                customFields.value = __nextValue;
                              },
                              "checkbox",
                              field.id,
                              [],
                            )}
                            data-v-98aa91ae=""
                          ></input>
                          {field.label}
                        </label>
                      ))}
                    </Fragment>
                  </fieldset>
                  <div
                    className={__classes("settings-save")}
                    data-v-98aa91ae=""
                  >
                    <button
                      type="button"
                      disabled={__props.dashboardSettingsSaving as any}
                      className={__classes("container-action")}
                      onClick={(event: any) => {
                        emit("saveSettings");
                      }}
                      data-v-98aa91ae=""
                    >
                      {__props.dashboardSettingsSaving
                        ? t("m.ff509c9ba052")
                        : t("m.1d6a782c4ccc")}
                    </button>
                    {__props.dashboardSettingsMessage ? (
                      <span role="status" data-v-98aa91ae="">
                        {display(__props.dashboardSettingsMessage)}
                      </span>
                    ) : null}
                    {__props.dashboardSettingsError ? (
                      <span role="alert" data-v-98aa91ae="">
                        {display(__props.dashboardSettingsError)}
                      </span>
                    ) : null}
                  </div>
                </div>
                <p
                  className={__classes("preference-separation-note")}
                  data-v-98aa91ae=""
                >
                  {t("m.02f03b13557b")}
                </p>
                <NodeAssetSettings
                  key={__props.node.nodeId as any}
                  nodeId={__props.node.nodeId as any}
                  data-v-98aa91ae=""
                ></NodeAssetSettings>
                <footer
                  data-testid="node-settings-delete"
                  className={__classes("node-settings-delete")}
                  data-v-98aa91ae=""
                >
                  <button
                    type="button"
                    disabled={deletingNode.value as any}
                    data-testid="node-delete"
                    className={__classes("secondary-button node-delete-button")}
                    onClick={(event: any) => {
                      __call(deleteNode, event);
                    }}
                    data-v-98aa91ae=""
                  >
                    {deletingNode.value
                      ? t("m.ff509c9ba052")
                      : t("node.delete")}
                  </button>
                  {nodeActionError.value ? (
                    <p role="alert" data-v-98aa91ae="">
                      {display(nodeActionError.value)}
                    </p>
                  ) : null}
                </footer>
              </section>
            ) : null}
            {activeSection.value === "probes" ? (
              <NodeServiceProbes
                key={__props.node.nodeId as any}
                nodeId={__props.node.nodeId as any}
                nodeName={__props.node.displayName as any}
                nodeOnline={__props.online as any}
                data-v-98aa91ae=""
              ></NodeServiceProbes>
            ) : null}
            {activeTerminal.value ? (
              <TerminalConsole
                {...activeTerminal.value}
                onClose={(event: any) => {
                  activeTerminal.value = null;
                }}
                data-v-98aa91ae=""
              ></TerminalConsole>
            ) : null}
          </section>
        </>
      );
    },
    input,
    [
      "readOnly",
      "node",
      "metrics",
      "inventory",
      "preferences",
      "preferenceIdentities",
      "online",
      "pending",
      "dockerStale",
      "nodeReason",
      "tasks",
      "taskSubmitting",
      "taskErrors",
      "dashboardSettings",
      "dashboardSettingsSaving",
      "dashboardSettingsMessage",
      "dashboardSettingsError",
      "currentTime",
    ],
    [],
    {},
    true,
  );
}
