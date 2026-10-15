import {
  api,
  type ContainerRebuildPlan,
  type ContainerTask,
  type DockerContainer,
  type RebuildPortBinding,
  type RebuildSpec,
} from "../api";
import { requestPrompt } from "../dialogs";
import { display, t } from "../i18n";
import { beforeDispose, cell, derived, watchState } from "../state";

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import "./ContainerRebuildWizard.css";
import UiSelect from "./UiSelect";
type Props = {
  nodeId: string;
  container: DockerContainer;
  stale: boolean;
  tasks: ContainerTask[];
} & { children?: import("react").ReactNode; [key: string]: any };
export default function ContainerRebuildWizard(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const opened = cell(false);
      const mode = cell<"keep" | "replace" | "clear">("keep");
      const bindings = cell<RebuildPortBinding[]>([]);
      const plan = cell<ContainerRebuildPlan>();
      const task = cell<ContainerTask>();
      const acknowledged = cell(false);
      const loadingPlan = cell(false);
      const submitting = cell(false);
      const cleaning = cell(false);
      const error = cell("");
      let pollTimer: ReturnType<typeof setTimeout> | undefined;
      let generation = 0;
      const taskMessage = derived(() => {
        if (!task.value) return "";
        if (task.value.status === "queued") return t("m.ab502a2e4167");
        if (task.value.status === "running")
          return t("m.dc8605450aaa", {
            "0": task.value.progress.phase || t("m.0a68e6a99abe"),
          });
        if (task.value.status === "succeeded")
          return task.value.result.observedState ===
            "rollback_container_removed_snapshot_retained"
            ? t("m.efcd16dd368b")
            : t("m.3c2ee110bf07");
        if (task.value.status === "unknown") return t("m.5013ec7ae306");
        if (task.value.status === "timed_out") return t("m.3e78f8c0e01b");
        if (task.value.status === "canceled") return t("m.d7df27ca8d31");
        if ((task.value.result.observedState ?? "").startsWith("restored:")) {
          return t("m.c4433033c3ce", {
            "0": (task.value.result.observedState ?? "").slice(
              "restored:".length,
            ),
          });
        }
        return t("m.bab93f6cf8a1", {
          "0":
            task.value.result.resourceRevision ||
            task.value.result.code ||
            t("m.fb2425b0c8e1"),
        });
      });
      const previousTask = derived(() =>
        props.tasks.find(
          (item) =>
            item.action === "rebuild" &&
            item.result.resourceRevision === props.container.id &&
            (item.status === "succeeded" ||
              (item.status === "failed" &&
                (item.result.observedState ?? "").startsWith("restored:"))),
        ),
      );
      const cleanupDone = derived(
        () =>
          props.tasks.some(
            (item) =>
              item.action === "rebuild_cleanup" &&
              item.targetId === props.container.id &&
              item.status === "succeeded",
          ) ||
          (task.value?.action === "rebuild_cleanup" &&
            task.value.targetId === props.container.id &&
            task.value.status === "succeeded"),
      );
      const cleanupTask = derived(() =>
        task.value?.action === "rebuild" &&
        task.value.result.resourceRevision === props.container.id &&
        (task.value.status === "succeeded" ||
          (task.value.status === "failed" &&
            (task.value.result.observedState ?? "").startsWith("restored:")))
          ? task.value
          : previousTask.value,
      );
      const cleanupRestoredFailure = derived(
        () =>
          cleanupTask.value?.status === "failed" &&
          (cleanupTask.value.result.observedState ?? "").startsWith(
            "restored:",
          ),
      );
      watchState(
        () => task.value?.taskId,
        () => {
          if (pollTimer) clearTimeout(pollTimer);
          if (
            task.value &&
            ["queued", "running", "unknown"].includes(task.value.status)
          )
            schedulePoll(task.value.taskId, generation);
        },
      );
      function taskTerminal(value: ContainerTask): boolean {
        return ["succeeded", "failed", "timed_out", "canceled"].includes(
          value.status,
        );
      }
      function specFromForm(): RebuildSpec {
        if (mode.value === "clear")
          return {
            clearPortBindings: true,
          };
        if (mode.value === "keep") return {};
        return {
          portBindings: bindings.value.map((binding) => ({
            containerPort: binding.containerPort.trim(),
            hostIp: binding.hostIp?.trim() || undefined,
            hostPort: binding.hostPort?.trim() || undefined,
          })),
        };
      }
      function addBinding() {
        mode.value = "replace";
        bindings.value = [
          ...bindings.value,
          {
            containerPort: "",
            hostIp: "",
            hostPort: "",
          },
        ];
        plan.value = undefined;
      }
      function removeBinding(index: number) {
        mode.value = "replace";
        bindings.value = bindings.value.filter((_, row) => row !== index);
        plan.value = undefined;
      }
      async function openWizard() {
        if (
          props.stale ||
          props.container.stale ||
          props.container.compose ||
          loadingPlan.value
        )
          return;
        opened.value = true;
        mode.value = "keep";
        bindings.value = [];
        plan.value = undefined;
        task.value = undefined;
        acknowledged.value = false;
        error.value = "";
        await preview();
      }
      async function preview() {
        loadingPlan.value = true;
        error.value = "";
        try {
          const result = await api.planContainerRebuild(
            props.nodeId,
            props.container.id,
            specFromForm(),
          );
          plan.value = result;
          acknowledged.value = false;
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : t("m.8dc5598b2ee8");
          plan.value = undefined;
        } finally {
          loadingPlan.value = false;
        }
      }
      async function waitForTask(taskId: string, requestGeneration: number) {
        try {
          const updated = await api.nodeTask(props.nodeId, taskId);
          if (requestGeneration !== generation) return;
          task.value = updated;
          if (!taskTerminal(updated) && updated.status !== "unknown")
            schedulePoll(taskId, requestGeneration);
        } catch (reason) {
          if (requestGeneration === generation)
            error.value =
              reason instanceof Error ? reason.message : t("m.b22473f5734c");
        }
      }
      function schedulePoll(taskId: string, requestGeneration: number) {
        if (pollTimer) clearTimeout(pollTimer);
        pollTimer = setTimeout(() => {
          void waitForTask(taskId, requestGeneration);
        }, 1200);
      }
      async function submitRebuild() {
        if (
          !plan.value ||
          !acknowledged.value ||
          submitting.value ||
          props.stale ||
          props.container.stale
        )
          return;
        const confirmation = await requestPrompt(
          t("m.751a2318fd6e", {
            "0": props.container.id,
          }),
        );
        if (confirmation === null) return;
        if (confirmation.trim() !== props.container.id) {
          error.value = t("m.25b38b35f7a5");
          return;
        }
        submitting.value = true;
        error.value = "";
        const requestGeneration = ++generation;
        try {
          const accepted = await api.createContainerTask(
            props.nodeId,
            props.container.id,
            {
              action: "rebuild",
              rebuild: specFromForm(),
            },
          );
          task.value = await api.nodeTask(props.nodeId, accepted.taskId);
          if (!taskTerminal(task.value) && task.value.status !== "unknown")
            schedulePoll(accepted.taskId, requestGeneration);
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : t("m.8beb6052b0fc");
        } finally {
          submitting.value = false;
        }
      }
      async function cleanupRollbackResources() {
        const rollback = cleanupTask.value;
        if (
          !rollback ||
          cleanupDone.value ||
          cleaning.value ||
          props.stale ||
          props.container.stale
        )
          return;
        const cleanupDescription = cleanupRestoredFailure.value
          ? t("m.91f8944e8aa7")
          : t("m.fd78403a0172");
        const confirmation = await requestPrompt(
          t("m.c9dab8fa99dd", {
            "0": cleanupDescription,
            "1": props.container.id,
          }),
        );
        if (confirmation === null) return;
        if (confirmation.trim() !== props.container.id) {
          error.value = t("m.af34e98d4a24");
          return;
        }
        cleaning.value = true;
        error.value = "";
        try {
          const accepted = await api.createContainerTask(
            props.nodeId,
            props.container.id,
            {
              action: "rebuild_cleanup",
              rebuild: {
                cleanupTaskId: rollback.taskId,
              },
              confirmationId: props.container.id,
            },
          );
          const updated = await api.nodeTask(props.nodeId, accepted.taskId);
          task.value = updated;
          if (updated.status === "queued" || updated.status === "running")
            schedulePoll(accepted.taskId, ++generation);
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : t("m.ffeb5f93c877");
        } finally {
          cleaning.value = false;
        }
      }
      beforeDispose(() => {
        generation++;
        if (pollTimer) clearTimeout(pollTimer);
      });
      return () => (
        <>
          <div className={__classes("rebuild-entry")} data-v-a7cd5075="">
            <button
              type="button"
              disabled={
                __props.stale ||
                __props.container.stale ||
                (Boolean(__props.container.compose) as any)
              }
              title={
                __props.container.compose
                  ? t("m.0c6674d92b52")
                  : (t("m.f754d1c47f3e") as any)
              }
              className={__classes("container-action")}
              onClick={(event: any) => {
                __call(openWizard, event);
              }}
              data-v-a7cd5075=""
            >
              {t("m.e6953475e88d")}
            </button>
            {task.value ? (
              <span
                data-testid="rebuild-task-status"
                data-status={task.value.status as any}
                role="status"
                className={__classes("container-task-status")}
                data-v-a7cd5075=""
              >
                {display(taskMessage.value)}
              </span>
            ) : null}
            {cleanupTask.value && !cleanupDone.value ? (
              <button
                type="button"
                disabled={
                  __props.stale ||
                  __props.container.stale ||
                  (cleaning.value as any)
                }
                className={__classes("container-action")}
                onClick={(event: any) => {
                  __call(cleanupRollbackResources, event);
                }}
                data-v-a7cd5075=""
              >
                {cleaning.value
                  ? t("m.248e8cfc2116")
                  : cleanupRestoredFailure.value
                    ? t("m.606302a043c7")
                    : t("m.4e188c5690ca")}
              </button>
            ) : null}
            {cleanupDone.value ? (
              <span
                role="status"
                className={__classes("container-task-status")}
                data-v-a7cd5075=""
              >
                {t("m.d36c4aa4610f")}
              </span>
            ) : null}
            {opened.value ? (
              <section
                role="dialog"
                aria-modal="false"
                aria-labelledby="rebuild-title"
                data-testid="container-rebuild-wizard"
                className={__classes("rebuild-dialog")}
                data-v-a7cd5075=""
              >
                <header
                  className={__classes("rebuild-heading")}
                  data-v-a7cd5075=""
                >
                  <div data-v-a7cd5075="">
                    <span className={__classes("eyebrow")} data-v-a7cd5075="">
                      {t("label.08671c7919d1")}
                    </span>
                    <h3 id="rebuild-title" data-v-a7cd5075="">
                      {t("m.4b8d9d4f3850")}
                      {__props.container.name ||
                        __props.container.id.slice(0, 12)}
                    </h3>
                  </div>
                  <button
                    type="button"
                    disabled={submitting.value as any}
                    className={__classes("container-action")}
                    onClick={(event: any) => {
                      opened.value = false;
                    }}
                    data-v-a7cd5075=""
                  >
                    {t("m.3fd47edce45b")}
                  </button>
                </header>
                <p className={__classes("rebuild-intro")} data-v-a7cd5075="">
                  {t("m.11edce34ba7c")}
                </p>
                <label className={__classes("rebuild-mode")} data-v-a7cd5075="">
                  {t("m.54644705e9c6")}
                  <UiSelect
                    disabled={submitting.value as any}
                    onChange={(event: any) => {
                      plan.value = undefined;
                    }}
                    modelValue={mode.value}
                    onUpdateModelValue={(__nextValue: any) => {
                      mode.value = __nextValue;
                    }}
                    modelModifiers={{}}
                    data-v-a7cd5075=""
                  >
                    <option value="keep" data-v-a7cd5075="">
                      {t("m.841bbfd5aeae")}
                    </option>
                    <option value="replace" data-v-a7cd5075="">
                      {t("m.1f329f154394")}
                    </option>
                    <option value="clear" data-v-a7cd5075="">
                      {t("m.7c32fb37345f")}
                    </option>
                  </UiSelect>
                </label>
                {mode.value === "replace" ? (
                  <div
                    className={__classes("rebuild-bindings")}
                    data-v-a7cd5075=""
                  >
                    <Fragment>
                      {__each(bindings.value, (binding, index) => (
                        <div
                          key={index as any}
                          data-testid={`rebuild-binding-${index}` as any}
                          className={__classes("rebuild-binding")}
                          data-v-a7cd5075=""
                        >
                          <label data-v-a7cd5075="">
                            {t("m.9c9980ac48fe")}
                            <input
                              aria-label={
                                t("m.99af036931c4", {
                                  "0": index + 1,
                                }) as any
                              }
                              placeholder="80/tcp"
                              onInput={(event: any) => {
                                plan.value = undefined;
                              }}
                              {...__input(
                                binding.containerPort,
                                (__nextValue: any) => {
                                  binding.containerPort = __nextValue;
                                },
                                "",
                                undefined,
                                [],
                              )}
                              data-v-a7cd5075=""
                            ></input>
                          </label>
                          <label data-v-a7cd5075="">
                            {t("m.7782666b6ce5")}
                            <input
                              aria-label={
                                t("m.c8c0858dd7d7", {
                                  "0": index + 1,
                                }) as any
                              }
                              placeholder={t("m.50f9c5e2f0a1") as any}
                              onInput={(event: any) => {
                                plan.value = undefined;
                              }}
                              {...__input(
                                binding.hostIp,
                                (__nextValue: any) => {
                                  binding.hostIp = __nextValue;
                                },
                                "",
                                undefined,
                                [],
                              )}
                              data-v-a7cd5075=""
                            ></input>
                          </label>
                          <label data-v-a7cd5075="">
                            {t("m.5c474b41c45e")}
                            <input
                              aria-label={
                                t("m.c6e031cb1c5f", {
                                  "0": index + 1,
                                }) as any
                              }
                              inputMode="numeric"
                              placeholder="18080"
                              onInput={(event: any) => {
                                plan.value = undefined;
                              }}
                              {...__input(
                                binding.hostPort,
                                (__nextValue: any) => {
                                  binding.hostPort = __nextValue;
                                },
                                "",
                                undefined,
                                [],
                              )}
                              data-v-a7cd5075=""
                            ></input>
                          </label>
                          <button
                            type="button"
                            aria-label={
                              t("m.6ccdc5171a37", {
                                "0": index + 1,
                              }) as any
                            }
                            className={__classes("container-action")}
                            onClick={(event: any) => {
                              removeBinding(index);
                            }}
                            data-v-a7cd5075=""
                          >
                            {t("m.6135d4159e89")}
                          </button>
                        </div>
                      ))}
                    </Fragment>
                    <button
                      type="button"
                      className={__classes("container-action")}
                      onClick={(event: any) => {
                        __call(addBinding, event);
                      }}
                      data-v-a7cd5075=""
                    >
                      {t("m.3681abca77fd")}
                    </button>
                  </div>
                ) : null}
                <button
                  type="button"
                  disabled={loadingPlan.value || (submitting.value as any)}
                  className={__classes("container-action")}
                  onClick={(event: any) => {
                    __call(preview, event);
                  }}
                  data-v-a7cd5075=""
                >
                  {loadingPlan.value
                    ? t("m.6b72c3d6855c")
                    : t("m.fda000ea51ab")}
                </button>
                {error.value ? (
                  <p
                    role="alert"
                    className={__classes("rebuild-error")}
                    data-v-a7cd5075=""
                  >
                    {display(error.value)}
                  </p>
                ) : null}
                {loadingPlan.value ? (
                  <p
                    role="status"
                    className={__classes("rebuild-wait")}
                    data-v-a7cd5075=""
                  >
                    {t("m.c6e3dd69d100")}
                  </p>
                ) : null}
                {plan.value ? (
                  <div
                    data-testid="rebuild-plan"
                    className={__classes("rebuild-plan")}
                    data-v-a7cd5075=""
                  >
                    <dl
                      className={__classes("rebuild-facts")}
                      data-v-a7cd5075=""
                    >
                      <div data-v-a7cd5075="">
                        <dt data-v-a7cd5075="">{t("m.eed0f73e6ed4")}</dt>
                        <dd data-v-a7cd5075="">{plan.value.imageId}</dd>
                      </div>
                      <div data-v-a7cd5075="">
                        <dt data-v-a7cd5075="">{t("m.82b7cf40b5e6")}</dt>
                        <dd data-v-a7cd5075="">
                          {plan.value.wasRunning
                            ? t("m.ad5a3b7023cc")
                            : t("m.7942087a70a9")}
                        </dd>
                      </div>
                      <div data-v-a7cd5075="">
                        <dt data-v-a7cd5075="">{t("m.07085db02b1f")}</dt>
                        <dd data-v-a7cd5075="">
                          {plan.value.writableLayerBytes}
                          {" bytes"}
                          {plan.value.snapshotRequired
                            ? t("m.0c661a382e4c")
                            : t("m.d9dae035a677")}
                        </dd>
                      </div>
                    </dl>
                    <p data-v-a7cd5075="">
                      <strong data-v-a7cd5075="">{t("m.9ffd3498126c")}</strong>
                      {plan.value.downtime}
                    </p>
                    <div
                      className={__classes("rebuild-columns")}
                      data-v-a7cd5075=""
                    >
                      <div data-v-a7cd5075="">
                        <h4 data-v-a7cd5075="">{t("m.670ec25af841")}</h4>
                        <ul data-v-a7cd5075="">
                          <Fragment>
                            {__each(plan.value.preserved, (item) => (
                              <li key={item as any} data-v-a7cd5075="">
                                {item}
                              </li>
                            ))}
                          </Fragment>
                        </ul>
                      </div>
                      <div data-v-a7cd5075="">
                        <h4 data-v-a7cd5075="">{t("m.e329c44530ee")}</h4>
                        <ul data-v-a7cd5075="">
                          <Fragment>
                            {__each(plan.value.changed, (item) => (
                              <li key={item as any} data-v-a7cd5075="">
                                {item}
                              </li>
                            ))}
                          </Fragment>
                        </ul>
                      </div>
                    </div>
                    <div
                      className={__classes("rebuild-columns")}
                      data-v-a7cd5075=""
                    >
                      <div data-v-a7cd5075="">
                        <h4 data-v-a7cd5075="">{t("m.6d355bd867cf")}</h4>
                        <ul data-v-a7cd5075="">
                          <Fragment>
                            {__each(plan.value.portsBefore, (item) => (
                              <li key={item as any} data-v-a7cd5075="">
                                {item}
                              </li>
                            ))}
                          </Fragment>
                          {!plan.value.portsBefore.length ? (
                            <li data-v-a7cd5075="">{t("m.cf159c2050f5")}</li>
                          ) : null}
                        </ul>
                      </div>
                      <div data-v-a7cd5075="">
                        <h4 data-v-a7cd5075="">{t("m.f0754c848ad6")}</h4>
                        <ul data-v-a7cd5075="">
                          <Fragment>
                            {__each(plan.value.portsAfter, (item) => (
                              <li key={item as any} data-v-a7cd5075="">
                                {item}
                              </li>
                            ))}
                          </Fragment>
                          {!plan.value.portsAfter.length ? (
                            <li data-v-a7cd5075="">{t("m.cf159c2050f5")}</li>
                          ) : null}
                        </ul>
                      </div>
                    </div>
                    <h4 data-v-a7cd5075="">{t("m.a68527cc9644")}</h4>
                    <ul data-v-a7cd5075="">
                      <Fragment>
                        {__each(plan.value.mounts, (mount) => (
                          <li
                            key={`${mount.type}-${mount.destination}` as any}
                            data-v-a7cd5075=""
                          >
                            {mount.type}
                            {" → "}
                            {mount.destination}
                            {"（"}
                            {mount.readWrite
                              ? t("m.a3bc3e79fc0d")
                              : t("m.3b5ec3533b0e")}
                            {"）"}
                          </li>
                        ))}
                      </Fragment>
                      {!plan.value.mounts.length ? (
                        <li data-v-a7cd5075="">{t("m.a687446f9cb9")}</li>
                      ) : null}
                    </ul>
                    <h4 data-v-a7cd5075="">{t("m.c006769c23af")}</h4>
                    <ul
                      className={__classes("rebuild-risks")}
                      data-v-a7cd5075=""
                    >
                      <Fragment>
                        {__each(plan.value.risks, (item) => (
                          <li key={item as any} data-v-a7cd5075="">
                            {item}
                          </li>
                        ))}
                      </Fragment>
                    </ul>
                    <label
                      className={__classes("rebuild-ack")}
                      data-v-a7cd5075=""
                    >
                      <input
                        type="checkbox"
                        disabled={submitting.value as any}
                        {...__input(
                          acknowledged.value,
                          (__nextValue: any) => {
                            acknowledged.value = __nextValue;
                          },
                          "checkbox",
                          undefined,
                          [],
                        )}
                        data-v-a7cd5075=""
                      ></input>
                      {t("m.2af7ec0d2b15")}
                    </label>
                    <button
                      type="button"
                      data-testid="submit-container-rebuild"
                      disabled={
                        !acknowledged.value ||
                        submitting.value ||
                        __props.stale ||
                        (__props.container.stale as any)
                      }
                      className={__classes(
                        "container-action container-action-danger",
                      )}
                      onClick={(event: any) => {
                        __call(submitRebuild, event);
                      }}
                      data-v-a7cd5075=""
                    >
                      {submitting.value
                        ? t("m.26ef00b7d061")
                        : t("m.be23bb9aeb97")}
                    </button>
                  </div>
                ) : null}
              </section>
            ) : null}
          </div>
        </>
      );
    },
    input,
    ["nodeId", "container", "stale", "tasks"],
    [],
    {},
    true,
  );
}
