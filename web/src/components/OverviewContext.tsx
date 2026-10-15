import { ApiError, api, operationRequest, type AgentNode } from "../api";
import { display, locale, t } from "../i18n";
import type { MetricsView } from "../metrics-contract";
import {
  afterMount,
  afterRender,
  beforeDispose,
  cell,
  derived,
  watchState,
} from "../state";
import UiIcon from "./UiIcon";

import { Fragment } from "react";
import {
  __call,
  __classes,
  __each,
  __input,
  __style,
  useController,
} from "../state";
import "./OverviewContext.css";
type Props = {
  node?: AgentNode;
  metrics?: MetricsView;
  online?: boolean;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function OverviewContext(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const emit = __emit;
      interface CoreInfo {
        hostname: string;
        platform: string;
        platformVersion: string;
        kernelVersion: string;
        architecture: string;
        version: string;
        bootTime: number;
        uptime: number;
        environment: string;
        addresses: string[];
        sampledAt: string;
      }
      interface Memo {
        content: string;
        revision: number;
        updatedAt: string;
      }
      const fieldKeys = [
        "hostname",
        "os",
        "kernel",
        "arch",
        "address",
        "boot",
        "uptime",
        "version",
      ] as const;
      type FieldKey = (typeof fieldKeys)[number];
      const fieldLabels = {
        hostname: "overview.hostname",
        os: "overview.os",
        kernel: "overview.kernel",
        arch: "overview.arch",
        address: "overview.address",
        boot: "overview.boot",
        uptime: "overview.uptime",
        version: "overview.version",
      } as const;
      const presentationKey = "nodedance.overview.presentation";
      const visibleFields = cell<FieldKey[]>([...fieldKeys]),
        memoVisible = cell(true),
        sensitiveHidden = cell(false);
      try {
        const saved = JSON.parse(
          localStorage.getItem(presentationKey) ?? "null",
        );
        if (saved && typeof saved === "object") {
          if (Array.isArray(saved.fields))
            visibleFields.value = fieldKeys.filter((key) =>
              saved.fields.includes(key),
            );
          if (typeof saved.memoVisible === "boolean")
            memoVisible.value = saved.memoVisible;
          if (typeof saved.sensitiveHidden === "boolean")
            sensitiveHidden.value = saved.sensitiveHidden;
        }
      } catch {
        /* Browser-local display settings are optional. */
      }
      watchState(
        [visibleFields, memoVisible, sensitiveHidden],
        () => {
          try {
            localStorage.setItem(
              presentationKey,
              JSON.stringify({
                fields: visibleFields.value,
                memoVisible: memoVisible.value,
                sensitiveHidden: sensitiveHidden.value,
              }),
            );
          } catch {}
        },
        {
          deep: true,
        },
      );
      const core = cell<CoreInfo>(),
        coreError = cell(""),
        coreBusy = cell(false),
        refreshBusy = cell(false);
      const memo = cell<Memo>(),
        draft = cell(""),
        editing = cell(false),
        saving = cell(false),
        memoError = cell(""),
        conflict = cell(false);
      const hostOpen = cell(false),
        memoOpen = cell(false),
        copyStatus = cell(""),
        refreshError = cell("");
      const settingsDialog = cell<HTMLDialogElement>(),
        settingsButton = cell<HTMLButtonElement>(),
        memoInput = cell<HTMLTextAreaElement>();
      let query: MediaQueryList | undefined,
        timer: ReturnType<typeof setInterval> | undefined,
        disposed = false;
      const resize = () => {
        hostOpen.value = query?.matches ?? false;
        memoOpen.value = (query?.matches ?? false) || editing.value;
      };
      async function loadCore() {
        if (coreBusy.value) return;
        coreBusy.value = true;
        try {
          const value = await operationRequest<CoreInfo>("dashboard.core-info");
          if (!disposed) {
            core.value = value;
            coreError.value = "";
          }
        } catch (e) {
          if (!disposed)
            coreError.value = e instanceof Error ? e.message : String(e);
        } finally {
          coreBusy.value = false;
        }
      }
      async function refreshInfo() {
        if (refreshBusy.value || coreBusy.value) return;
        refreshBusy.value = true;
        refreshError.value = "";
        copyStatus.value = "";
        const id = props.node?.nodeId;
        try {
          if (id) emit("metrics", await api.nodeMetrics(id));
          else await loadCore();
        } catch (e) {
          if (!disposed && props.node?.nodeId === id)
            refreshError.value = e instanceof Error ? e.message : String(e);
        } finally {
          refreshBusy.value = false;
        }
      }
      async function loadMemo() {
        try {
          const value = await operationRequest<Memo>("dashboard.memo");
          if (!disposed) {
            memo.value = value;
            draft.value = value.content;
            memoError.value = "";
            conflict.value = false;
          }
        } catch (e) {
          if (!disposed)
            memoError.value = e instanceof Error ? e.message : String(e);
        }
      }
      async function startMemoEdit() {
        memoOpen.value = true;
        editing.value = true;
        await afterRender();
        memoInput.value?.focus();
      }
      function cancelMemoEdit() {
        draft.value = memo.value?.content ?? "";
        editing.value = false;
        memoError.value = "";
        conflict.value = false;
      }
      async function saveMemo() {
        if (!memo.value || saving.value) return;
        saving.value = true;
        memoError.value = "";
        try {
          const value = await operationRequest<Memo>(
            "dashboard.save-memo",
            {},
            {
              body: JSON.stringify({
                content: draft.value,
                revision: memo.value.revision,
              }),
            },
          );
          if (!disposed) {
            memo.value = value;
            draft.value = value.content;
            editing.value = false;
            conflict.value = false;
          }
        } catch (e) {
          if (!disposed) {
            conflict.value = e instanceof ApiError && e.status === 409;
            memoError.value = conflict.value
              ? t("overview.memoConflict")
              : e instanceof Error
                ? e.message
                : String(e);
          }
        } finally {
          saving.value = false;
        }
      }
      function duration(value?: number) {
        if (value == null) return "—";
        const days = Math.floor(value / 86400),
          hours = Math.floor((value % 86400) / 3600);
        return `${days} ${t("overview.days")} ${hours} ${t("overview.hours")}`;
      }
      function date(seconds?: number) {
        return seconds
          ? new Date(seconds * 1000).toLocaleString(locale.value)
          : "—";
      }
      const title = derived(() =>
        props.node
          ? sensitiveHidden.value
            ? "Agent"
            : props.node.displayName
          : "Core",
      );
      const allRows = derived(() => {
        const m = props.metrics?.metrics,
          info = core.value;
        const get = (value: unknown) =>
          value == null || value === "" ? "—" : String(value);
        const values = props.node
          ? [
              get(m?.system.hostname.value),
              [m?.system.platform.value, m?.system.platformVersion.value]
                .filter(Boolean)
                .join(" ") || "—",
              get(m?.system.kernelVersion.value),
              get(m?.system.architecture.value),
              m?.host?.addresses.value?.map((a) => a.address).join(" · ") ||
                "—",
              date(m?.uptime.value?.bootTimeUnix),
              duration(m?.uptime.value?.seconds),
              props.node.agentVersion || "—",
            ]
          : [
              get(info?.hostname),
              [info?.platform, info?.platformVersion]
                .filter(Boolean)
                .join(" ") || "—",
              get(info?.kernelVersion),
              get(info?.architecture),
              info?.addresses?.join(" · ") || "—",
              date(info?.bootTime),
              duration(info?.uptime),
              get(info?.version),
            ];
        return fieldKeys.map((key, index) => ({
          key,
          label: t(fieldLabels[key]),
          value:
            sensitiveHidden.value && (key === "hostname" || key === "address")
              ? "••••••"
              : values[index]!,
        }));
      });
      const rows = derived(() =>
        allRows.value.filter((row) => visibleFields.value.includes(row.key)),
      );
      async function copyInfo() {
        const text = [
          title.value,
          ...rows.value.map((row) => `${row.label}: ${row.value}`),
        ].join("\n");
        copyStatus.value = "";
        try {
          let copied = false;
          if (navigator.clipboard) {
            try {
              await navigator.clipboard.writeText(text);
              copied = true;
            } catch {}
          }
          if (!copied) {
            const active = document.activeElement as HTMLElement | null,
              input = document.createElement("textarea");
            input.value = text;
            input.style.cssText = "position:fixed;left:-9999px;top:0";
            document.body.append(input);
            try {
              input.select();
              copied = document.execCommand("copy");
            } finally {
              input.remove();
              active?.focus({
                preventScroll: true,
              });
            }
          }
          copyStatus.value = t(
            copied ? "overview.copied" : "overview.copyFailed",
          );
        } catch {
          copyStatus.value = t("overview.copyFailed");
        }
      }
      function openSettings() {
        settingsDialog.value?.showModal();
      }
      function closeSettings() {
        settingsDialog.value?.close();
        settingsButton.value?.focus({
          preventScroll: true,
        });
      }
      watchState(
        () => props.node?.nodeId,
        () => {
          copyStatus.value = "";
          refreshError.value = "";
        },
      );
      afterMount(() => {
        query = matchMedia("(min-width:1101px)");
        resize();
        query.addEventListener("change", resize);
        void loadCore();
        void loadMemo();
        timer = setInterval(() => {
          if (!props.node) void loadCore();
        }, 30000);
      });
      beforeDispose(() => {
        disposed = true;
        query?.removeEventListener("change", resize);
        if (timer) clearInterval(timer);
      });
      return () => (
        <>
          <aside
            aria-label={t("overview.context") as any}
            className={__classes("overview-context")}
            data-v-2aa82488=""
          >
            <section
              data-testid="overview-host-info"
              className={__classes("context-section")}
              data-v-2aa82488=""
            >
              <header
                className={__classes("context-heading")}
                data-v-2aa82488=""
              >
                <h2 data-v-2aa82488="">
                  <button
                    type="button"
                    aria-expanded={hostOpen.value as any}
                    className={__classes("section-toggle")}
                    onClick={(event: any) => {
                      hostOpen.value = !hostOpen.value;
                    }}
                    data-v-2aa82488=""
                  >
                    {t("overview.hostInfo")}
                  </button>
                </h2>
                <div
                  aria-label={t("overview.hostActions") as any}
                  className={__classes("header-actions")}
                  data-v-2aa82488=""
                >
                  <button
                    ref={__dom(settingsButton)}
                    type="button"
                    aria-label={t("overview.displaySettings") as any}
                    title={t("overview.displaySettings") as any}
                    onClick={(event: any) => {
                      __call(openSettings, event);
                    }}
                    data-v-2aa82488=""
                  >
                    <UiIcon name="cog" data-v-2aa82488=""></UiIcon>
                  </button>
                  <button
                    type="button"
                    disabled={coreBusy.value || (refreshBusy.value as any)}
                    aria-label={t("overview.refresh") as any}
                    title={t("overview.refresh") as any}
                    onClick={(event: any) => {
                      __call(refreshInfo, event);
                    }}
                    data-v-2aa82488=""
                  >
                    <UiIcon name="refresh" data-v-2aa82488=""></UiIcon>
                  </button>
                  <button
                    type="button"
                    aria-label={
                      t(
                        sensitiveHidden.value
                          ? "overview.showSensitive"
                          : "overview.hideSensitive",
                      ) as any
                    }
                    title={
                      t(
                        sensitiveHidden.value
                          ? "overview.showSensitive"
                          : "overview.hideSensitive",
                      ) as any
                    }
                    aria-pressed={sensitiveHidden.value as any}
                    onClick={(event: any) => {
                      sensitiveHidden.value = !sensitiveHidden.value;
                      copyStatus.value = "";
                    }}
                    data-v-2aa82488=""
                  >
                    <UiIcon
                      name={sensitiveHidden.value ? "eyeOff" : ("eye" as any)}
                      data-v-2aa82488=""
                    ></UiIcon>
                  </button>
                  <button
                    type="button"
                    disabled={
                      !rows.value.length ||
                      (!__props.node && !core.value) ||
                      (!!__props.node && (!__props.metrics as any))
                    }
                    aria-label={t("overview.copyInfo") as any}
                    title={t("overview.copyInfo") as any}
                    onClick={(event: any) => {
                      __call(copyInfo, event);
                    }}
                    data-v-2aa82488=""
                  >
                    <UiIcon name="copy" data-v-2aa82488=""></UiIcon>
                  </button>
                </div>
              </header>
              <div
                className={__classes("context-body")}
                style={__style({
                  display: hostOpen.value ? undefined : "none",
                })}
                data-v-2aa82488=""
              >
                <div className={__classes("context-target")} data-v-2aa82488="">
                  {__props.node ? (
                    <span title={title.value as any} data-v-2aa82488="">
                      {title.value}
                    </span>
                  ) : null}
                  <div
                    className={__classes("context-actions")}
                    data-v-2aa82488=""
                  >
                    <button
                      type="button"
                      aria-pressed={!__props.node as any}
                      onClick={(event: any) => {
                        emit("core");
                      }}
                      data-v-2aa82488=""
                    >
                      {"Core"}
                    </button>
                    {__props.node ? (
                      <span
                        className={__classes("context-status")}
                        data-v-2aa82488=""
                      >
                        {t(
                          __props.online
                            ? "overview.online"
                            : "overview.lastKnown",
                        )}
                      </span>
                    ) : null}
                  </div>
                </div>
                {!__props.node && core.value?.environment === "container" ? (
                  <p className={__classes("context-note")} data-v-2aa82488="">
                    {t("overview.containerInfo")}
                  </p>
                ) : null}
                {__props.node && !__props.metrics ? (
                  <p className={__classes("context-note")} data-v-2aa82488="">
                    {t("overview.noData")}
                  </p>
                ) : null}
                <dl data-v-2aa82488="">
                  <Fragment>
                    {__each(rows.value, (row) => (
                      <Fragment key={row.key as any}>
                        <dt data-v-2aa82488="">{row.label}</dt>
                        <dd data-field={row.key as any} data-v-2aa82488="">
                          {row.value}
                        </dd>
                      </Fragment>
                    ))}
                  </Fragment>
                </dl>
                {!rows.value.length ? (
                  <p className={__classes("context-note")} data-v-2aa82488="">
                    {t("overview.noFields")}
                  </p>
                ) : null}
                {(!__props.node && coreError.value) || refreshError.value ? (
                  <p
                    role="alert"
                    className={__classes("context-error")}
                    data-v-2aa82488=""
                  >
                    {display(refreshError.value || coreError.value)}
                  </p>
                ) : null}
                {copyStatus.value ? (
                  <p
                    role="status"
                    className={__classes("context-note copy-status")}
                    data-v-2aa82488=""
                  >
                    {copyStatus.value}
                  </p>
                ) : null}
              </div>
            </section>
            <section
              data-testid="overview-memo"
              className={__classes("context-section memo-section")}
              style={__style({
                display:
                  memoVisible.value || editing.value ? undefined : "none",
              })}
              data-v-2aa82488=""
            >
              <header
                className={__classes("context-heading")}
                data-v-2aa82488=""
              >
                <h2 data-v-2aa82488="">
                  <button
                    type="button"
                    aria-expanded={memoOpen.value as any}
                    className={__classes("section-toggle")}
                    onClick={(event: any) => {
                      memoOpen.value = !memoOpen.value;
                    }}
                    data-v-2aa82488=""
                  >
                    {t("overview.memo")}
                  </button>
                </h2>
                <div
                  aria-label={t("overview.memoActions") as any}
                  className={__classes("header-actions")}
                  data-v-2aa82488=""
                >
                  {!editing.value ? (
                    <button
                      type="button"
                      disabled={!memo.value as any}
                      aria-label={t("overview.edit") as any}
                      title={t("overview.edit") as any}
                      onClick={(event: any) => {
                        __call(startMemoEdit, event);
                      }}
                      data-v-2aa82488=""
                    >
                      <UiIcon name="edit" data-v-2aa82488=""></UiIcon>
                    </button>
                  ) : (
                    <Fragment>
                      <button
                        type="button"
                        disabled={saving.value as any}
                        aria-label={t("overview.save") as any}
                        title={t("overview.save") as any}
                        onClick={(event: any) => {
                          __call(saveMemo, event);
                        }}
                        data-v-2aa82488=""
                      >
                        <UiIcon name="check" data-v-2aa82488=""></UiIcon>
                      </button>
                      <button
                        type="button"
                        disabled={saving.value as any}
                        aria-label={t("overview.cancel") as any}
                        title={t("overview.cancel") as any}
                        onClick={(event: any) => {
                          __call(cancelMemoEdit, event);
                        }}
                        data-v-2aa82488=""
                      >
                        <UiIcon name="close" data-v-2aa82488=""></UiIcon>
                      </button>
                    </Fragment>
                  )}
                </div>
              </header>
              <div
                className={__classes("context-body")}
                style={__style({
                  display: memoOpen.value ? undefined : "none",
                })}
                data-v-2aa82488=""
              >
                <p className={__classes("context-note")} data-v-2aa82488="">
                  {t("overview.memoShared")}
                </p>
                <textarea
                  ref={__dom(memoInput)}
                  aria-label={t("overview.memo") as any}
                  readOnly={!editing.value as any}
                  disabled={saving.value || (!memo.value as any)}
                  rows={7}
                  placeholder={t("overview.memoPlaceholder") as any}
                  className={__classes("memo-input")}
                  {...__input(
                    draft.value,
                    (__nextValue: any) => {
                      draft.value = __nextValue;
                    },
                    "",
                    undefined,
                    [],
                  )}
                  data-v-2aa82488=""
                ></textarea>
                {conflict.value || !memo.value ? (
                  <div className={__classes("memo-actions")} data-v-2aa82488="">
                    <button
                      type="button"
                      disabled={saving.value as any}
                      onClick={(event: any) => {
                        __call(loadMemo, event);
                      }}
                      data-v-2aa82488=""
                    >
                      {t("overview.reloadMemo")}
                    </button>
                  </div>
                ) : null}
                {memoError.value ? (
                  <p
                    role="alert"
                    className={__classes("context-error")}
                    data-v-2aa82488=""
                  >
                    {display(memoError.value)}
                  </p>
                ) : null}
              </div>
            </section>
            <dialog
              ref={__dom(settingsDialog)}
              aria-labelledby="overview-settings-title"
              className={__classes("context-settings")}
              onCancel={(event: any) => {
                event.preventDefault();
                __call(closeSettings, event);
              }}
              onClick={(event: any) => {
                __call((event) => {
                  if (event.target === settingsDialog.value) closeSettings();
                }, event);
              }}
              data-v-2aa82488=""
            >
              <header data-v-2aa82488="">
                <h2 id="overview-settings-title" data-v-2aa82488="">
                  {t("overview.displaySettings")}
                </h2>
                <button
                  type="button"
                  aria-label={t("overview.close") as any}
                  onClick={(event: any) => {
                    __call(closeSettings, event);
                  }}
                  data-v-2aa82488=""
                >
                  <UiIcon name="close" data-v-2aa82488=""></UiIcon>
                </button>
              </header>
              <p data-v-2aa82488="">{t("overview.browserLocal")}</p>
              <label
                className={__classes("settings-option")}
                data-v-2aa82488=""
              >
                <input
                  type="checkbox"
                  {...__input(
                    memoVisible.value,
                    (__nextValue: any) => {
                      memoVisible.value = __nextValue;
                    },
                    "checkbox",
                    undefined,
                    [],
                  )}
                  data-v-2aa82488=""
                ></input>
                {t("overview.showMemo")}
              </label>
              <fieldset data-v-2aa82488="">
                <legend data-v-2aa82488="">{t("overview.infoFields")}</legend>
                <Fragment>
                  {__each(fieldKeys, (key) => (
                    <label
                      key={key as any}
                      className={__classes("settings-option")}
                      data-v-2aa82488=""
                    >
                      <input
                        value={key as any}
                        type="checkbox"
                        {...__input(
                          visibleFields.value,
                          (__nextValue: any) => {
                            visibleFields.value = __nextValue;
                          },
                          "checkbox",
                          key,
                          [],
                        )}
                        data-v-2aa82488=""
                      ></input>
                      {t(fieldLabels[key])}
                    </label>
                  ))}
                </Fragment>
              </fieldset>
              <footer data-v-2aa82488="">
                <button
                  type="button"
                  onClick={(event: any) => {
                    visibleFields.value = [...fieldKeys];
                    memoVisible.value = true;
                  }}
                  data-v-2aa82488=""
                >
                  {t("overview.resetDisplay")}
                </button>
                <button
                  type="button"
                  onClick={(event: any) => {
                    __call(closeSettings, event);
                  }}
                  data-v-2aa82488=""
                >
                  {t("overview.done")}
                </button>
              </footer>
            </dialog>
          </aside>
        </>
      );
    },
    input,
    ["node", "metrics", "online"],
    [],
    {},
    true,
  );
}
