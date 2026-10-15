import {
  api,
  operationRequest,
  type ServiceProbe,
  type ServiceProbePayload,
  type ServiceProbeRun,
} from "../api";
import { requestConfirm } from "../dialogs";
import { display, locale, t } from "../i18n";
import {
  afterMount,
  beforeDispose,
  cell,
  observable,
  watchState,
} from "../state";

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import "./NodeServiceProbes.css";
import UiSelect from "./UiSelect";
type Props = {
  nodeId: string;
  nodeName: string;
  nodeOnline: boolean;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function NodeServiceProbes(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const probes = cell<ServiceProbe[]>([]);
      const history = cell<Record<string, ServiceProbeRun[]>>({});
      const quality = cell<
        Record<
          string,
          {
            packetLossPercent?: number;
            jitterMs?: number;
            failurePercent?: number;
            meanLatencyMs?: number;
            p95LatencyMs?: number;
            unknown: number;
          }
        >
      >({});
      const visibleHistory = cell<Record<string, boolean>>({});
      const loading = cell(false);
      const saving = cell(false);
      const deleting = cell("");
      const error = cell("");
      const notice = cell("");
      const formVisible = cell(false);
      const editingID = cell("");
      const editingRevision = cell<number>();
      let refreshTimer: ReturnType<typeof setInterval> | undefined;
      const form = observable({
        name: "",
        kind: "http" as ServiceProbe["kind"],
        target: "",
        expectedHttpStatus: 200,
        intervalSeconds: 30,
        timeoutSeconds: 3,
        enabled: true,
      });
      const nodeProbes = () =>
        probes.value.filter((probe) => probe.nodeId === props.nodeId);
      function resetForm() {
        editingID.value = "";
        editingRevision.value = undefined;
        Object.assign(form, {
          name: "",
          kind: "http",
          target: "",
          expectedHttpStatus: 200,
          intervalSeconds: 30,
          timeoutSeconds: 3,
          enabled: true,
        });
      }
      async function refresh(silent = false) {
        if (!silent) loading.value = true;
        try {
          const response = await api.serviceProbes();
          probes.value = Array.isArray(response.probes) ? response.probes : [];
        } catch (reason) {
          if (!silent) error.value = message(reason);
        } finally {
          loading.value = false;
        }
      }
      function beginCreate() {
        error.value = "";
        notice.value = "";
        resetForm();
        formVisible.value = true;
      }
      function beginEdit(probe: ServiceProbe) {
        error.value = "";
        notice.value = "";
        editingID.value = probe.id;
        editingRevision.value = probe.revision;
        Object.assign(form, {
          name: probe.name,
          kind: probe.kind,
          target: probe.target,
          expectedHttpStatus: probe.expectedHttpStatus || 200,
          intervalSeconds: probe.intervalSeconds,
          timeoutSeconds: probe.timeoutSeconds,
          enabled: probe.enabled,
        });
        formVisible.value = true;
      }
      function closeForm() {
        formVisible.value = false;
        resetForm();
      }
      function payload(revision?: number): ServiceProbePayload {
        const value: ServiceProbePayload = {
          nodeId: props.nodeId,
          name: form.name.trim(),
          kind: form.kind,
          target: form.target.trim(),
          intervalSeconds: Number(form.intervalSeconds),
          timeoutSeconds: Number(form.timeoutSeconds),
          enabled: form.enabled,
        };
        if (["http", "https"].includes(form.kind))
          value.expectedHttpStatus = Number(form.expectedHttpStatus);
        if (revision !== undefined) value.revision = revision;
        return value;
      }
      async function save() {
        error.value = "";
        notice.value = "";
        saving.value = true;
        try {
          if (editingID.value)
            await api.updateServiceProbe(
              editingID.value,
              payload(editingRevision.value),
            );
          else await api.createServiceProbe(payload());
          closeForm();
          notice.value = t("m.c538fb4a37f3");
          await refresh(true);
        } catch (reason) {
          error.value = message(reason);
        } finally {
          saving.value = false;
        }
      }
      async function toggleEnabled(probe: ServiceProbe) {
        error.value = "";
        notice.value = "";
        try {
          await api.updateServiceProbe(probe.id, {
            nodeId: probe.nodeId,
            name: probe.name,
            kind: probe.kind,
            target: probe.target,
            expectedHttpStatus:
              probe.kind === "tcp" ? undefined : probe.expectedHttpStatus,
            intervalSeconds: probe.intervalSeconds,
            timeoutSeconds: probe.timeoutSeconds,
            enabled: !probe.enabled,
            revision: probe.revision,
          });
          notice.value = probe.enabled
            ? t("m.95f6288e13e9")
            : t("m.c5ad49ccd171");
          await refresh(true);
        } catch (reason) {
          error.value = message(reason);
        }
      }
      async function removeProbe(probe: ServiceProbe) {
        if (
          !(await requestConfirm(
            t("m.cc16da771452", {
              "0": probe.name,
            }),
          ))
        )
          return;
        deleting.value = probe.id;
        error.value = "";
        notice.value = "";
        try {
          await api.deleteServiceProbe(probe.id, probe.revision);
          probes.value = probes.value.filter((item) => item.id !== probe.id);
          notice.value = t("m.cbeed1058101");
        } catch (reason) {
          error.value = message(reason);
        } finally {
          deleting.value = "";
        }
      }
      async function toggleHistory(probe: ServiceProbe) {
        const visible = !visibleHistory.value[probe.id];
        visibleHistory.value = {
          ...visibleHistory.value,
          [probe.id]: visible,
        };
        if (!visible) return;
        try {
          const [result, stats] = await Promise.all([
            api.serviceProbeHistory(probe.id),
            operationRequest<{
              packetLossPercent?: number;
              jitterMs?: number;
              failurePercent?: number;
              meanLatencyMs?: number;
              p95LatencyMs?: number;
              unknown: number;
            }>("probes.quality", {
              params: {
                id: probe.id,
              },
            }),
          ]);
          quality.value[probe.id] = stats;
          history.value = {
            ...history.value,
            [probe.id]: result.runs,
          };
        } catch (reason) {
          error.value = message(reason);
        }
      }
      function statusLabel(probe: ServiceProbe): string {
        if (!probe.enabled) return t("m.a8c3698b5b8c");
        if (!props.nodeOnline || probe.status === "unknown")
          return t("m.4d8c1c5b4283");
        return probe.status === "healthy"
          ? t("m.296de0e31f8c")
          : t("m.428fb8bfeecf");
      }
      function errorLabel(code?: string): string {
        const labels: Record<string, string> = {
          node_offline: t("m.badb7a4e8552"),
          capability_unavailable: t("m.9f746c0f8354"),
          dispatch_unavailable: t("m.d740821f830c"),
          dispatch_timeout: t("m.71c8b847fdb7"),
          core_restarted: t("m.a0b5d6b0691a"),
          capacity: t("m.f96946fb2910"),
          timeout: t("m.ae5ead4d8907"),
          tls_verification: t("m.cb33e695a138"),
          http_status_mismatch: t("m.13ba30326078"),
          dns: t("m.3a577c9dabb1"),
          connection_failed: t("m.89c4766afc85"),
          cancelled: t("m.0294852f2c2c"),
        };
        return code ? labels[code] || t("m.c318cf5f0af4") : "";
      }
      function formatTime(value?: string): string {
        if (!value) return "—";
        const date = new Date(value);
        return Number.isNaN(date.getTime())
          ? "—"
          : date.toLocaleString(locale.value);
      }
      function message(reason: unknown): string {
        return reason instanceof Error && reason.message
          ? reason.message
          : t("m.ebf63ef228d2");
      }
      watchState(
        () => props.nodeId,
        () => {
          error.value = "";
          notice.value = "";
          formVisible.value = false;
          void refresh();
        },
      );
      watchState(
        () => form.kind,
        (kind) => {
          const old = form.target.replace(/^(https?|tcp|icmp):\/\//, "");
          if (kind === "icmp")
            form.target = `icmp://${old.split("/")[0]?.split(":")[0] ?? ""}`;
          else form.target = `${kind}://${old}`;
        },
      );
      afterMount(() => {
        void refresh();
        refreshTimer = setInterval(() => {
          void refresh(true);
        }, 5000);
      });
      beforeDispose(() => {
        if (refreshTimer !== undefined) clearInterval(refreshTimer);
      });
      return () => (
        <>
          <section
            aria-labelledby="service-probes-heading"
            data-testid="service-probes"
            className={__classes("service-probes")}
            data-v-5be80864=""
          >
            <header className={__classes("probe-heading")} data-v-5be80864="">
              <div data-v-5be80864="">
                <span className={__classes("eyebrow")} data-v-5be80864="">
                  {"SERVICE CHECKS · "}
                  {__props.nodeName}
                </span>
                <h2 id="service-probes-heading" data-v-5be80864="">
                  {t("m.3fefdc07fa14")}
                </h2>
                <p data-v-5be80864="">{t("m.46359b5cb550")}</p>
              </div>
              <button
                type="button"
                className={__classes("probe-primary")}
                onClick={(event: any) => {
                  __call(beginCreate, event);
                }}
                data-v-5be80864=""
              >
                {t("m.41e755cbc3c5")}
              </button>
            </header>
            {!__props.nodeOnline ? (
              <p
                role="status"
                className={__classes("probe-note")}
                data-v-5be80864=""
              >
                {t("m.12dc4fd08dbb")}
              </p>
            ) : null}
            {error.value ? (
              <div
                role="alert"
                className={__classes("probe-message probe-error")}
                data-v-5be80864=""
              >
                {display(error.value)}
              </div>
            ) : null}
            {notice.value ? (
              <div
                role="status"
                className={__classes("probe-message probe-notice")}
                data-v-5be80864=""
              >
                {display(notice.value)}
              </div>
            ) : null}
            {formVisible.value ? (
              <form
                data-testid="probe-form"
                className={__classes("probe-form")}
                onSubmit={(event: any) => {
                  event.preventDefault();
                  __call(save, event);
                }}
                data-v-5be80864=""
              >
                <h3 data-v-5be80864="">
                  {editingID.value ? t("m.5ffc6380c433") : t("m.4a7abfcfad36")}
                </h3>
                <div className={__classes("probe-fields")} data-v-5be80864="">
                  <label data-v-5be80864="">
                    <span data-v-5be80864="">{t("m.d44e9b3d3b31")}</span>
                    <input
                      maxLength={80}
                      required={true}
                      placeholder={t("m.bd0242098b8e") as any}
                      {...__input(
                        form.name,
                        (__nextValue: any) => {
                          form.name = __nextValue;
                        },
                        "",
                        undefined,
                        [],
                      )}
                      data-v-5be80864=""
                    ></input>
                  </label>
                  <label data-v-5be80864="">
                    <span data-v-5be80864="">{t("m.ab2f31f30acf")}</span>
                    <UiSelect
                      modelValue={form.kind}
                      onUpdateModelValue={(__nextValue: any) => {
                        form.kind = __nextValue;
                      }}
                      modelModifiers={{}}
                      data-v-5be80864=""
                    >
                      <option value="http" data-v-5be80864="">
                        {"HTTP"}
                      </option>
                      <option value="https" data-v-5be80864="">
                        {"HTTPS"}
                      </option>
                      <option value="tcp" data-v-5be80864="">
                        {"TCP"}
                      </option>
                      <option value="icmp" data-v-5be80864="">
                        {"ICMP"}
                      </option>
                    </UiSelect>
                  </label>
                  <label
                    className={__classes("probe-target")}
                    data-v-5be80864=""
                  >
                    <span data-v-5be80864="">{t("m.fb60c9043c7a")}</span>
                    <input
                      required={true}
                      maxLength={2048}
                      placeholder={
                        form.kind === "tcp"
                          ? "tcp://127.0.0.1:5432"
                          : (`${form.kind}://127.0.0.1:8080/health` as any)
                      }
                      {...__input(
                        form.target,
                        (__nextValue: any) => {
                          form.target = __nextValue;
                        },
                        "",
                        undefined,
                        [],
                      )}
                      data-v-5be80864=""
                    ></input>
                  </label>
                  {["http", "https"].includes(form.kind) ? (
                    <label data-v-5be80864="">
                      <span data-v-5be80864="">{t("m.3f63bb98cd18")}</span>
                      <input
                        type="number"
                        min="100"
                        max="599"
                        required={true}
                        {...__input(
                          form.expectedHttpStatus,
                          (__nextValue: any) => {
                            form.expectedHttpStatus = __nextValue;
                          },
                          "number",
                          undefined,
                          ["number"],
                        )}
                        data-v-5be80864=""
                      ></input>
                    </label>
                  ) : null}
                  <label data-v-5be80864="">
                    <span data-v-5be80864="">{t("m.455adcae032d")}</span>
                    <input
                      type="number"
                      min="10"
                      max="86400"
                      required={true}
                      {...__input(
                        form.intervalSeconds,
                        (__nextValue: any) => {
                          form.intervalSeconds = __nextValue;
                        },
                        "number",
                        undefined,
                        ["number"],
                      )}
                      data-v-5be80864=""
                    ></input>
                  </label>
                  <label data-v-5be80864="">
                    <span data-v-5be80864="">{t("m.e814e078437b")}</span>
                    <input
                      type="number"
                      min="1"
                      max="30"
                      required={true}
                      {...__input(
                        form.timeoutSeconds,
                        (__nextValue: any) => {
                          form.timeoutSeconds = __nextValue;
                        },
                        "number",
                        undefined,
                        ["number"],
                      )}
                      data-v-5be80864=""
                    ></input>
                  </label>
                </div>
                <label
                  className={__classes("probe-enabled")}
                  data-v-5be80864=""
                >
                  <input
                    type="checkbox"
                    {...__input(
                      form.enabled,
                      (__nextValue: any) => {
                        form.enabled = __nextValue;
                      },
                      "checkbox",
                      undefined,
                      [],
                    )}
                    data-v-5be80864=""
                  ></input>
                  <span data-v-5be80864="">{t("m.6cdc68ea4c5f")}</span>
                </label>
                <p className={__classes("probe-help")} data-v-5be80864="">
                  {t("m.898892992159")}
                </p>
                <div
                  className={__classes("probe-form-actions")}
                  data-v-5be80864=""
                >
                  <button
                    type="submit"
                    disabled={saving.value as any}
                    className={__classes("probe-primary")}
                    data-v-5be80864=""
                  >
                    {saving.value ? t("m.6bdb4435095e") : t("m.6e584e3d5ce6")}
                  </button>
                  <button
                    type="button"
                    disabled={saving.value as any}
                    className={__classes("probe-secondary")}
                    onClick={(event: any) => {
                      __call(closeForm, event);
                    }}
                    data-v-5be80864=""
                  >
                    {t("m.2cd0f3be8738")}
                  </button>
                </div>
              </form>
            ) : null}
            {loading.value && !probes.value.length ? (
              <p
                role="status"
                className={__classes("probe-empty")}
                data-v-5be80864=""
              >
                {t("m.e7985076d5fd")}
              </p>
            ) : nodeProbes().length === 0 ? (
              <p className={__classes("probe-empty")} data-v-5be80864="">
                {loading.value ? t("m.86b6d0d63062") : t("m.e0ad4994de04")}
              </p>
            ) : (
              <div className={__classes("probe-list")} data-v-5be80864="">
                <Fragment>
                  {__each(nodeProbes(), (probe) => (
                    <article
                      key={probe.id as any}
                      data-probe-id={probe.id as any}
                      className={__classes("probe-card")}
                      data-v-5be80864=""
                    >
                      <div
                        className={__classes("probe-card-heading")}
                        data-v-5be80864=""
                      >
                        <div
                          className={__classes("probe-copy")}
                          data-v-5be80864=""
                        >
                          <strong data-v-5be80864="">{probe.name}</strong>
                          <code data-v-5be80864="">
                            {probe.kind.toUpperCase()}
                            {" · "}
                            {probe.target}
                          </code>
                        </div>
                        <span
                          data-status={
                            !__props.nodeOnline
                              ? "unknown"
                              : probe.enabled
                                ? probe.status
                                : ("disabled" as any)
                          }
                          className={__classes("probe-status")}
                          data-v-5be80864=""
                        >
                          {statusLabel(probe)}
                        </span>
                      </div>
                      <div
                        className={__classes("probe-meta")}
                        data-v-5be80864=""
                      >
                        <span data-v-5be80864="">
                          {t("m.4f5df723d99d")}
                          {probe.intervalSeconds}
                          {t("m.9dcdc2b289b9")}
                        </span>
                        <span data-v-5be80864="">
                          {t("m.e512cf016f96")}
                          {probe.timeoutSeconds}
                          {t("m.9dcdc2b289b9")}
                        </span>
                        {["http", "https"].includes(probe.kind) ? (
                          <span data-v-5be80864="">
                            {t("m.ed1f74c64ac9")}
                            {probe.expectedHttpStatus}
                          </span>
                        ) : null}
                        <span data-v-5be80864="">
                          {t("m.28d75d9db816")}
                          {formatTime(probe.lastCheckedAt)}
                        </span>
                        {probe.lastErrorCode ? (
                          <span data-v-5be80864="">
                            {errorLabel(probe.lastErrorCode)}
                          </span>
                        ) : null}
                      </div>
                      <div
                        className={__classes("probe-actions")}
                        data-v-5be80864=""
                      >
                        <button
                          type="button"
                          onClick={(event: any) => {
                            toggleHistory(probe);
                          }}
                          data-v-5be80864=""
                        >
                          {visibleHistory.value[probe.id]
                            ? t("m.a9f4bab69211")
                            : t("m.a5783b941d7d")}
                        </button>
                        <button
                          type="button"
                          onClick={(event: any) => {
                            beginEdit(probe);
                          }}
                          data-v-5be80864=""
                        >
                          {t("m.051836569928")}
                        </button>
                        <button
                          type="button"
                          onClick={(event: any) => {
                            toggleEnabled(probe);
                          }}
                          data-v-5be80864=""
                        >
                          {probe.enabled
                            ? t("m.4e6fd0e28c55")
                            : t("m.f4f0ead1116b")}
                        </button>
                        <button
                          type="button"
                          disabled={deleting.value === (probe.id as any)}
                          className={__classes("probe-delete")}
                          onClick={(event: any) => {
                            removeProbe(probe);
                          }}
                          data-v-5be80864=""
                        >
                          {deleting.value === probe.id
                            ? t("m.4fd4f40094d4")
                            : t("m.2f9daa828907")}
                        </button>
                      </div>
                      {visibleHistory.value[probe.id] &&
                      quality.value[probe.id] ? (
                        <p
                          className={__classes("probe-meta")}
                          data-v-5be80864=""
                        >
                          {t("host.quality")}
                          {" · "}
                          {t("host.failure")}
                          {": "}
                          {quality.value[probe.id]?.failurePercent?.toFixed(
                            1,
                          ) ?? "—"}
                          {"% · "}
                          {t("host.packetLoss")}
                          {": "}
                          {quality.value[probe.id]?.packetLossPercent?.toFixed(
                            1,
                          ) ?? "—"}
                          {"% · "}
                          {t("host.jitter")}
                          {": "}
                          {quality.value[probe.id]?.jitterMs?.toFixed(1) ?? "—"}
                          {" ms · P95: "}
                          {quality.value[probe.id]?.p95LatencyMs ?? "—"}
                          {" ms · "}
                          {t("host.unknown")}
                          {": "}
                          {quality.value[probe.id]?.unknown}
                        </p>
                      ) : null}
                      {visibleHistory.value[probe.id] ? (
                        <ol
                          aria-label={
                            t("m.34392b76904a", {
                              "0": probe.name,
                            }) as any
                          }
                          className={__classes("probe-history")}
                          data-v-5be80864=""
                        >
                          <Fragment>
                            {__each(history.value[probe.id] || [], (run) => (
                              <li
                                key={run.runId as any}
                                data-status={run.status as any}
                                data-v-5be80864=""
                              >
                                <span data-v-5be80864="">
                                  {formatTime(run.checkedAt)}
                                </span>
                                <strong data-v-5be80864="">
                                  {run.status === "healthy"
                                    ? t("m.296de0e31f8c")
                                    : run.status === "unhealthy"
                                      ? t("m.428fb8bfeecf")
                                      : run.status === "pending"
                                        ? t("m.33037aadf043")
                                        : t("m.4d8c1c5b4283")}
                                </strong>
                                {run.httpStatus ? (
                                  <span data-v-5be80864="">
                                    {"HTTP "}
                                    {run.httpStatus}
                                  </span>
                                ) : null}
                                {run.latencyMs !== undefined ? (
                                  <span data-v-5be80864="">
                                    {run.latencyMs}
                                    {" ms"}
                                  </span>
                                ) : null}
                                {run.errorCode ? (
                                  <span data-v-5be80864="">
                                    {errorLabel(run.errorCode)}
                                  </span>
                                ) : null}
                              </li>
                            ))}
                          </Fragment>
                          {history.value[probe.id]?.length === 0 ? (
                            <li
                              className={__classes("probe-no-history")}
                              data-v-5be80864=""
                            >
                              {t("m.140d051bd612")}
                            </li>
                          ) : null}
                        </ol>
                      ) : null}
                    </article>
                  ))}
                </Fragment>
              </div>
            )}
          </section>
        </>
      );
    },
    input,
    ["nodeId", "nodeName", "nodeOnline"],
    [],
    {},
    true,
  );
}
