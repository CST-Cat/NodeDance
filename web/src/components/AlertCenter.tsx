import {
  api,
  operationRequest,
  type AgentNode,
  type AlertChannel,
  type AlertDelivery,
  type AlertEvent,
  type AlertItem,
  type AlertRule,
  type AlertWindow,
} from "../api";
import { display, locale, t } from "../i18n";
import { useViewTab } from "../navigation";
import { afterMount, beforeDispose, cell, derived, observable } from "../state";

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import "./AlertCenter.css";
import UiSelect from "./UiSelect";
type Props = {} & { children?: import("react").ReactNode; [key: string]: any };
export default function AlertCenter(input: Props) {
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
      const windows = cell<AlertWindow[]>([]);
      const deliveries = cell<AlertDelivery[]>([]);
      const events = cell<AlertEvent[]>([]);
      const selectedAlert = cell("");
      const editingChannel = cell("");
      const ruleForm = observable({
        name: "",
        kind: "cpu" as AlertRule["kind"],
        nodeId: "",
        subjectId: "",
        severity: "warning" as AlertRule["severity"],
        threshold: 90,
        durationSeconds: 300,
        cooldownSeconds: 300,
        expectedState: "running",
        enabled: true,
        channelIds: [] as string[],
      });
      const channelForm = observable({
        name: "",
        kind: "webhook" as AlertChannel["kind"],
        webhookUrl: "",
        messageTemplate: "",
        smtpHost: "",
        smtpPort: 587,
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
      async function refresh() {
        loading.value = true;
        error.value = "";
        const results = await Promise.allSettled([
          api.nodes(),
          api.activeAlerts(),
          api.alertHistory(),
          api.alertRules(),
          api.alertChannels(),
          api.alertWindows(),
          api.alertDeliveries(),
          api.alertEvents(),
          operationRequest<{
            metrics: Record<string, string>;
          }>("metrics.catalog"),
        ]);
        const failure = results.find((item) => item.status === "rejected");
        if (failure?.status === "rejected")
          error.value = message(failure.reason);
        if (results[0].status === "fulfilled") {
          nodes.value = results[0].value.nodes;
          if (!ruleForm.nodeId) ruleForm.nodeId = nodes.value[0]?.nodeId ?? "";
        }
        if (results[1].status === "fulfilled")
          active.value = results[1].value.alerts;
        if (results[2].status === "fulfilled")
          history.value = results[2].value.alerts;
        if (results[3].status === "fulfilled")
          rules.value = results[3].value.rules;
        if (results[4].status === "fulfilled")
          channels.value = results[4].value.channels;
        if (results[5].status === "fulfilled")
          windows.value = results[5].value.windows;
        if (results[6].status === "fulfilled")
          deliveries.value = results[6].value.deliveries;
        if (results[7].status === "fulfilled")
          events.value = results[7].value.events;
        if (results[8].status === "fulfilled")
          metricCatalog.value = results[8].value.metrics;
        loading.value = false;
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
          await api.createAlertRule(body);
          ruleForm.name = "";
          ruleForm.subjectId = "";
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
        const smtpSecurityMode = channelForm.smtpSecurityMode;
        if (
          channelForm.kind === "smtp" &&
          smtpSecurityMode !== "starttls" &&
          smtpSecurityMode !== "implicit_tls"
        ) {
          error.value = t("m.4beb86e21302");
          return;
        }
        const smtpConfigMode =
          smtpSecurityMode === "starttls" || smtpSecurityMode === "implicit_tls"
            ? smtpSecurityMode
            : undefined;
        clearMessages();
        saving.value = true;
        try {
          const messageTemplate = channelForm.messageTemplate || undefined;
          const config =
            channelForm.kind === "webhook"
              ? {
                  webhookUrl: channelForm.webhookUrl,
                  ...(messageTemplate
                    ? {
                        messageTemplate,
                      }
                    : {}),
                }
              : {
                  smtpHost: channelForm.smtpHost,
                  smtpPort: Number(channelForm.smtpPort),
                  smtpSecurityMode: smtpConfigMode,
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
          await refresh();
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
      afterMount(() => {
        void refresh();
        refreshTimer = window.setInterval(() => {
          if (!loading.value) void refresh();
        }, 30_000);
      });
      beforeDispose(() => {
        if (refreshTimer !== undefined) window.clearInterval(refreshTimer);
      });
      return () => (
        <>
          <section
            className={__classes("alerts-page workspace-page")}
            data-v-acd260e8=""
          >
            <header className={__classes("alerts-heading")} data-v-acd260e8="">
              <div data-v-acd260e8="">
                <span className={__classes("eyebrow")} data-v-acd260e8="">
                  {t("label.74b98e3066ff")}
                </span>
                <h1 data-v-acd260e8="">
                  {t("m.1b92ed81c005")}
                  <span data-v-acd260e8="">{"."}</span>
                </h1>
                <p data-v-acd260e8="">{t("m.30dc14a37a07")}</p>
              </div>
              <button
                type="button"
                disabled={loading.value as any}
                className={__classes("alert-button subtle")}
                onClick={(event: any) => {
                  __call(refresh, event);
                }}
                data-v-acd260e8=""
              >
                {loading.value ? t("m.7f3c8312816f") : t("m.0468ee76ee3d")}
              </button>
            </header>
            {error.value ? (
              <div
                role="alert"
                className={__classes("alert-message danger")}
                data-v-acd260e8=""
              >
                {display(error.value)}
              </div>
            ) : null}
            {notice.value ? (
              <div
                role="status"
                className={__classes("alert-message success")}
                data-v-acd260e8=""
              >
                {display(notice.value)}
              </div>
            ) : null}
            <nav
              aria-label={t("m.6c08cfefefbc") as any}
              className={__classes("alert-tabs")}
              data-v-acd260e8=""
            >
              <Fragment>
                {__each(
                  [
                    ["active", t("m.ee404f35c3ce")],
                    ["history", t("m.7fe7f1f0fb28")],
                    ["rules", t("m.7488ac33d3de")],
                    ["channels", t("m.b2354cebb5bc")],
                    ["windows", t("m.cb20b84d602a")],
                    ["deliveries", t("m.f0c5fa02c974")],
                  ] as Array<[Tab, string]>,
                  (item) => (
                    <button
                      key={item[0] as any}
                      type="button"
                      aria-current={
                        tab.value === item[0] ? "page" : (undefined as any)
                      }
                      onClick={(event: any) => {
                        tab.value = item[0];
                      }}
                      data-v-acd260e8=""
                    >
                      {item[1]}
                      {item[0] === "active" && active.value.length ? (
                        <span data-v-acd260e8="">{active.value.length}</span>
                      ) : null}
                    </button>
                  ),
                )}
              </Fragment>
            </nav>
            {tab.value === "active" || tab.value === "history" ? (
              <section className={__classes("alert-card")} data-v-acd260e8="">
                <div className={__classes("section-title")} data-v-acd260e8="">
                  <div data-v-acd260e8="">
                    <h2 data-v-acd260e8="">
                      {tab.value === "active"
                        ? t("m.e713ce880f38")
                        : t("m.f49736f12f80")}
                    </h2>
                    <p data-v-acd260e8="">
                      {tab.value === "active"
                        ? t("m.0fa70890a3d2")
                        : t("m.9bb92d183ceb")}
                    </p>
                  </div>
                </div>
                {activeList().length ? (
                  <div className={__classes("alert-list")} data-v-acd260e8="">
                    <Fragment>
                      {__each(activeList(), (item) => (
                        <article
                          key={item.id as any}
                          data-severity={item.severity as any}
                          className={__classes("alert-row")}
                          data-v-acd260e8=""
                        >
                          <div
                            aria-hidden="true"
                            className={__classes("severity-mark")}
                            data-v-acd260e8=""
                          ></div>
                          <div
                            className={__classes("alert-content")}
                            data-v-acd260e8=""
                          >
                            <div
                              className={__classes("alert-title")}
                              data-v-acd260e8=""
                            >
                              <strong data-v-acd260e8="">
                                {item.ruleName}
                              </strong>
                              <span
                                className={__classes("pill", item.status)}
                                data-v-acd260e8=""
                              >
                                {item.status === "active"
                                  ? t("m.3c01089ea0e4")
                                  : t("m.3617f737f437")}
                              </span>
                              {item.acknowledgedAt ? (
                                <span
                                  className={__classes("pill acknowledged")}
                                  data-v-acd260e8=""
                                >
                                  {t("m.d56a511f498f")}
                                </span>
                              ) : null}
                              {item.suppressionReason ? (
                                <span
                                  className={__classes("pill suppressed")}
                                  data-v-acd260e8=""
                                >
                                  {item.suppressionReason === "node_offline"
                                    ? t("m.c46a19c8d6ca")
                                    : t("m.5329772f4d2b")}
                                </span>
                              ) : null}
                            </div>
                            <p data-v-acd260e8="">{display(item.message)}</p>
                            <small data-v-acd260e8="">
                              {item.nodeName}
                              {" · "}
                              {item.nodeId}
                              {t("m.a2f8212e41a4")}
                              {when(item.firstSeenAt)}
                              {item.resolvedAt ? (
                                <Fragment>
                                  {t("m.daf142e39346")}
                                  {when(item.resolvedAt)}
                                </Fragment>
                              ) : null}
                            </small>
                          </div>
                          {item.status === "active" ? (
                            <div
                              className={__classes("row-actions")}
                              data-v-acd260e8=""
                            >
                              <button
                                type="button"
                                disabled={!!item.acknowledgedAt as any}
                                className={__classes("alert-button")}
                                onClick={(event: any) => {
                                  acknowledge(item);
                                }}
                                data-v-acd260e8=""
                              >
                                {t("m.36f33adaf094")}
                              </button>
                              <button
                                type="button"
                                className={__classes("alert-button subtle")}
                                onClick={(event: any) => {
                                  silence(item);
                                }}
                                data-v-acd260e8=""
                              >
                                {t("m.d69600baef4b")}
                              </button>
                            </div>
                          ) : null}
                        </article>
                      ))}
                    </Fragment>
                  </div>
                ) : (
                  <div className={__classes("empty-state")} data-v-acd260e8="">
                    {tab.value === "active"
                      ? t("m.b2f037729dad")
                      : t("m.8eac1ef8ee32")}
                  </div>
                )}
                {tab.value === "history" ? (
                  <div className={__classes("event-list")} data-v-acd260e8="">
                    <h3 data-v-acd260e8="">{t("m.512c5669c853")}</h3>
                    <Fragment>
                      {__each(events.value, (event) => (
                        <article key={event.id as any} data-v-acd260e8="">
                          <span data-v-acd260e8="">{event.kind}</span>
                          <span data-v-acd260e8="">
                            {display(event.message)}
                          </span>
                          <time data-v-acd260e8="">
                            {when(event.occurredAt)}
                          </time>
                        </article>
                      ))}
                    </Fragment>
                    {!events.value.length ? (
                      <p
                        className={__classes("empty-state")}
                        data-v-acd260e8=""
                      >
                        {t("m.d3ae2b8f4adf")}
                      </p>
                    ) : null}
                  </div>
                ) : null}
              </section>
            ) : tab.value === "rules" ? (
              <section className={__classes("alert-grid")} data-v-acd260e8="">
                <div className={__classes("alert-card")} data-v-acd260e8="">
                  <div
                    className={__classes("section-title")}
                    data-v-acd260e8=""
                  >
                    <div data-v-acd260e8="">
                      <h2 data-v-acd260e8="">{t("m.52ad1003d627")}</h2>
                      <p data-v-acd260e8="">{t("m.b609ad7cd505")}</p>
                    </div>
                  </div>
                  <div className={__classes("table-wrap")} data-v-acd260e8="">
                    <table data-v-acd260e8="">
                      <thead data-v-acd260e8="">
                        <tr data-v-acd260e8="">
                          <th data-v-acd260e8="">{t("m.1123245d1bed")}</th>
                          <th data-v-acd260e8="">{t("m.2410d860f093")}</th>
                          <th data-v-acd260e8="">{t("m.16bb1d2b098c")}</th>
                          <th data-v-acd260e8="">{t("m.1882ae19b6a2")}</th>
                          <th data-v-acd260e8="">{t("m.6320b4a8722a")}</th>
                          <th data-v-acd260e8=""></th>
                        </tr>
                      </thead>
                      <tbody data-v-acd260e8="">
                        <Fragment>
                          {__each(rules.value, (rule) => (
                            <tr key={rule.id as any} data-v-acd260e8="">
                              <td data-v-acd260e8="">{rule.name}</td>
                              <td data-v-acd260e8="">
                                {nodes.value.find(
                                  (n) => n.nodeId === rule.nodeId,
                                )?.displayName ?? rule.nodeId}
                              </td>
                              <td data-v-acd260e8="">
                                {ruleLabel(rule.kind)}
                                {rule.threshold !== undefined ? (
                                  <Fragment>
                                    {" > "}
                                    {rule.threshold}
                                    {[
                                      "cpu",
                                      "memory",
                                      "disk",
                                      "probe_failure",
                                      "probe_packet_loss",
                                    ].includes(rule.kind)
                                      ? "%"
                                      : ""}
                                  </Fragment>
                                ) : null}
                                {rule.expectedState ? (
                                  <Fragment>
                                    {" ≠ "}
                                    {rule.expectedState}
                                  </Fragment>
                                ) : null}
                              </td>
                              <td data-v-acd260e8="">
                                {rule.durationSeconds}
                                {t("m.9dcdc2b289b9")}
                              </td>
                              <td data-v-acd260e8="">
                                {rule.enabled
                                  ? t("m.dfb802238b38")
                                  : t("m.a8c3698b5b8c")}
                              </td>
                              <td data-v-acd260e8="">
                                <button
                                  type="button"
                                  className={__classes(
                                    "text-button danger-text",
                                  )}
                                  onClick={(event: any) => {
                                    deleteRule(rule);
                                  }}
                                  data-v-acd260e8=""
                                >
                                  {t("m.2f9daa828907")}
                                </button>
                              </td>
                            </tr>
                          ))}
                        </Fragment>
                      </tbody>
                    </table>
                  </div>
                  {!rules.value.length ? (
                    <p className={__classes("empty-state")} data-v-acd260e8="">
                      {t("m.915b0451461e")}
                    </p>
                  ) : null}
                </div>
                <form
                  className={__classes("alert-card alert-form-card")}
                  onSubmit={(event: any) => {
                    event.preventDefault();
                    __call(saveRule, event);
                  }}
                  data-v-acd260e8=""
                >
                  <div
                    className={__classes("section-title")}
                    data-v-acd260e8=""
                  >
                    <div data-v-acd260e8="">
                      <h2 data-v-acd260e8="">{t("m.83009fcfe6c1")}</h2>
                      <p data-v-acd260e8="">{t("m.b8fa37ab5ac0")}</p>
                    </div>
                  </div>
                  <label data-v-acd260e8="">
                    {t("m.18dba51b47c8")}
                    <input
                      required={true}
                      maxLength={80}
                      placeholder={t("m.fb8ad27501de") as any}
                      {...__input(
                        ruleForm.name,
                        (__nextValue: any) => {
                          ruleForm.name = __nextValue;
                        },
                        "",
                        undefined,
                        [],
                      )}
                      data-v-acd260e8=""
                    ></input>
                  </label>
                  <label data-v-acd260e8="">
                    {t("m.2410d860f093")}
                    <UiSelect
                      required={true}
                      modelValue={ruleForm.nodeId}
                      onUpdateModelValue={(__nextValue: any) => {
                        ruleForm.nodeId = __nextValue;
                      }}
                      modelModifiers={{}}
                      data-v-acd260e8=""
                    >
                      <Fragment>
                        {__each(nodes.value, (node) => (
                          <option
                            key={node.nodeId as any}
                            value={node.nodeId as any}
                            data-v-acd260e8=""
                          >
                            {node.displayName}
                            {" · "}
                            {node.status}
                          </option>
                        ))}
                      </Fragment>
                    </UiSelect>
                  </label>
                  <label data-v-acd260e8="">
                    {t("m.16bb1d2b098c")}
                    <UiSelect
                      modelValue={ruleForm.kind}
                      onUpdateModelValue={(__nextValue: any) => {
                        ruleForm.kind = __nextValue;
                      }}
                      modelModifiers={{}}
                      data-v-acd260e8=""
                    >
                      <Fragment>
                        {__each(kindOptions.value, (item) => (
                          <option
                            key={item.value as any}
                            value={item.value as any}
                            data-v-acd260e8=""
                          >
                            {item.label}
                          </option>
                        ))}
                      </Fragment>
                    </UiSelect>
                  </label>
                  {needsThreshold.value ? (
                    <label data-v-acd260e8="">
                      {t("m.26a4ac708409")}
                      <input
                        type="number"
                        min="0"
                        max={
                          [
                            "cpu",
                            "memory",
                            "disk",
                            "probe_failure",
                            "probe_packet_loss",
                          ].includes(ruleForm.kind)
                            ? 100
                            : (undefined as any)
                        }
                        step="0.1"
                        required={true}
                        {...__input(
                          ruleForm.threshold,
                          (__nextValue: any) => {
                            ruleForm.threshold = __nextValue;
                          },
                          "number",
                          undefined,
                          ["number"],
                        )}
                        data-v-acd260e8=""
                      ></input>
                    </label>
                  ) : null}
                  {needsSubject.value ? (
                    <label data-v-acd260e8="">
                      {t("m.ad293c799482")}
                      {ruleForm.kind === "metric" ? (
                        <UiSelect
                          required={true}
                          modelValue={ruleForm.subjectId}
                          onUpdateModelValue={(__nextValue: any) => {
                            ruleForm.subjectId = __nextValue;
                          }}
                          modelModifiers={{}}
                          data-v-acd260e8=""
                        >
                          <Fragment>
                            {__each(metricCatalog.value, (unit, key) => (
                              <option
                                key={key as any}
                                value={key as any}
                                data-v-acd260e8=""
                              >
                                {key}
                                {" · "}
                                {unit}
                              </option>
                            ))}
                          </Fragment>
                        </UiSelect>
                      ) : (
                        <input
                          required={true}
                          placeholder={t("m.8a90cd8dc652") as any}
                          {...__input(
                            ruleForm.subjectId,
                            (__nextValue: any) => {
                              ruleForm.subjectId = __nextValue;
                            },
                            "",
                            undefined,
                            [],
                          )}
                          data-v-acd260e8=""
                        ></input>
                      )}
                    </label>
                  ) : null}
                  {needsState.value ? (
                    <label data-v-acd260e8="">
                      {t("m.b3d94c7eff6f")}
                      <UiSelect
                        modelValue={ruleForm.expectedState}
                        onUpdateModelValue={(__nextValue: any) => {
                          ruleForm.expectedState = __nextValue;
                        }}
                        modelModifiers={{}}
                        data-v-acd260e8=""
                      >
                        <option value="running" data-v-acd260e8="">
                          {t("m.1f0eb99b7ed0")}
                        </option>
                        <option value="stopped" data-v-acd260e8="">
                          {t("m.f006455e3baf")}
                        </option>
                        <option value="healthy" data-v-acd260e8="">
                          {t("m.aa18b6dcd893")}
                        </option>
                        <option value="unhealthy" data-v-acd260e8="">
                          {t("m.395a7bf80b24")}
                        </option>
                      </UiSelect>
                    </label>
                  ) : null}
                  <div
                    className={__classes("inline-fields")}
                    data-v-acd260e8=""
                  >
                    <label data-v-acd260e8="">
                      {t("m.f67bd602eaa1")}
                      <input
                        type="number"
                        min="0"
                        max="86400"
                        {...__input(
                          ruleForm.durationSeconds,
                          (__nextValue: any) => {
                            ruleForm.durationSeconds = __nextValue;
                          },
                          "number",
                          undefined,
                          ["number"],
                        )}
                        data-v-acd260e8=""
                      ></input>
                    </label>
                    <label data-v-acd260e8="">
                      {t("m.c90ed7f53ce4")}
                      <input
                        type="number"
                        min="0"
                        max="86400"
                        {...__input(
                          ruleForm.cooldownSeconds,
                          (__nextValue: any) => {
                            ruleForm.cooldownSeconds = __nextValue;
                          },
                          "number",
                          undefined,
                          ["number"],
                        )}
                        data-v-acd260e8=""
                      ></input>
                    </label>
                  </div>
                  <label data-v-acd260e8="">
                    {t("m.3d9d02e83d39")}
                    <UiSelect
                      modelValue={ruleForm.severity}
                      onUpdateModelValue={(__nextValue: any) => {
                        ruleForm.severity = __nextValue;
                      }}
                      modelModifiers={{}}
                      data-v-acd260e8=""
                    >
                      <option value="info" data-v-acd260e8="">
                        {t("m.f56c6c82203b")}
                      </option>
                      <option value="warning" data-v-acd260e8="">
                        {t("m.a8b7a4480407")}
                      </option>
                      <option value="critical" data-v-acd260e8="">
                        {t("m.73eb0e14e307")}
                      </option>
                    </UiSelect>
                  </label>
                  <fieldset data-v-acd260e8="">
                    <legend data-v-acd260e8="">{t("m.1bcd7265cd39")}</legend>
                    <Fragment>
                      {__each(channels.value, (channel) => (
                        <label
                          key={channel.id as any}
                          className={__classes("check-row")}
                          data-v-acd260e8=""
                        >
                          <input
                            type="checkbox"
                            value={channel.id as any}
                            {...__input(
                              ruleForm.channelIds,
                              (__nextValue: any) => {
                                ruleForm.channelIds = __nextValue;
                              },
                              "checkbox",
                              channel.id,
                              [],
                            )}
                            data-v-acd260e8=""
                          ></input>
                          {channel.name}
                        </label>
                      ))}
                    </Fragment>
                    {!channels.value.length ? (
                      <span className={__classes("hint")} data-v-acd260e8="">
                        {t("m.1c5c0b7d73e4")}
                      </span>
                    ) : null}
                  </fieldset>
                  <label className={__classes("check-row")} data-v-acd260e8="">
                    <input
                      type="checkbox"
                      {...__input(
                        ruleForm.enabled,
                        (__nextValue: any) => {
                          ruleForm.enabled = __nextValue;
                        },
                        "checkbox",
                        undefined,
                        [],
                      )}
                      data-v-acd260e8=""
                    ></input>
                    {t("m.e45a3f15b735")}
                  </label>
                  <button
                    type="submit"
                    disabled={saving.value || (!nodes.value.length as any)}
                    className={__classes("alert-button primary")}
                    data-v-acd260e8=""
                  >
                    {saving.value ? t("m.ff509c9ba052") : t("m.63faeddb2a46")}
                  </button>
                </form>
              </section>
            ) : tab.value === "channels" ? (
              <section className={__classes("alert-grid")} data-v-acd260e8="">
                <div className={__classes("alert-card")} data-v-acd260e8="">
                  <div
                    className={__classes("section-title")}
                    data-v-acd260e8=""
                  >
                    <div data-v-acd260e8="">
                      <h2 data-v-acd260e8="">{t("m.b2354cebb5bc")}</h2>
                      <p data-v-acd260e8="">{t("m.3cb6afac5598")}</p>
                    </div>
                  </div>
                  <div className={__classes("channel-list")} data-v-acd260e8="">
                    <Fragment>
                      {__each(channels.value, (channel) => (
                        <article key={channel.id as any} data-v-acd260e8="">
                          <div data-v-acd260e8="">
                            <strong data-v-acd260e8="">{channel.name}</strong>
                            <span
                              className={__classes("pill")}
                              data-v-acd260e8=""
                            >
                              {channel.kind === "webhook" ? "Webhook" : "SMTP"}
                            </span>
                            <small data-v-acd260e8="">
                              {channel.kind === "webhook"
                                ? channel.config.webhookUrl
                                : `${channel.config.smtpHost}:${channel.config.smtpPort} → ${channel.config.smtpTo}`}
                            </small>
                          </div>
                          <div
                            className={__classes("row-actions")}
                            data-v-acd260e8=""
                          >
                            <button
                              type="button"
                              className={__classes("alert-button")}
                              onClick={(event: any) => {
                                testChannel(channel);
                              }}
                              data-v-acd260e8=""
                            >
                              {t("m.e4263450f9ec")}
                            </button>
                            <button
                              type="button"
                              className={__classes("alert-button subtle")}
                              onClick={(event: any) => {
                                editChannel(channel);
                              }}
                              data-v-acd260e8=""
                            >
                              {t("m.051836569928")}
                            </button>
                            <button
                              type="button"
                              className={__classes("text-button danger-text")}
                              onClick={(event: any) => {
                                deleteChannel(channel);
                              }}
                              data-v-acd260e8=""
                            >
                              {t("m.2f9daa828907")}
                            </button>
                          </div>
                        </article>
                      ))}
                    </Fragment>
                    {!channels.value.length ? (
                      <p
                        className={__classes("empty-state")}
                        data-v-acd260e8=""
                      >
                        {t("m.6b2cde6d4a15")}
                      </p>
                    ) : null}
                  </div>
                </div>
                <form
                  className={__classes("alert-card alert-form-card")}
                  onSubmit={(event: any) => {
                    event.preventDefault();
                    __call(saveChannel, event);
                  }}
                  data-v-acd260e8=""
                >
                  <div
                    className={__classes("section-title")}
                    data-v-acd260e8=""
                  >
                    <div data-v-acd260e8="">
                      <h2 data-v-acd260e8="">
                        {editingChannel.value
                          ? t("m.9536f6b3a7da")
                          : t("m.edb55e6632ce")}
                      </h2>
                      <p data-v-acd260e8="">{t("m.7582c5883aa1")}</p>
                    </div>
                  </div>
                  <label data-v-acd260e8="">
                    {t("m.d44e9b3d3b31")}
                    <input
                      required={true}
                      maxLength={80}
                      placeholder={t("m.ab0113166ed7") as any}
                      {...__input(
                        channelForm.name,
                        (__nextValue: any) => {
                          channelForm.name = __nextValue;
                        },
                        "",
                        undefined,
                        [],
                      )}
                      data-v-acd260e8=""
                    ></input>
                  </label>
                  <label data-v-acd260e8="">
                    {t("m.ba40014ff496")}
                    <UiSelect
                      modelValue={channelForm.kind}
                      onUpdateModelValue={(__nextValue: any) => {
                        channelForm.kind = __nextValue;
                      }}
                      modelModifiers={{}}
                      data-v-acd260e8=""
                    >
                      <option value="webhook" data-v-acd260e8="">
                        {"Webhook"}
                      </option>
                      <option value="smtp" data-v-acd260e8="">
                        {t("m.06344b933760")}
                      </option>
                    </UiSelect>
                  </label>
                  {channelForm.kind === "webhook" ? (
                    <Fragment>
                      <label data-v-acd260e8="">
                        {t("label.84805a7574a8")}
                        <input
                          type="url"
                          required={true}
                          placeholder="https://hooks.example/…"
                          {...__input(
                            channelForm.webhookUrl,
                            (__nextValue: any) => {
                              channelForm.webhookUrl = __nextValue;
                            },
                            "url",
                            undefined,
                            [],
                          )}
                          data-v-acd260e8=""
                        ></input>
                      </label>
                      <small className={__classes("hint")} data-v-acd260e8="">
                        {t("m.ce3a64e47c4d")}
                      </small>
                    </Fragment>
                  ) : (
                    <Fragment>
                      <label data-v-acd260e8="">
                        {t("m.5344a59d11c5")}
                        <input
                          required={true}
                          placeholder="smtp.example.com"
                          {...__input(
                            channelForm.smtpHost,
                            (__nextValue: any) => {
                              channelForm.smtpHost = __nextValue;
                            },
                            "",
                            undefined,
                            [],
                          )}
                          data-v-acd260e8=""
                        ></input>
                      </label>
                      <label data-v-acd260e8="">
                        {t("m.e71ac32b544b")}
                        <input
                          type="number"
                          min="1"
                          max="65535"
                          required={true}
                          {...__input(
                            channelForm.smtpPort,
                            (__nextValue: any) => {
                              channelForm.smtpPort = __nextValue;
                            },
                            "number",
                            undefined,
                            ["number"],
                          )}
                          data-v-acd260e8=""
                        ></input>
                      </label>
                      <label data-v-acd260e8="">
                        {t("m.475ebf9f8ba3")}
                        <UiSelect
                          modelValue={channelForm.smtpSecurityMode}
                          onUpdateModelValue={(__nextValue: any) => {
                            channelForm.smtpSecurityMode = __nextValue;
                          }}
                          modelModifiers={{}}
                          data-v-acd260e8=""
                        >
                          <option value="" disabled={true} data-v-acd260e8="">
                            {t("m.45fa6e264d64")}
                          </option>
                          <option value="starttls" data-v-acd260e8="">
                            {t("m.5b097c6cccbc")}
                          </option>
                          <option value="implicit_tls" data-v-acd260e8="">
                            {t("m.906148fea2fc")}
                          </option>
                        </UiSelect>
                      </label>
                      <small className={__classes("hint")} data-v-acd260e8="">
                        {t("m.c373a9cc1577")}
                      </small>
                      <label data-v-acd260e8="">
                        {t("m.bae32961a3b5")}
                        <input
                          type="email"
                          required={true}
                          {...__input(
                            channelForm.smtpFrom,
                            (__nextValue: any) => {
                              channelForm.smtpFrom = __nextValue;
                            },
                            "email",
                            undefined,
                            [],
                          )}
                          data-v-acd260e8=""
                        ></input>
                      </label>
                      <label data-v-acd260e8="">
                        {t("m.c237b665a6d8")}
                        <input
                          type="email"
                          required={true}
                          {...__input(
                            channelForm.smtpTo,
                            (__nextValue: any) => {
                              channelForm.smtpTo = __nextValue;
                            },
                            "email",
                            undefined,
                            [],
                          )}
                          data-v-acd260e8=""
                        ></input>
                      </label>
                      <label data-v-acd260e8="">
                        {t("m.e67fea90bde6")}
                        <input
                          autoComplete="off"
                          {...__input(
                            channelForm.smtpUsername,
                            (__nextValue: any) => {
                              channelForm.smtpUsername = __nextValue;
                            },
                            "",
                            undefined,
                            [],
                          )}
                          data-v-acd260e8=""
                        ></input>
                      </label>
                      <label data-v-acd260e8="">
                        {t("m.82170ccaa6b5")}
                        <input
                          type="password"
                          autoComplete="new-password"
                          placeholder={
                            editingChannel.value &&
                            channels.value.find(
                              (x) => x.id === editingChannel.value,
                            )?.hasSecret
                              ? t("m.0c5bcbe51a9f")
                              : (t("m.61a7dfb3a2c4") as any)
                          }
                          {...__input(
                            channelForm.secret,
                            (__nextValue: any) => {
                              channelForm.secret = __nextValue;
                            },
                            "password",
                            undefined,
                            [],
                          )}
                          data-v-acd260e8=""
                        ></input>
                      </label>
                    </Fragment>
                  )}
                  <label data-v-acd260e8="">
                    {t("m.fa3b2035a83a")}
                    <textarea
                      maxLength={2048}
                      rows={4}
                      spellCheck="false"
                      placeholder="{{severity}} · {{ruleName}}：{{message}}"
                      {...__input(
                        channelForm.messageTemplate,
                        (__nextValue: any) => {
                          channelForm.messageTemplate = __nextValue;
                        },
                        "",
                        undefined,
                        [],
                      )}
                      data-v-acd260e8=""
                    ></textarea>
                  </label>
                  <small className={__classes("hint")} data-v-acd260e8="">
                    {t("m.aaedf01e6fae")}
                  </small>
                  <label className={__classes("check-row")} data-v-acd260e8="">
                    <input
                      type="checkbox"
                      {...__input(
                        channelForm.enabled,
                        (__nextValue: any) => {
                          channelForm.enabled = __nextValue;
                        },
                        "checkbox",
                        undefined,
                        [],
                      )}
                      data-v-acd260e8=""
                    ></input>
                    {t("m.848250ce08f9")}
                  </label>
                  <div className={__classes("row-actions")} data-v-acd260e8="">
                    <button
                      type="submit"
                      disabled={saving.value as any}
                      className={__classes("alert-button primary")}
                      data-v-acd260e8=""
                    >
                      {saving.value ? t("m.ff509c9ba052") : t("m.5fb0d9558110")}
                    </button>
                    {editingChannel.value ? (
                      <button
                        type="button"
                        className={__classes("alert-button subtle")}
                        onClick={(event: any) => {
                          __call(clearChannelForm, event);
                        }}
                        data-v-acd260e8=""
                      >
                        {t("m.4b29ce8e4093")}
                      </button>
                    ) : null}
                  </div>
                </form>
              </section>
            ) : tab.value === "windows" ? (
              <section className={__classes("alert-grid")} data-v-acd260e8="">
                <div className={__classes("alert-card")} data-v-acd260e8="">
                  <div
                    className={__classes("section-title")}
                    data-v-acd260e8=""
                  >
                    <div data-v-acd260e8="">
                      <h2 data-v-acd260e8="">{t("m.5b44b4cbe8e7")}</h2>
                      <p data-v-acd260e8="">{t("m.9ba72a13180d")}</p>
                    </div>
                  </div>
                  <div className={__classes("window-list")} data-v-acd260e8="">
                    <Fragment>
                      {__each(windows.value, (window) => (
                        <article key={window.id as any} data-v-acd260e8="">
                          <div data-v-acd260e8="">
                            <strong data-v-acd260e8="">
                              {window.kind === "maintenance"
                                ? t("m.1e9fa64d9d1e")
                                : t("m.2edd4433070c")}
                              {" · "}
                              {window.scopeType === "global"
                                ? t("m.63d6b47116de")
                                : window.scopeType === "node"
                                  ? t("m.2410d860f093")
                                  : t("m.1123245d1bed")}
                            </strong>
                            <small data-v-acd260e8="">
                              {when(window.startsAt)}
                              {" — "}
                              {when(window.endsAt)}
                            </small>
                            <p data-v-acd260e8="">{window.reason}</p>
                          </div>
                          <button
                            type="button"
                            className={__classes("text-button danger-text")}
                            onClick={(event: any) => {
                              deleteWindow(window);
                            }}
                            data-v-acd260e8=""
                          >
                            {t("m.4e6fd0e28c55")}
                          </button>
                        </article>
                      ))}
                    </Fragment>
                    {!windows.value.length ? (
                      <p
                        className={__classes("empty-state")}
                        data-v-acd260e8=""
                      >
                        {t("m.20975dbddcfd")}
                      </p>
                    ) : null}
                  </div>
                </div>
                <form
                  className={__classes("alert-card alert-form-card")}
                  onSubmit={(event: any) => {
                    event.preventDefault();
                    __call(saveWindow, event);
                  }}
                  data-v-acd260e8=""
                >
                  <div
                    className={__classes("section-title")}
                    data-v-acd260e8=""
                  >
                    <div data-v-acd260e8="">
                      <h2 data-v-acd260e8="">{t("m.ad7d42133c82")}</h2>
                      <p data-v-acd260e8="">{t("m.e4926094a3a4")}</p>
                    </div>
                  </div>
                  <label data-v-acd260e8="">
                    {t("m.ba40014ff496")}
                    <UiSelect
                      modelValue={windowForm.kind}
                      onUpdateModelValue={(__nextValue: any) => {
                        windowForm.kind = __nextValue;
                      }}
                      modelModifiers={{}}
                      data-v-acd260e8=""
                    >
                      <option value="maintenance" data-v-acd260e8="">
                        {t("m.21138f1b0642")}
                      </option>
                      <option value="silence" data-v-acd260e8="">
                        {t("m.bf436f2fa459")}
                      </option>
                    </UiSelect>
                  </label>
                  <label data-v-acd260e8="">
                    {t("m.ae72b532f4b8")}
                    <UiSelect
                      modelValue={windowForm.scopeType}
                      onUpdateModelValue={(__nextValue: any) => {
                        windowForm.scopeType = __nextValue;
                      }}
                      modelModifiers={{}}
                      data-v-acd260e8=""
                    >
                      <option value="global" data-v-acd260e8="">
                        {t("m.63d6b47116de")}
                      </option>
                      <option value="node" data-v-acd260e8="">
                        {t("m.f8c3f7bee9ee")}
                      </option>
                      <option value="rule" data-v-acd260e8="">
                        {t("m.dff9fdf5372b")}
                      </option>
                    </UiSelect>
                  </label>
                  {windowForm.scopeType !== "global" ? (
                    <label data-v-acd260e8="">
                      {t("m.67cdf2db35e4")}
                      <input
                        required={true}
                        placeholder="UUID"
                        {...__input(
                          windowForm.scopeId,
                          (__nextValue: any) => {
                            windowForm.scopeId = __nextValue;
                          },
                          "",
                          undefined,
                          [],
                        )}
                        data-v-acd260e8=""
                      ></input>
                    </label>
                  ) : null}
                  <div
                    className={__classes("inline-fields")}
                    data-v-acd260e8=""
                  >
                    <label data-v-acd260e8="">
                      {t("m.d2bb025a2e51")}
                      <input
                        type="datetime-local"
                        {...__input(
                          windowForm.startsAt,
                          (__nextValue: any) => {
                            windowForm.startsAt = __nextValue;
                          },
                          "datetime-local",
                          undefined,
                          [],
                        )}
                        data-v-acd260e8=""
                      ></input>
                    </label>
                    <label data-v-acd260e8="">
                      {t("m.c7b24e7997e9")}
                      <input
                        type="datetime-local"
                        {...__input(
                          windowForm.endsAt,
                          (__nextValue: any) => {
                            windowForm.endsAt = __nextValue;
                          },
                          "datetime-local",
                          undefined,
                          [],
                        )}
                        data-v-acd260e8=""
                      ></input>
                    </label>
                  </div>
                  <label data-v-acd260e8="">
                    {t("m.f029d9cb4a76")}
                    <input
                      required={true}
                      maxLength={500}
                      placeholder={t("m.440715526b90") as any}
                      {...__input(
                        windowForm.reason,
                        (__nextValue: any) => {
                          windowForm.reason = __nextValue;
                        },
                        "",
                        undefined,
                        [],
                      )}
                      data-v-acd260e8=""
                    ></input>
                  </label>
                  <button
                    type="submit"
                    disabled={saving.value as any}
                    className={__classes("alert-button primary")}
                    data-v-acd260e8=""
                  >
                    {t("m.2cb77284fedd")}
                  </button>
                </form>
              </section>
            ) : (
              <section className={__classes("alert-card")} data-v-acd260e8="">
                <div className={__classes("section-title")} data-v-acd260e8="">
                  <div data-v-acd260e8="">
                    <h2 data-v-acd260e8="">{t("m.0d09685cc503")}</h2>
                    <p data-v-acd260e8="">{t("m.da3e796d7d49")}</p>
                  </div>
                </div>
                <div className={__classes("table-wrap")} data-v-acd260e8="">
                  <table data-v-acd260e8="">
                    <thead data-v-acd260e8="">
                      <tr data-v-acd260e8="">
                        <th data-v-acd260e8="">{t("m.c35ffb529d5d")}</th>
                        <th data-v-acd260e8="">{t("m.0d12cbd6562b")}</th>
                        <th data-v-acd260e8="">{t("m.6320b4a8722a")}</th>
                        <th data-v-acd260e8="">{t("m.58a823852f8a")}</th>
                        <th data-v-acd260e8="">{"HTTP"}</th>
                        <th data-v-acd260e8="">{t("m.8b6ff498515b")}</th>
                        <th data-v-acd260e8="">{t("m.0bc1fb72ae1b")}</th>
                      </tr>
                    </thead>
                    <tbody data-v-acd260e8="">
                      <Fragment>
                        {__each(deliveries.value, (delivery) => (
                          <tr key={delivery.id as any} data-v-acd260e8="">
                            <td data-v-acd260e8="">{delivery.channelName}</td>
                            <td data-v-acd260e8="">
                              {delivery.testSend
                                ? t("m.6aa8f49cc992")
                                : delivery.kind.toUpperCase()}
                            </td>
                            <td data-v-acd260e8="">
                              <span
                                className={__classes("pill", delivery.status)}
                                data-v-acd260e8=""
                              >
                                {deliveryLabel(delivery.status)}
                              </span>
                            </td>
                            <td data-v-acd260e8="">
                              {delivery.attempts}
                              {" / "}
                              {delivery.maxAttempts}
                            </td>
                            <td data-v-acd260e8="">
                              {delivery.httpStatus ?? "—"}
                            </td>
                            <td data-v-acd260e8="">
                              {when(delivery.deliveredAt ?? delivery.createdAt)}
                            </td>
                            <td data-v-acd260e8="">
                              {display(delivery.lastError || "—")}
                            </td>
                          </tr>
                        ))}
                      </Fragment>
                    </tbody>
                  </table>
                </div>
                {!deliveries.value.length ? (
                  <p className={__classes("empty-state")} data-v-acd260e8="">
                    {t("m.7bf308b00b96")}
                  </p>
                ) : null}
              </section>
            )}
          </section>
        </>
      );
    },
    input,
    [],
    [],
    {},
    true,
  );
}
