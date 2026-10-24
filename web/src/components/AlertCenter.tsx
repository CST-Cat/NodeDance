import { ExpandedTextarea } from "./ExpandedTextarea";
import {
  api,
  operationRequest,
  type AgentNode,
  type AlertChannel,
  type ChannelProvider,
  type AlertDelivery,
  type AlertEvent,
  type AlertItem,
  type AlertRule,
  type AlertWindow,
} from "../api";
import { display, locale, t } from "../i18n";
import { useViewTab } from "../navigation";
import {
  afterMount,
  beforeDispose,
  cell,
  derived,
  observable,
  watchState,
} from "../state";

import { useController } from "../state";
import { cloneElement, useRef, type ReactElement } from "react";
import {
  ManagementPage,
  Input,
  Select,
  Checkbox,
  Button,
  Badge,
  Alert,
  Tabs,
  TabsList,
  TabsTrigger,
} from "./ManagementUI";
import { Dialog, DialogContent } from "./arc/dialog/dialog";
import { TabsContent } from "./arc/tabs/tabs";
import { SettingsGroup } from "./SettingsGroup";
import { EmptyState } from "./arc/empty-state/empty-state";
import { Skeleton } from "./arc/skeleton/skeleton";
import { SortableDataTable } from "./arc/sortable-data-table/sortable-data-table";
import "./PersonalSettings.css";
import "./AlertCenter.css";
type Props = {} & { children?: import("react").ReactNode; [key: string]: any };
export default function AlertCenter(input: Props) {
  const ruleDialogContainer = useRef<HTMLElement>(null);
  const ruleEditTrigger = useRef<HTMLButtonElement>(null);
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      type Tab =
        | "active"
        | "history"
        | "rules"
        | "channels"
        | "windows"
        | "deliveries";
      const tab = useViewTab<Tab>("tab", "active", () => [
        "active",
        "history",
        "rules",
        "channels",
        "windows",
        "deliveries",
      ]);
      const loading = cell(false);
      const saving = cell(false);
      const error = cell("");
      const notice = cell("");
      const nodes = cell<AgentNode[]>([]);
      const active = cell<AlertItem[]>([]);
      const history = cell<AlertItem[]>([]);
      const rules = cell<AlertRule[]>([]);
      const channels = cell<AlertChannel[]>([]);
      const channelProviders = cell<ChannelProvider[]>([]);
      const selectedProvider = derived(() =>
        channelProviders.value.find((p) => p.id === channelForm.provider),
      );
      const windows = cell<AlertWindow[]>([]);
      const deliveries = cell<AlertDelivery[]>([]);
      const events = cell<AlertEvent[]>([]);
      const loadedTabs = cell<Partial<Record<Tab, boolean>>>({});
      const editingChannel = cell("");
      const editingRule = cell<AlertRule>();
      const ruleNodeFilter = cell("");
      const visibleRules = derived(() =>
        rules.value.filter(
          (rule) =>
            !ruleNodeFilter.value || rule.nodeId === ruleNodeFilter.value,
        ),
      );
      const ruleForm = observable({
        name: "",
        kind: "cpu" as AlertRule["kind"],
        nodeId: "",
        subjectId: "",
        severity: "warning" as AlertRule["severity"],
        threshold: 90 as number | string,
        durationSeconds: 300 as number | string,
        cooldownSeconds: 300 as number | string,
        expectedState: "running",
        enabled: true,
        channelIds: [] as string[],
      });
      const channelForm = observable({
        name: "",
        kind: "webhook" as AlertChannel["kind"],
        provider: "",
        chatId: "",
        method: "POST",
        bodyTemplate: "",
        headers: "",
        signingSecret: "",
        clearHeaders: false,
        clearSigning: false,
        webhookUrl: "",
        messageTemplate: "",
        smtpHost: "",
        smtpPort: 587 as number | string,
        smtpSecurityMode: "starttls" as
          | NonNullable<AlertChannel["config"]["smtpSecurityMode"]>
          | "",
        smtpFrom: "",
        smtpTo: "",
        smtpUsername: "",
        secret: "",
        enabled: true,
        revision: 0,
      });
      const windowForm = observable({
        kind: "maintenance" as AlertWindow["kind"],
        scopeType: "global" as AlertWindow["scopeType"],
        scopeId: "",
        startsAt: "",
        endsAt: "",
        reason: "",
      });
      const kindOptions = derived<
        Array<{
          value: AlertRule["kind"];
          label: string;
        }>
      >(() => [
        {
          value: "node_offline",
          label: t("m.aaf5e5fef170"),
        },
        {
          value: "cpu",
          label: t("m.1c82e5547854"),
        },
        {
          value: "memory",
          label: t("m.ace212ad8fd3"),
        },
        {
          value: "disk",
          label: t("m.eef857d05e0c"),
        },
        {
          value: "docker_unavailable",
          label: t("m.d3e431f9d10b"),
        },
        {
          value: "metric",
          label: t("host.metricAlert"),
        },
        {
          value: "probe_latency",
          label: t("host.latencyAlert"),
        },
        {
          value: "probe_packet_loss",
          label: t("host.lossAlert"),
        },
        {
          value: "probe_failure",
          label: t("host.failureAlert"),
        },
        {
          value: "asset_expiry",
          label: t("host.expiryAlert"),
        },
        {
          value: "traffic_quota",
          label: t("host.quotaAlert"),
        },
        {
          value: "container_state",
          label: t("m.15e1d002ad08"),
        },
        {
          value: "probe_state",
          label: t("m.c4134bc129e8"),
        },
      ]);
      const needsThreshold = derived(() =>
        [
          "cpu",
          "memory",
          "disk",
          "metric",
          "probe_latency",
          "probe_failure",
          "probe_packet_loss",
        ].includes(ruleForm.kind),
      );
      const needsSubject = derived(() =>
        [
          "container_state",
          "probe_state",
          "metric",
          "probe_latency",
          "probe_failure",
          "probe_packet_loss",
        ].includes(ruleForm.kind),
      );
      const needsState = derived(() =>
        ["container_state", "probe_state"].includes(ruleForm.kind),
      );
      const metricCatalog = cell<Record<string, string>>({});
      function message(reason: unknown) {
        return reason instanceof Error ? reason.message : t("m.a472d51792cc");
      }
      function clearMessages() {
        error.value = "";
        notice.value = "";
      }
      let disposed = false;
      let refreshRequest: AbortController | undefined;
      async function refresh() {
        if (disposed) return;
        refreshRequest?.abort();
        const currentTab = tab.value;
        const controller = new AbortController();
        refreshRequest = controller;
        loading.value = true;
        error.value = "";
        async function load<T>(name: string, apply: (value: T) => void) {
          try {
            const result = await operationRequest<T>(
              name,
              {},
              { signal: controller.signal },
            );
            if (!disposed && !controller.signal.aborted) apply(result);
          } catch (reason) {
            if (!disposed && !controller.signal.aborted)
              error.value = message(reason);
          }
        }
        const reads = [
          load<{ alerts: AlertItem[] }>("alerts.list", (result) => {
            active.value = result.alerts;
          }),
        ];
        if (tab.value === "history") {
          reads.push(
            load<{ alerts: AlertItem[] }>("alerts.history", (result) => {
              history.value = result.alerts;
            }),
          );
          reads.push(
            load<{ events: AlertEvent[] }>("alerts.events", (result) => {
              events.value = result.events;
            }),
          );
        } else if (tab.value === "rules") {
          reads.push(
            load<{ nodes: AgentNode[] }>("nodes.list", (result) => {
              nodes.value = result.nodes;
              if (!ruleForm.nodeId)
                ruleForm.nodeId = result.nodes[0]?.nodeId ?? "";
            }),
          );
          reads.push(
            load<{ rules: AlertRule[] }>("rules.list", (result) => {
              rules.value = result.rules;
            }),
          );
          reads.push(
            load<{ channels: AlertChannel[] }>("channels.list", (result) => {
              channels.value = result.channels;
            }),
          );
          reads.push(
            load<{ metrics: Record<string, string> }>(
              "metrics.catalog",
              (result) => {
                metricCatalog.value = result.metrics;
              },
            ),
          );
        } else if (tab.value === "channels") {
          reads.push(
            load<{ providers: ChannelProvider[] }>(
              "channels.options",
              (result) => {
                channelProviders.value = result.providers;
              },
            ),
          );
          reads.push(
            load<{ channels: AlertChannel[] }>("channels.list", (result) => {
              channels.value = result.channels;
            }),
          );
        } else if (tab.value === "windows") {
          reads.push(
            load<{ windows: AlertWindow[] }>("windows.list", (result) => {
              windows.value = result.windows;
            }),
          );
        } else if (tab.value === "deliveries") {
          reads.push(
            load<{ deliveries: AlertDelivery[] }>(
              "deliveries.list",
              (result) => {
                deliveries.value = result.deliveries;
              },
            ),
          );
        }
        await Promise.all(reads);
        if (!disposed && refreshRequest === controller) {
          loadedTabs.value = { ...loadedTabs.value, [currentTab]: true };
          loading.value = false;
        }
      }
      async function acknowledge(item: AlertItem) {
        clearMessages();
        try {
          await api.acknowledgeAlert(item.id);
          notice.value = t("m.cea97c0a7048");
          await refresh();
        } catch (reason) {
          error.value = message(reason);
        }
      }
      async function silence(item: AlertItem) {
        clearMessages();
        try {
          await api.silenceAlert(
            item.id,
            new Date(Date.now() + 60 * 60_000).toISOString(),
            t("m.8c345ca1ae04"),
          );
          notice.value = t("m.2abe886a8b9e");
          await refresh();
        } catch (reason) {
          error.value = message(reason);
        }
      }
      function editRule(rule: AlertRule) {
        editingRule.value = { ...rule, channelIds: [...rule.channelIds] };
        Object.assign(ruleForm, {
          name: rule.name,
          kind: rule.kind,
          nodeId: rule.nodeId,
          subjectId: rule.subjectId ?? "",
          severity: rule.severity,
          threshold: rule.threshold ?? 90,
          durationSeconds: rule.durationSeconds,
          cooldownSeconds: rule.cooldownSeconds,
          expectedState: rule.expectedState ?? "running",
          channelIds: [...rule.channelIds],
          enabled: rule.enabled,
        });
        clearMessages();
      }
      function clearRuleForm() {
        if (editingRule.value) error.value = "";
        editingRule.value = undefined;
        Object.assign(ruleForm, {
          name: "",
          kind: "cpu",
          nodeId:
            ruleNodeFilter.value ||
            ruleForm.nodeId ||
            nodes.value[0]?.nodeId ||
            "",
          subjectId: "",
          severity: "warning",
          threshold: 90,
          durationSeconds: 300,
          cooldownSeconds: 300,
          expectedState: "running",
          channelIds: [],
          enabled: true,
        });
      }
      async function saveRule() {
        clearMessages();
        saving.value = true;
        try {
          const body = {
            name: ruleForm.name,
            kind: ruleForm.kind,
            nodeId: ruleForm.nodeId,
            subjectId: needsSubject.value ? ruleForm.subjectId : "",
            severity: ruleForm.severity,
            ...(needsThreshold.value
              ? {
                  threshold: Number(ruleForm.threshold),
                }
              : {}),
            durationSeconds: Number(ruleForm.durationSeconds),
            cooldownSeconds: Number(ruleForm.cooldownSeconds),
            expectedState: needsState.value ? ruleForm.expectedState : "",
            channelIds: ruleForm.channelIds,
            enabled: ruleForm.enabled,
          };
          const editing = editingRule.value;
          if (editing) {
            await api.updateAlertRule(editing.id, {
              ...body,
              revision: editing.revision,
            });
            clearRuleForm();
          } else {
            await api.createAlertRule(body);
            ruleForm.name = "";
            ruleForm.subjectId = "";
          }
          notice.value = t("m.43fed933b297");
          await refresh();
        } catch (reason) {
          error.value = message(reason);
        } finally {
          saving.value = false;
        }
      }
      function editChannel(channel: AlertChannel) {
        editingChannel.value = channel.id;
        channelForm.name = channel.name;
        channelForm.kind = channel.kind;
        channelForm.enabled = channel.enabled;
        channelForm.revision = channel.revision;
        channelForm.secret = "";
        channelForm.provider = channel.config.provider ?? "";
        channelForm.chatId = channel.config.chatId ?? "";
        channelForm.method = channel.config.method ?? "POST";
        channelForm.bodyTemplate = channel.config.bodyTemplate ?? "";
        channelForm.headers = "";
        channelForm.signingSecret = "";
        channelForm.clearHeaders = false;
        channelForm.clearSigning = false;
        channelForm.webhookUrl = channel.config.webhookUrl ?? "";
        channelForm.messageTemplate = channel.config.messageTemplate ?? "";
        channelForm.smtpHost = channel.config.smtpHost ?? "";
        channelForm.smtpPort = channel.config.smtpPort ?? 587;
        channelForm.smtpSecurityMode = channel.config.smtpSecurityMode ?? "";
        channelForm.smtpFrom = channel.config.smtpFrom ?? "";
        channelForm.smtpTo = channel.config.smtpTo ?? "";
        channelForm.smtpUsername = channel.config.smtpUsername ?? "";
      }
      function clearChannelForm() {
        editingChannel.value = "";
        channelForm.name = "";
        channelForm.kind = "webhook";
        channelForm.provider = "";
        channelForm.chatId = "";
        channelForm.method = "POST";
        channelForm.bodyTemplate = "";
        channelForm.headers = "";
        channelForm.signingSecret = "";
        channelForm.clearHeaders = false;
        channelForm.clearSigning = false;
        channelForm.webhookUrl = "";
        channelForm.messageTemplate = "";
        channelForm.smtpHost = "";
        channelForm.smtpPort = 587;
        channelForm.smtpSecurityMode = "starttls";
        channelForm.smtpFrom = "";
        channelForm.smtpTo = "";
        channelForm.smtpUsername = "";
        channelForm.secret = "";
        channelForm.enabled = true;
        channelForm.revision = 0;
      }
      async function saveChannel() {
        clearMessages();
        saving.value = true;
        try {
          const messageTemplate = channelForm.messageTemplate || undefined;
          const config =
            channelForm.kind === "webhook"
              ? {
                  webhookUrl: selectedProvider.value?.url
                    ? channelForm.webhookUrl
                    : undefined,
                  provider: channelForm.provider || undefined,
                  chatId: selectedProvider.value?.chatId
                    ? channelForm.chatId
                    : undefined,
                  method: selectedProvider.value?.customRequest
                    ? channelForm.method
                    : undefined,
                  bodyTemplate: selectedProvider.value?.customRequest
                    ? channelForm.bodyTemplate
                    : undefined,
                  ...(messageTemplate
                    ? {
                        messageTemplate,
                      }
                    : {}),
                }
              : {
                  smtpHost: channelForm.smtpHost,
                  smtpPort: Number(channelForm.smtpPort),
                  smtpSecurityMode: channelForm.smtpSecurityMode || undefined,
                  smtpFrom: channelForm.smtpFrom,
                  smtpTo: channelForm.smtpTo,
                  smtpUsername: channelForm.smtpUsername,
                  ...(messageTemplate
                    ? {
                        messageTemplate,
                      }
                    : {}),
                };
          await api.saveAlertChannel(
            {
              name: channelForm.name,
              kind: channelForm.kind,
              config,
              secret: channelForm.secret || undefined,
              headers:
                channelForm.kind === "webhook" &&
                selectedProvider.value?.customRequest
                  ? channelForm.clearHeaders
                    ? {}
                    : channelForm.headers.trim()
                      ? JSON.parse(channelForm.headers)
                      : undefined
                  : undefined,
              signingSecret:
                channelForm.kind === "webhook" &&
                selectedProvider.value?.signing
                  ? channelForm.clearSigning
                    ? ""
                    : channelForm.signingSecret || undefined
                  : undefined,
              enabled: channelForm.enabled,
              ...(editingChannel.value
                ? {
                    revision: channelForm.revision,
                  }
                : {}),
            },
            editingChannel.value || undefined,
          );
          notice.value = t("m.628832ec4bdb");
          clearChannelForm();
          await refresh();
        } catch (reason) {
          error.value = message(reason);
        } finally {
          saving.value = false;
        }
      }
      async function deleteChannel(channel: AlertChannel) {
        clearMessages();
        try {
          await api.deleteAlertChannel(channel.id, channel.revision);
          notice.value = t("m.ac4a5dec2790");
          await refresh();
        } catch (reason) {
          error.value = message(reason);
        }
      }
      async function testChannel(channel: AlertChannel) {
        clearMessages();
        try {
          await api.testAlertChannel(channel.id);
          notice.value = t("m.852b75a2577e", {
            "0": channel.name,
          });
          tab.value = "deliveries";
        } catch (reason) {
          error.value = message(reason);
        }
      }
      async function saveWindow() {
        clearMessages();
        saving.value = true;
        try {
          const startsAt = windowForm.startsAt
            ? new Date(windowForm.startsAt).toISOString()
            : new Date().toISOString();
          const endsAt = windowForm.endsAt
            ? new Date(windowForm.endsAt).toISOString()
            : new Date(Date.now() + 60 * 60_000).toISOString();
          await api.createAlertWindow({
            kind: windowForm.kind,
            scopeType: windowForm.scopeType,
            ...(windowForm.scopeType === "global"
              ? {}
              : {
                  scopeId: windowForm.scopeId,
                }),
            startsAt,
            endsAt,
            reason: windowForm.reason,
          });
          notice.value = t("m.1f561856659c");
          windowForm.reason = "";
          await refresh();
        } catch (reason) {
          error.value = message(reason);
        } finally {
          saving.value = false;
        }
      }
      async function deleteWindow(window: AlertWindow) {
        try {
          await api.deleteAlertWindow(window.id);
          notice.value = t("m.7e51b232bf64");
          await refresh();
        } catch (reason) {
          error.value = message(reason);
        }
      }
      async function deleteRule(rule: AlertRule) {
        try {
          await api.deleteAlertRule(rule.id, rule.revision);
          if (editingRule.value?.id === rule.id) clearRuleForm();
          notice.value = t("m.daa8c0f91450");
          await refresh();
        } catch (reason) {
          error.value = message(reason);
        }
      }
      function when(value?: string) {
        if (!value) return "—";
        const date = new Date(value);
        return Number.isNaN(date.getTime())
          ? value
          : date.toLocaleString(locale.value);
      }
      function deliveryLabel(status: string) {
        const labels: Record<string, string> = {
          retry: t("delivery.retry"),
          suppressed: t("delivery.suppressed"),
          pending: t("delivery.pending"),
          sending: t("delivery.sending"),
          sent: t("delivery.sent"),
          delivered: t("delivery.delivered"),
          retrying: t("delivery.retrying"),
          failed: t("delivery.failed"),
          queued: t("delivery.queued"),
        };
        return labels[status] ?? status;
      }
      function ruleLabel(kind: string) {
        return (
          kindOptions.value.find((item) => item.value === kind)?.label ?? kind
        );
      }
      function activeList() {
        return tab.value === "active" ? active.value : history.value;
      }
      let refreshTimer: number | undefined;
      watchState(
        tab,
        () => {
          void refresh();
        },
        { immediate: true },
      );
      afterMount(() => {
        refreshTimer = window.setInterval(() => {
          if (!loading.value) void refresh();
        }, 30_000);
      });
      beforeDispose(() => {
        disposed = true;
        refreshRequest?.abort();
        if (refreshTimer !== undefined) window.clearInterval(refreshTimer);
      });
      const row = (
        name: string,
        label: string,
        control: ReactElement<{ "aria-label"?: string }>,
        description?: string,
      ) => (
        <div key={name} className="parameter-row alert-field">
          <label htmlFor={`alert-${name}`}>{label}</label>
          <div className="alert-control">
            {cloneElement(control, { "aria-label": label })}
            {description ? <small>{description}</small> : null}
          </div>
        </div>
      );
      const heading = (label: string) => (
        <div className="workspace-section-heading">
          <h2>{label}</h2>
        </div>
      );
      const empty = (label: string) => (
        <SettingsGroup>
          {loading.value && !loadedTabs.value[tab.value] ? (
            <Skeleton label={t("load.initial")} lines={2} />
          ) : (
            <EmptyState title={label} description="" />
          )}
        </SettingsGroup>
      );
      const renderRuleForm = () => (
        <form
          className="workspace-section alert-form-card alert-rule-form"
          onSubmit={(event) => {
            event.preventDefault();
            void saveRule();
          }}
        >
          {!editingRule.value ? heading(t("m.83009fcfe6c1")) : null}
          {editingRule.value && error.value ? (
            <Alert role="alert" tone="danger" title={t("management.error")}>
              {display(error.value)}
            </Alert>
          ) : null}
          <SettingsGroup>
            {row(
              "rule-name",
              t("m.18dba51b47c8"),
              <Input
                id="alert-rule-name"
                required
                placeholder={t("m.fb8ad27501de")}
                value={ruleForm.name}
                onChange={(event) => {
                  ruleForm.name = event.target.value;
                }}
              />,
            )}
            {row(
              "rule-node",
              t("m.2410d860f093"),
              <Select
                id="alert-rule-node"
                required
                value={ruleForm.nodeId}
                onValueChange={(value) => {
                  ruleForm.nodeId = value;
                }}
                options={nodes.value.map((node) => ({
                  value: node.nodeId,
                  label: `${node.displayName} · ${node.status}`,
                }))}
              />,
            )}
            {row(
              "rule-kind",
              t("m.16bb1d2b098c"),
              <Select
                id="alert-rule-kind"
                value={ruleForm.kind}
                onValueChange={(value) => {
                  ruleForm.kind = value as AlertRule["kind"];
                }}
                options={kindOptions.value}
              />,
            )}
            {needsThreshold.value
              ? row(
                  "rule-threshold",
                  t("m.26a4ac708409"),
                  <Input
                    id="alert-rule-threshold"
                    type="number"
                    min={0}
                    max={
                      [
                        "cpu",
                        "memory",
                        "disk",
                        "probe_failure",
                        "probe_packet_loss",
                      ].includes(ruleForm.kind)
                        ? 100
                        : undefined
                    }
                    step="any"
                    required
                    value={ruleForm.threshold}
                    onChange={(event) => {
                      ruleForm.threshold =
                        event.target.value === ""
                          ? ""
                          : Number(event.target.value);
                    }}
                  />,
                )
              : null}
            {needsSubject.value
              ? row(
                  "rule-subject",
                  t("m.ad293c799482"),
                  ruleForm.kind === "metric" ? (
                    <Select
                      id="alert-rule-subject"
                      required
                      value={ruleForm.subjectId}
                      onValueChange={(value) => {
                        ruleForm.subjectId = value;
                      }}
                      options={Object.entries(metricCatalog.value).map(
                        ([value, unit]) => ({
                          value,
                          label: `${value} · ${unit}`,
                        }),
                      )}
                    />
                  ) : (
                    <Input
                      id="alert-rule-subject"
                      required
                      placeholder={t("m.8a90cd8dc652")}
                      value={ruleForm.subjectId}
                      onChange={(event) => {
                        ruleForm.subjectId = event.target.value;
                      }}
                    />
                  ),
                )
              : null}
            {needsState.value
              ? row(
                  "rule-state",
                  t("m.b3d94c7eff6f"),
                  <Select
                    id="alert-rule-state"
                    value={ruleForm.expectedState}
                    onValueChange={(value) => {
                      ruleForm.expectedState = value;
                    }}
                    options={[
                      {
                        value: "running",
                        label: t("m.1f0eb99b7ed0"),
                      },
                      {
                        value: "stopped",
                        label: t("m.f006455e3baf"),
                      },
                      {
                        value: "healthy",
                        label: t("m.aa18b6dcd893"),
                      },
                      {
                        value: "unhealthy",
                        label: t("m.395a7bf80b24"),
                      },
                    ]}
                  />,
                )
              : null}
            {row(
              "rule-duration",
              t("m.f67bd602eaa1"),
              <Input
                id="alert-rule-duration"
                type="number"
                min={0}
                max={86400}
                value={ruleForm.durationSeconds}
                onChange={(event) => {
                  ruleForm.durationSeconds = Number(event.target.value);
                }}
              />,
            )}
            {row(
              "rule-cooldown",
              t("m.c90ed7f53ce4"),
              <Input
                id="alert-rule-cooldown"
                type="number"
                min={0}
                max={86400}
                value={ruleForm.cooldownSeconds}
                onChange={(event) => {
                  ruleForm.cooldownSeconds = Number(event.target.value);
                }}
              />,
            )}
            {row(
              "rule-severity",
              t("m.3d9d02e83d39"),
              <Select
                id="alert-rule-severity"
                value={ruleForm.severity}
                onValueChange={(value) => {
                  ruleForm.severity = value as AlertRule["severity"];
                }}
                options={[
                  { value: "info", label: t("m.f56c6c82203b") },
                  { value: "warning", label: t("m.a8b7a4480407") },
                  { value: "critical", label: t("m.73eb0e14e307") },
                ]}
              />,
            )}
            <div className="parameter-row">
              <span>{t("m.1bcd7265cd39")}</span>
              <div className="alert-choices">
                {channels.value.length ? (
                  channels.value.map((channel) => (
                    <Checkbox
                      key={channel.id}
                      label={channel.name}
                      checked={ruleForm.channelIds.includes(channel.id)}
                      onCheckedChange={(checked) => {
                        ruleForm.channelIds =
                          checked === true
                            ? [...ruleForm.channelIds, channel.id]
                            : ruleForm.channelIds.filter(
                                (id) => id !== channel.id,
                              );
                      }}
                    />
                  ))
                ) : (
                  <small>{t("m.1c5c0b7d73e4")}</small>
                )}
              </div>
            </div>
            {row(
              "rule-enabled",
              t("m.e45a3f15b735"),
              <Checkbox
                id="alert-rule-enabled"
                checked={ruleForm.enabled}
                onCheckedChange={(value) => {
                  ruleForm.enabled = value === true;
                }}
              />,
            )}
          </SettingsGroup>
          <div className="settings-section-actions">
            {editingRule.value ? (
              <Button
                type="button"
                variant="secondary"
                disabled={saving.value}
                onClick={clearRuleForm}
              >
                {t("m.4b29ce8e4093")}
              </Button>
            ) : null}
            <Button
              type="submit"
              disabled={saving.value || !nodes.value.length}
            >
              {saving.value
                ? t("m.ff509c9ba052")
                : editingRule.value
                  ? t("alerts.saveRule")
                  : t("m.63faeddb2a46")}
            </Button>
          </div>
        </form>
      );
      return () => (
        <ManagementPage
          registryStyles
          className="alerts-page preferences-page workspace-settings"
          aria-busy={loading.value}
        >
          <header className="workspace-heading">
            <h1>{t("m.1b92ed81c005")}</h1>
            <Button
              variant="secondary"
              disabled={loading.value}
              onClick={() => void refresh()}
            >
              {loading.value ? t("m.7f3c8312816f") : t("m.0468ee76ee3d")}
            </Button>
          </header>
          {error.value && !editingRule.value ? (
            <Alert role="alert" tone="danger" title={t("management.error")}>
              {display(error.value)}
            </Alert>
          ) : null}
          {notice.value ? (
            <Alert role="status" tone="success" title={t("management.notice")}>
              {display(notice.value)}
            </Alert>
          ) : null}
          <Tabs
            className="alert-view-tabs"
            value={tab.value}
            onValueChange={(value) => {
              tab.value = value as Tab;
            }}
          >
            <TabsList
              className="alert-tabs"
              aria-label={t("m.6c08cfefefbc")}
              onPointerDownCapture={(event) => {
                event.currentTarget.dataset.focusSource = "pointer";
              }}
              onKeyDownCapture={(event) => {
                delete event.currentTarget.dataset.focusSource;
              }}
              onBlurCapture={(event) => {
                if (!event.currentTarget.contains(event.relatedTarget))
                  delete event.currentTarget.dataset.focusSource;
              }}
            >
              {(
                [
                  ["active", t("m.ee404f35c3ce")],
                  ["history", t("m.7fe7f1f0fb28")],
                  ["rules", t("m.7488ac33d3de")],
                  ["channels", t("m.b2354cebb5bc")],
                  ["windows", t("m.cb20b84d602a")],
                  ["deliveries", t("m.f0c5fa02c974")],
                ] as const
              ).map(([value, label]) => (
                <TabsTrigger key={value} value={value}>
                  {label}
                  {value === "active" && active.value.length
                    ? ` (${active.value.length})`
                    : ""}
                </TabsTrigger>
              ))}
            </TabsList>
            <TabsContent
              value={tab.value}
              className="settings-sections alert-sections"
            >
              {tab.value === "active" || tab.value === "history" ? (
                <>
                  <section className="workspace-section alert-list">
                    {heading(
                      tab.value === "active"
                        ? t("m.ee404f35c3ce")
                        : t("m.7fe7f1f0fb28"),
                    )}
                    {activeList().length ? (
                      <SettingsGroup>
                        {activeList().map((item) => (
                          <article
                            key={item.id}
                            className="alert-record"
                            data-severity={item.severity}
                          >
                            <div className="alert-record-title">
                              <strong>{item.ruleName}</strong>
                              <Badge
                                tone={
                                  item.status === "active"
                                    ? item.severity === "critical"
                                      ? "danger"
                                      : "warning"
                                    : "neutral"
                                }
                              >
                                {item.status === "active"
                                  ? t("m.3c01089ea0e4")
                                  : t("m.3617f737f437")}
                              </Badge>
                              {item.acknowledgedAt ? (
                                <Badge>{t("m.d56a511f498f")}</Badge>
                              ) : null}
                              {item.suppressionReason ? (
                                <Badge>
                                  {item.suppressionReason === "node_offline"
                                    ? t("m.c46a19c8d6ca")
                                    : t("m.5329772f4d2b")}
                                </Badge>
                              ) : null}
                            </div>
                            <p>{display(item.message)}</p>
                            <small>
                              {item.nodeName} · {item.nodeId}
                              {t("m.a2f8212e41a4")}
                              {when(item.firstSeenAt)}
                              {item.resolvedAt
                                ? `${t("m.daf142e39346")}${when(item.resolvedAt)}`
                                : ""}
                            </small>
                            {item.status === "active" ? (
                              <div className="settings-section-actions">
                                <Button
                                  variant="secondary"
                                  disabled={!!item.acknowledgedAt}
                                  onClick={() => void acknowledge(item)}
                                >
                                  {t("m.36f33adaf094")}
                                </Button>
                                <Button
                                  variant="secondary"
                                  onClick={() => void silence(item)}
                                >
                                  {t("m.d69600baef4b")}
                                </Button>
                              </div>
                            ) : null}
                          </article>
                        ))}
                      </SettingsGroup>
                    ) : (
                      empty(
                        tab.value === "active"
                          ? t("m.b2f037729dad")
                          : t("m.8eac1ef8ee32"),
                      )
                    )}
                  </section>
                  {tab.value === "history" ? (
                    <section className="workspace-section event-list">
                      {heading(t("m.512c5669c853"))}
                      {events.value.length ? (
                        <SettingsGroup>
                          {events.value.map((event) => (
                            <article key={event.id} className="alert-record">
                              <strong>{event.kind}</strong>
                              <p>{display(event.message)}</p>
                              <time>{when(event.occurredAt)}</time>
                            </article>
                          ))}
                        </SettingsGroup>
                      ) : (
                        empty(t("m.d3ae2b8f4adf"))
                      )}
                    </section>
                  ) : null}
                </>
              ) : tab.value === "rules" ? (
                <>
                  <section
                    ref={ruleDialogContainer}
                    className="workspace-section alert-rule-list"
                  >
                    {heading(t("m.52ad1003d627"))}
                    <p className="alert-defaults-hint">
                      {t("alerts.defaultThresholds")}
                    </p>
                    <SettingsGroup>
                      {row(
                        "rule-filter",
                        t("m.2410d860f093"),
                        <Select
                          id="alert-rule-filter"
                          value={ruleNodeFilter.value}
                          onValueChange={(value) => {
                            ruleNodeFilter.value = value;
                          }}
                          options={[
                            { value: "", label: t("alerts.allNodes") },
                            ...nodes.value.map((node) => ({
                              value: node.nodeId,
                              label: node.displayName,
                            })),
                          ]}
                        />,
                      )}
                    </SettingsGroup>
                    {visibleRules.value.length ? (
                      <SortableDataTable
                        rows={visibleRules.value.map((rule) => ({ ...rule }))}
                        rowKey="id"
                        caption={t("m.52ad1003d627")}
                        columns={[
                          {
                            key: "name",
                            label: t("m.1123245d1bed"),
                            sortable: true,
                          },
                          {
                            key: "nodeId",
                            label: t("m.2410d860f093"),
                            render: (_, rule) =>
                              nodes.value.find(
                                (node) => node.nodeId === rule.nodeId,
                              )?.displayName ?? rule.nodeId,
                          },
                          {
                            key: "kind",
                            label: t("m.16bb1d2b098c"),
                            render: (_, rule) =>
                              `${ruleLabel(rule.kind)}${rule.threshold !== undefined ? ` > ${rule.threshold}${["cpu", "memory", "disk", "probe_failure", "probe_packet_loss"].includes(rule.kind) ? "%" : ""}` : ""}${rule.expectedState ? ` ≠ ${rule.expectedState}` : ""}`,
                          },
                          {
                            key: "durationSeconds",
                            label: t("m.1882ae19b6a2"),
                            render: (_, rule) =>
                              `${rule.durationSeconds} ${t("m.9dcdc2b289b9")}`,
                          },
                          {
                            key: "enabled",
                            label: t("m.6320b4a8722a"),
                            render: (_, rule) =>
                              rule.enabled
                                ? t("m.dfb802238b38")
                                : t("m.a8c3698b5b8c"),
                          },
                          {
                            key: "actions",
                            label: t("domains.actions"),
                            width: 176,
                            render: (_, rule) => (
                              <div className="alert-rule-actions">
                                <Button
                                  size="sm"
                                  variant="secondary"
                                  disabled={saving.value}
                                  onClick={(event) => {
                                    ruleEditTrigger.current =
                                      event.currentTarget;
                                    editRule(rule);
                                  }}
                                >
                                  {t("m.051836569928")}
                                </Button>
                                <Button
                                  size="sm"
                                  variant="danger"
                                  disabled={saving.value}
                                  onClick={() => void deleteRule(rule)}
                                >
                                  {t("m.2f9daa828907")}
                                </Button>
                              </div>
                            ),
                          },
                        ]}
                      />
                    ) : (
                      empty(t("m.915b0451461e"))
                    )}
                  </section>
                  {!editingRule.value ? renderRuleForm() : null}
                  <Dialog
                    open={!!editingRule.value}
                    onOpenChange={(open) => {
                      if (!open && !saving.value) clearRuleForm();
                    }}
                  >
                    <DialogContent
                      container={ruleDialogContainer.current?.closest<HTMLElement>(
                        ".alerts-page",
                      )}
                      className="alert-rule-dialog"
                      title={t("alerts.editRule")}
                      onEscapeKeyDown={(event) => {
                        if (saving.value) event.preventDefault();
                      }}
                      onPointerDownOutside={(event) => {
                        if (saving.value) event.preventDefault();
                      }}
                      onCloseAutoFocus={(event) => {
                        event.preventDefault();
                        if (ruleEditTrigger.current?.isConnected)
                          ruleEditTrigger.current.focus({
                            preventScroll: true,
                          });
                      }}
                    >
                      {editingRule.value ? renderRuleForm() : null}
                    </DialogContent>
                  </Dialog>
                </>
              ) : tab.value === "channels" ? (
                <>
                  <section className="workspace-section channel-list">
                    {heading(t("m.b2354cebb5bc"))}
                    {channels.value.length ? (
                      <SettingsGroup>
                        {channels.value.map((channel) => (
                          <article key={channel.id} className="alert-record">
                            <div className="alert-record-title">
                              <strong>{channel.name}</strong>
                              <Badge>
                                {channel.config.provider
                                  ? (channelProviders.value.find(
                                      (p) => p.id === channel.config.provider,
                                    )?.name ?? channel.config.provider)
                                  : channel.kind.toUpperCase()}
                              </Badge>
                              <span>
                                {channel.enabled
                                  ? t("m.dfb802238b38")
                                  : t("m.a8c3698b5b8c")}
                              </span>
                            </div>
                            <small>
                              {channel.kind === "webhook"
                                ? channel.config.chatId ||
                                  channel.config.webhookUrl
                                : `${channel.config.smtpHost}:${channel.config.smtpPort} → ${channel.config.smtpTo}`}
                            </small>
                            <div className="settings-section-actions">
                              <Button
                                variant="secondary"
                                onClick={() => editChannel(channel)}
                              >
                                {t("m.051836569928")}
                              </Button>
                              <Button
                                variant="secondary"
                                onClick={() => void testChannel(channel)}
                              >
                                {t("m.e4263450f9ec")}
                              </Button>
                              <Button
                                variant="danger"
                                onClick={() => void deleteChannel(channel)}
                              >
                                {t("m.2f9daa828907")}
                              </Button>
                            </div>
                          </article>
                        ))}
                      </SettingsGroup>
                    ) : (
                      empty(t("m.1c5c0b7d73e4"))
                    )}
                  </section>
                  <form
                    className="workspace-section alert-form-card alert-channel-form"
                    onSubmit={(event) => {
                      event.preventDefault();
                      void saveChannel();
                    }}
                  >
                    {heading(
                      editingChannel.value
                        ? t("m.9536f6b3a7da")
                        : t("m.edb55e6632ce"),
                    )}
                    <SettingsGroup>
                      {row(
                        "channel-name",
                        t("m.d44e9b3d3b31"),
                        <Input
                          id="alert-channel-name"
                          required
                          placeholder={t("m.ab0113166ed7")}
                          value={channelForm.name}
                          onChange={(event) => {
                            channelForm.name = event.target.value;
                          }}
                        />,
                      )}
                      {row(
                        "channel-kind",
                        t("m.ba40014ff496"),
                        <Select
                          id="alert-channel-kind"
                          value={channelForm.kind}
                          onValueChange={(value) => {
                            channelForm.kind = value as AlertChannel["kind"];
                            channelForm.secret = "";
                          }}
                          options={[
                            { value: "webhook", label: "Webhook" },
                            { value: "smtp", label: t("m.06344b933760") },
                          ]}
                        />,
                      )}
                      {channelForm.kind === "webhook" ? (
                        <>
                          {row(
                            "channel-provider",
                            t("channels.provider"),
                            <Select
                              id="alert-channel-provider"
                              value={channelForm.provider}
                              onValueChange={(value) => {
                                channelForm.provider = value;
                                channelForm.secret = "";
                                channelForm.webhookUrl = "";
                                channelForm.chatId = "";
                                channelForm.headers = "";
                                channelForm.signingSecret = "";
                                channelForm.clearHeaders = false;
                                channelForm.clearSigning = false;
                              }}
                              options={channelProviders.value.map((p) => ({
                                value: p.id,
                                label:
                                  t(`channels.provider.${p.id}`) ===
                                  `channels.provider.${p.id}`
                                    ? p.name
                                    : t(`channels.provider.${p.id}`),
                              }))}
                            />,
                          )}
                          {selectedProvider.value?.url &&
                            row(
                              "channel-url",
                              t("label.84805a7574a8"),
                              <Input
                                id="alert-channel-url"
                                type="url"
                                required={selectedProvider.value.customRequest}
                                placeholder={
                                  selectedProvider.value.customRequest
                                    ? "https://hooks.example/…"
                                    : "https://api.day.app"
                                }
                                value={channelForm.webhookUrl}
                                onChange={(event) => {
                                  channelForm.webhookUrl = event.target.value;
                                }}
                              />,
                            )}
                          {selectedProvider.value?.chatId &&
                            row(
                              "channel-chat",
                              "Chat ID",
                              <Input
                                id="alert-channel-chat"
                                required
                                value={channelForm.chatId}
                                onChange={(event) => {
                                  channelForm.chatId = event.target.value;
                                }}
                              />,
                            )}
                          {row(
                            "channel-secret",
                            selectedProvider.value
                              ? t(
                                  `channels.secret.${selectedProvider.value.secretLabel}`,
                                )
                              : t("m.82170ccaa6b5"),
                            <Input
                              id="alert-channel-secret"
                              type="password"
                              autoComplete="new-password"
                              placeholder={
                                editingChannel.value ? t("m.0c5bcbe51a9f") : ""
                              }
                              value={channelForm.secret}
                              onChange={(event) => {
                                channelForm.secret = event.target.value;
                              }}
                            />,
                          )}
                          {selectedProvider.value?.signing && (
                            <>
                              {row(
                                "channel-signing",
                                t("channels.signingSecret"),
                                <Input
                                  id="alert-channel-signing"
                                  type="password"
                                  autoComplete="new-password"
                                  placeholder={
                                    editingChannel.value
                                      ? t("m.0c5bcbe51a9f")
                                      : ""
                                  }
                                  value={channelForm.signingSecret}
                                  onChange={(event) => {
                                    channelForm.signingSecret =
                                      event.target.value;
                                    channelForm.clearSigning = false;
                                  }}
                                />,
                              )}
                              {editingChannel.value &&
                                channels.value.find(
                                  (c) => c.id === editingChannel.value,
                                )?.hasSigningSecret &&
                                row(
                                  "channel-clear-signing",
                                  t("channels.clearSigning"),
                                  <Checkbox
                                    id="alert-channel-clear-signing"
                                    checked={channelForm.clearSigning}
                                    onCheckedChange={(value) => {
                                      channelForm.clearSigning = value === true;
                                      channelForm.signingSecret = "";
                                    }}
                                  />,
                                )}
                            </>
                          )}
                          {selectedProvider.value?.customRequest && (
                            <>
                              {row(
                                "channel-method",
                                t("channels.method"),
                                <Select
                                  id="alert-channel-method"
                                  value={channelForm.method}
                                  onValueChange={(value) => {
                                    channelForm.method = value;
                                  }}
                                  options={(
                                    selectedProvider.value.methods ?? []
                                  ).map((value) => ({ value, label: value }))}
                                />,
                              )}
                              {row(
                                "channel-headers",
                                t("channels.headers"),
                                <ExpandedTextarea
                                  id="alert-channel-headers"
                                  rows={3}
                                  spellCheck={false}
                                  autoComplete="off"
                                  placeholder={
                                    editingChannel.value
                                      ? t("channels.headersKeep")
                                      : '{"Authorization":"…"}'
                                  }
                                  value={channelForm.headers}
                                  onChange={(event) => {
                                    channelForm.headers = event.target.value;
                                    channelForm.clearHeaders = false;
                                  }}
                                />,
                              )}
                              {editingChannel.value &&
                                channels.value.find(
                                  (c) => c.id === editingChannel.value,
                                )?.hasHeaders &&
                                row(
                                  "channel-clear-headers",
                                  t("channels.clearHeaders"),
                                  <Checkbox
                                    id="alert-channel-clear-headers"
                                    checked={channelForm.clearHeaders}
                                    onCheckedChange={(value) => {
                                      channelForm.clearHeaders = value === true;
                                      channelForm.headers = "";
                                    }}
                                  />,
                                )}
                              {row(
                                "channel-body",
                                t("channels.body"),
                                <ExpandedTextarea
                                  id="alert-channel-body"
                                  rows={4}
                                  spellCheck={false}
                                  placeholder={'{"text":"{{message}}"}'}
                                  value={channelForm.bodyTemplate}
                                  onChange={(event) => {
                                    channelForm.bodyTemplate =
                                      event.target.value;
                                  }}
                                />,
                                t("channels.bodyHelp"),
                              )}
                            </>
                          )}
                        </>
                      ) : (
                        <>
                          {row(
                            "smtp-host",
                            t("m.5344a59d11c5"),
                            <Input
                              id="alert-smtp-host"
                              required
                              placeholder="smtp.example.com"
                              value={channelForm.smtpHost}
                              onChange={(event) => {
                                channelForm.smtpHost = event.target.value;
                              }}
                            />,
                          )}
                          {row(
                            "smtp-port",
                            t("m.e71ac32b544b"),
                            <Input
                              id="alert-smtp-port"
                              type="number"
                              required
                              min={1}
                              max={65535}
                              value={channelForm.smtpPort}
                              onChange={(event) => {
                                channelForm.smtpPort = Number(
                                  event.target.value,
                                );
                              }}
                            />,
                          )}
                          {row(
                            "smtp-security",
                            t("m.475ebf9f8ba3"),
                            <Select
                              id="alert-smtp-security"
                              value={channelForm.smtpSecurityMode}
                              onValueChange={(value) => {
                                channelForm.smtpSecurityMode =
                                  value as typeof channelForm.smtpSecurityMode;
                              }}
                              options={[
                                {
                                  value: "",
                                  label: t("m.45fa6e264d64"),
                                  disabled: true,
                                },
                                {
                                  value: "starttls",
                                  label: t("m.5b097c6cccbc"),
                                },
                                {
                                  value: "implicit_tls",
                                  label: t("m.906148fea2fc"),
                                },
                              ]}
                            />,
                            t("m.c373a9cc1577"),
                          )}
                          {row(
                            "smtp-from",
                            t("m.bae32961a3b5"),
                            <Input
                              id="alert-smtp-from"
                              name="smtpFrom"
                              inputMode="email"
                              required
                              value={channelForm.smtpFrom}
                              onChange={(event) => {
                                channelForm.smtpFrom = event.target.value;
                              }}
                            />,
                          )}
                          {row(
                            "smtp-to",
                            t("m.c237b665a6d8"),
                            <Input
                              id="alert-smtp-to"
                              name="smtpTo"
                              inputMode="email"
                              required
                              value={channelForm.smtpTo}
                              onChange={(event) => {
                                channelForm.smtpTo = event.target.value;
                              }}
                            />,
                          )}
                          {row(
                            "smtp-user",
                            t("m.e67fea90bde6"),
                            <Input
                              id="alert-smtp-user"
                              autoComplete="off"
                              value={channelForm.smtpUsername}
                              onChange={(event) => {
                                channelForm.smtpUsername = event.target.value;
                              }}
                            />,
                          )}
                          {row(
                            "smtp-secret",
                            t("m.82170ccaa6b5"),
                            <Input
                              id="alert-smtp-secret"
                              type="password"
                              autoComplete="new-password"
                              placeholder={
                                editingChannel.value &&
                                channels.value.find(
                                  (channel) =>
                                    channel.id === editingChannel.value,
                                )?.hasSecret
                                  ? t("m.0c5bcbe51a9f")
                                  : t("m.61a7dfb3a2c4")
                              }
                              value={channelForm.secret}
                              onChange={(event) => {
                                channelForm.secret = event.target.value;
                              }}
                            />,
                          )}
                        </>
                      )}
                      {row(
                        "channel-template",
                        t("m.fa3b2035a83a"),
                        <ExpandedTextarea
                          id="alert-channel-template"
                          rows={4}
                          spellCheck={false}
                          placeholder="{{severity}} · {{ruleName}}：{{message}}"
                          value={channelForm.messageTemplate}
                          onChange={(event) => {
                            channelForm.messageTemplate = event.target.value;
                          }}
                        />,
                        t("m.aaedf01e6fae"),
                      )}
                      {row(
                        "channel-enabled",
                        t("m.848250ce08f9"),
                        <Checkbox
                          id="alert-channel-enabled"
                          checked={channelForm.enabled}
                          onCheckedChange={(value) => {
                            channelForm.enabled = value === true;
                          }}
                        />,
                      )}
                    </SettingsGroup>
                    <div className="settings-section-actions">
                      {editingChannel.value ? (
                        <Button
                          type="button"
                          variant="secondary"
                          onClick={clearChannelForm}
                        >
                          {t("m.4b29ce8e4093")}
                        </Button>
                      ) : null}
                      <Button type="submit" disabled={saving.value}>
                        {saving.value
                          ? t("m.ff509c9ba052")
                          : t("m.5fb0d9558110")}
                      </Button>
                    </div>
                  </form>
                </>
              ) : tab.value === "windows" ? (
                <>
                  <section className="workspace-section window-list">
                    {heading(t("m.cb20b84d602a"))}
                    {windows.value.length ? (
                      <SettingsGroup>
                        {windows.value.map((window) => (
                          <article key={window.id} className="alert-record">
                            <strong>
                              {window.kind === "maintenance"
                                ? t("m.21138f1b0642")
                                : t("m.bf436f2fa459")}{" "}
                              ·{" "}
                              {window.scopeType === "global"
                                ? t("m.63d6b47116de")
                                : window.scopeType === "node"
                                  ? t("m.2410d860f093")
                                  : t("m.1123245d1bed")}
                            </strong>
                            <small>
                              {when(window.startsAt)} — {when(window.endsAt)}
                            </small>
                            <p>{window.reason}</p>
                            <div className="settings-section-actions">
                              <Button
                                variant="danger"
                                onClick={() => void deleteWindow(window)}
                              >
                                {t("m.4e6fd0e28c55")}
                              </Button>
                            </div>
                          </article>
                        ))}
                      </SettingsGroup>
                    ) : (
                      empty(t("m.20975dbddcfd"))
                    )}
                  </section>
                  <form
                    className="workspace-section alert-form-card alert-window-form"
                    onSubmit={(event) => {
                      event.preventDefault();
                      void saveWindow();
                    }}
                  >
                    {heading(t("m.ad7d42133c82"))}
                    <SettingsGroup>
                      {row(
                        "window-kind",
                        t("m.ba40014ff496"),
                        <Select
                          id="alert-window-kind"
                          value={windowForm.kind}
                          onValueChange={(value) => {
                            windowForm.kind = value as AlertWindow["kind"];
                          }}
                          options={[
                            {
                              value: "maintenance",
                              label: t("m.21138f1b0642"),
                            },
                            { value: "silence", label: t("m.bf436f2fa459") },
                          ]}
                        />,
                      )}
                      {row(
                        "window-scope",
                        t("m.ae72b532f4b8"),
                        <Select
                          id="alert-window-scope"
                          value={windowForm.scopeType}
                          onValueChange={(value) => {
                            windowForm.scopeType =
                              value as AlertWindow["scopeType"];
                          }}
                          options={[
                            { value: "global", label: t("m.63d6b47116de") },
                            { value: "node", label: t("m.f8c3f7bee9ee") },
                            { value: "rule", label: t("m.dff9fdf5372b") },
                          ]}
                        />,
                      )}
                      {windowForm.scopeType !== "global"
                        ? row(
                            "window-id",
                            t("m.67cdf2db35e4"),
                            <Input
                              id="alert-window-id"
                              required
                              placeholder="UUID"
                              value={windowForm.scopeId}
                              onChange={(event) => {
                                windowForm.scopeId = event.target.value;
                              }}
                            />,
                          )
                        : null}
                      {row(
                        "window-start",
                        t("m.d2bb025a2e51"),
                        <Input
                          id="alert-window-start"
                          type="datetime-local"
                          className="min-w-0"
                          value={windowForm.startsAt}
                          onChange={(event) => {
                            windowForm.startsAt = event.target.value;
                          }}
                        />,
                        t("m.e4926094a3a4"),
                      )}
                      {row(
                        "window-end",
                        t("m.c7b24e7997e9"),
                        <Input
                          id="alert-window-end"
                          type="datetime-local"
                          className="min-w-0"
                          value={windowForm.endsAt}
                          onChange={(event) => {
                            windowForm.endsAt = event.target.value;
                          }}
                        />,
                      )}
                      {row(
                        "window-reason",
                        t("m.f029d9cb4a76"),
                        <Input
                          id="alert-window-reason"
                          required
                          placeholder={t("m.440715526b90")}
                          value={windowForm.reason}
                          onChange={(event) => {
                            windowForm.reason = event.target.value;
                          }}
                        />,
                      )}
                    </SettingsGroup>
                    <div className="settings-section-actions">
                      <Button type="submit" disabled={saving.value}>
                        {t("m.2cb77284fedd")}
                      </Button>
                    </div>
                  </form>
                </>
              ) : (
                <section className="workspace-section alert-deliveries">
                  {heading(t("m.0d09685cc503"))}
                  {deliveries.value.length ? (
                    <SortableDataTable
                      rows={deliveries.value.map((delivery) => ({
                        ...delivery,
                      }))}
                      rowKey="id"
                      caption={t("m.0d09685cc503")}
                      columns={[
                        { key: "channelName", label: t("m.c35ffb529d5d") },
                        {
                          key: "kind",
                          label: t("m.0d12cbd6562b"),
                          render: (_, delivery) =>
                            delivery.testSend
                              ? t("m.6aa8f49cc992")
                              : delivery.kind.toUpperCase(),
                        },
                        {
                          key: "status",
                          label: t("m.6320b4a8722a"),
                          render: (_, delivery) => (
                            <Badge
                              tone={
                                delivery.status === "failed"
                                  ? "danger"
                                  : "neutral"
                              }
                            >
                              {deliveryLabel(delivery.status)}
                            </Badge>
                          ),
                        },
                        {
                          key: "attempts",
                          label: t("m.58a823852f8a"),
                          render: (_, delivery) =>
                            `${delivery.attempts} / ${delivery.maxAttempts}`,
                        },
                        {
                          key: "httpStatus",
                          label: "HTTP",
                          render: (_, delivery) => delivery.httpStatus ?? "—",
                        },
                        {
                          key: "createdAt",
                          label: t("m.8b6ff498515b"),
                          render: (_, delivery) =>
                            when(delivery.deliveredAt ?? delivery.createdAt),
                        },
                        {
                          key: "lastError",
                          label: t("m.0bc1fb72ae1b"),
                          render: (_, delivery) =>
                            display(delivery.lastError || "—"),
                        },
                      ]}
                    />
                  ) : (
                    empty(t("m.7bf308b00b96"))
                  )}
                </section>
              )}
            </TabsContent>
          </Tabs>
        </ManagementPage>
      );
    },
    input,
    [],
    [],
    {},
    true,
  );
}
