import UiLoadState from "./UiLoadState";
import {
  api,
  type ContainerTask,
  type DockerImage,
  type TaskAuditEvent,
} from "../api";
import { requestPrompt } from "../dialogs";
import { display, locale, t } from "../i18n";
import { afterMount, beforeDispose, cell, derived } from "../state";

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import "./ImagesPanel.css";
type Props = {
  nodeId: string;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function ImagesPanel(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const images = cell<DockerImage[]>([]);
      const loaded = cell(false);
      const total = cell(0);
      const page = cell(0);
      const filterInput = cell("");
      const filter = cell("");
      const reference = cell("");
      const username = cell("");
      const password = cell("");
      const loading = cell(false);
      const submitting = cell(false);
      const deleting = cell<Record<string, boolean>>({});
      const error = cell("");
      const notice = cell("");
      const activeTask = cell<ContainerTask>();
      const cancelPending = cell(false);
      const auditEvents = cell<TaskAuditEvent[]>([]);
      const auditVisible = cell(false);
      const auditLoading = cell(false);
      const auditError = cell("");
      const taskError = cell("");
      const pageCount = derived(() => Math.max(1, Math.ceil(total.value / 50)));
      let pollTimer: ReturnType<typeof setInterval> | undefined;
      let disposed = false;
      function taskIsActive(task?: ContainerTask): boolean {
        return Boolean(
          task && ["queued", "running", "unknown"].includes(task.status),
        );
      }
      function setActiveTask(task: ContainerTask) {
        if (activeTask.value?.taskId !== task.taskId) {
          cancelPending.value = false;
          auditEvents.value = [];
          auditVisible.value = false;
          auditError.value = "";
        }
        activeTask.value = task;
      }
      function taskStateLabel(task: ContainerTask): string {
        if (task.status === "queued") return t("m.165cd916f553");
        if (task.status === "running")
          return task.progress.total > 0
            ? t("m.f4854b2456e3", {
                "0": task.progress.completed.toLocaleString(locale.value),
                "1": task.progress.total.toLocaleString(locale.value),
              })
            : t("m.472b61dd983a", {
                "0": task.progress.phase || t("m.694b71bc8013"),
              });
        if (task.status === "unknown") return t("m.e04f5d26341a");
        if (task.status === "succeeded")
          return t("m.52a8bda11dff", {
            "0": task.result.observedState || t("m.5b67afa89eb2"),
          });
        if (task.status === "canceled") return t("m.d2fbf2ec8c2d");
        if (task.status === "timed_out") return t("m.7112c0bda6de");
        return t("m.5bc4c4816eeb", {
          "0":
            task.result.observedState ||
            task.result.code ||
            t("m.d885659ddd84"),
        });
      }
      function formatSize(bytes: number): string {
        if (bytes < 1024) return `${bytes} B`;
        const units = ["KiB", "MiB", "GiB", "TiB"];
        let value = bytes;
        let unit = -1;
        do {
          value /= 1024;
          unit++;
        } while (value >= 1024 && unit < units.length - 1);
        return `${value.toFixed(value >= 100 ? 0 : value >= 10 ? 1 : 2)} ${units[unit]}`;
      }
      async function loadImages() {
        if (loading.value || disposed) return;
        loading.value = true;
        error.value = "";
        try {
          const result = await api.nodeImages(
            props.nodeId,
            filter.value,
            page.value,
          );
          if (disposed) return;
          if (result.errorCode) throw new Error(t("m.159467f4355b"));
          images.value = Array.isArray(result.images) ? result.images : [];
          total.value = result.total;
          loaded.value = true;
        } catch (reason) {
          if (!disposed)
            error.value =
              reason instanceof Error ? reason.message : t("m.e7515c35320f");
        } finally {
          loading.value = false;
        }
      }
      async function loadLatestImageTask() {
        try {
          const result = await api.nodeTasks(props.nodeId);
          if (disposed) return;
          const latest = result.tasks
            .filter(
              (task) =>
                task.action === "image_pull" || task.action === "image_delete",
            )
            .sort(
              (left, right) =>
                Date.parse(right.createdAt) - Date.parse(left.createdAt),
            )[0];
          if (latest) {
            setActiveTask(latest);
          }
        } catch (reason) {
          if (!disposed)
            taskError.value =
              reason instanceof Error ? reason.message : t("m.77293f47bbd9");
        }
      }
      async function refreshTask() {
        if (!activeTask.value || disposed) return;
        try {
          const next = await api.nodeTask(
            props.nodeId,
            activeTask.value.taskId,
          );
          if (disposed) return;
          const wasActive = taskIsActive(activeTask.value);
          setActiveTask(next);
          if (wasActive && !taskIsActive(next)) {
            cancelPending.value = false;
            await loadImages();
          }
        } catch (reason) {
          if (!disposed)
            taskError.value =
              reason instanceof Error ? reason.message : t("m.cac85b60b94d");
        }
      }
      async function submitPull() {
        const imageReference = reference.value.trim();
        if (!imageReference || submitting.value) return;
        submitting.value = true;
        error.value = "";
        notice.value = "";
        taskError.value = "";
        const auth = {
          username: username.value,
          password: password.value,
        };
        username.value = "";
        password.value = "";
        try {
          const accepted = await api.createImagePullTask(props.nodeId, {
            imageReference,
            ...(auth.username || auth.password
              ? {
                  username: auth.username,
                  password: auth.password,
                }
              : {}),
          });
          reference.value = "";
          setActiveTask(await api.nodeTask(props.nodeId, accepted.taskId));
          notice.value = t("m.ddaed4324a4e");
          await loadImages();
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : t("m.c831f021d9c3");
        } finally {
          auth.username = "";
          auth.password = "";
          username.value = "";
          password.value = "";
          submitting.value = false;
        }
      }
      async function submitDelete(image: DockerImage) {
        if (image.containers > 0 || deleting.value[image.id]) return;
        const confirmation = await requestPrompt(
          t("m.90829b35e800", {
            "0": image.id,
          }),
        );
        if (confirmation === null) return;
        if (confirmation.trim() !== image.id) {
          error.value = t("m.b64864a55a31");
          return;
        }
        deleting.value = {
          ...deleting.value,
          [image.id]: true,
        };
        error.value = "";
        try {
          const accepted = await api.createImageDeleteTask(
            props.nodeId,
            image.id,
          );
          setActiveTask(await api.nodeTask(props.nodeId, accepted.taskId));
          notice.value = t("m.3ea800a3051c");
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : t("m.2cd1ecc3647f");
        } finally {
          deleting.value = {
            ...deleting.value,
            [image.id]: false,
          };
        }
      }
      async function cancelPull() {
        const task = activeTask.value;
        if (!task || task.action !== "image_pull" || task.status !== "running")
          return;
        taskError.value = "";
        try {
          setActiveTask(await api.cancelNodeTask(props.nodeId, task.taskId));
          cancelPending.value = true;
          notice.value = t("m.0469be03574e");
        } catch (reason) {
          taskError.value =
            reason instanceof Error ? reason.message : t("m.c32c64a98d10");
        }
      }
      async function toggleAudit() {
        if (!activeTask.value) return;
        auditVisible.value = !auditVisible.value;
        if (
          !auditVisible.value ||
          auditEvents.value.length ||
          auditLoading.value
        )
          return;
        auditLoading.value = true;
        auditError.value = "";
        try {
          const result = await api.nodeTaskAudit(
            props.nodeId,
            activeTask.value.taskId,
          );
          if (!disposed) auditEvents.value = result.events;
        } catch (reason) {
          if (!disposed)
            auditError.value =
              reason instanceof Error ? reason.message : t("m.74c2941dde99");
        } finally {
          auditLoading.value = false;
        }
      }
      function applyFilter() {
        page.value = 0;
        filter.value = filterInput.value.trim();
        void loadImages();
      }
      function movePage(offset: number) {
        const next = page.value + offset;
        if (next < 0 || next >= pageCount.value) return;
        page.value = next;
        void loadImages();
      }
      afterMount(() => {
        void Promise.all([loadImages(), loadLatestImageTask()]);
        pollTimer = setInterval(() => {
          if (taskIsActive(activeTask.value)) void refreshTask();
          else void loadLatestImageTask();
        }, 1500);
      });
      beforeDispose(() => {
        disposed = true;
        if (pollTimer !== undefined) clearInterval(pollTimer);
        reference.value = "";
        username.value = "";
        password.value = "";
      });
      return () => (
        <>
          <section
            aria-labelledby="images-title"
            data-testid="docker-images"
            className={__classes("images-panel ui-load-region")}
            data-v-ae74c5fc=""
          >
            <header className={__classes("images-heading")} data-v-ae74c5fc="">
              <div data-v-ae74c5fc="">
                <span className={__classes("eyebrow")} data-v-ae74c5fc="">
                  {t("label.525f325a82b5")}
                </span>
                <h2 id="images-title" data-v-ae74c5fc="">
                  {t("m.176c09844e0f")}
                </h2>
              </div>
              <button
                type="button"
                disabled={loading.value as any}
                className={__classes("image-button")}
                onClick={(event: any) => {
                  __call(loadImages, event);
                }}
                data-v-ae74c5fc=""
              >
                {loading.value ? t("m.86b6d0d63062") : t("m.aee887434131")}
              </button>
            </header>
            <form
              className={__classes("image-pull-form")}
              onSubmit={(event: any) => {
                event.preventDefault();
                __call(submitPull, event);
              }}
              data-v-ae74c5fc=""
            >
              <label
                className={__classes("image-field image-reference")}
                data-v-ae74c5fc=""
              >
                {t("m.1357b2ae4279")}
                <input
                  name="imageReference"
                  autoComplete="off"
                  placeholder={t("m.15c4c6755fbe") as any}
                  required={true}
                  {...__input(
                    reference.value,
                    (__nextValue: any) => {
                      reference.value = __nextValue;
                    },
                    "",
                    undefined,
                    [],
                  )}
                  data-v-ae74c5fc=""
                ></input>
              </label>
              <label className={__classes("image-field")} data-v-ae74c5fc="">
                {t("m.4867bdab7b06")}
                <input
                  name="registryUsername"
                  autoComplete="off"
                  placeholder={t("m.293b74110f45") as any}
                  {...__input(
                    username.value,
                    (__nextValue: any) => {
                      username.value = __nextValue;
                    },
                    "",
                    undefined,
                    [],
                  )}
                  data-v-ae74c5fc=""
                ></input>
              </label>
              <label className={__classes("image-field")} data-v-ae74c5fc="">
                {t("m.8b7addffb5c2")}
                <input
                  name="registryPassword"
                  type="password"
                  autoComplete="new-password"
                  placeholder={t("m.51dccb462e09") as any}
                  {...__input(
                    password.value,
                    (__nextValue: any) => {
                      password.value = __nextValue;
                    },
                    "password",
                    undefined,
                    [],
                  )}
                  data-v-ae74c5fc=""
                ></input>
              </label>
              <button
                type="submit"
                disabled={submitting.value || (!reference.value.trim() as any)}
                className={__classes("image-button primary")}
                data-v-ae74c5fc=""
              >
                {submitting.value ? t("m.26ef00b7d061") : t("m.2b0005604564")}
              </button>
            </form>
            <p className={__classes("image-hint")} data-v-ae74c5fc="">
              {t("m.87bb077c4318")}
            </p>
            <div className={__classes("image-filter-row")} data-v-ae74c5fc="">
              <label className={__classes("image-field")} data-v-ae74c5fc="">
                {t("m.8a6ab3b06580")}
                <input
                  type="search"
                  placeholder={t("m.f7a89a15336a") as any}
                  onKeyDown={(event: any) => {
                    if (event.key !== "Enter") return;
                    event.preventDefault();
                    __call(applyFilter, event);
                  }}
                  {...__input(
                    filterInput.value,
                    (__nextValue: any) => {
                      filterInput.value = __nextValue;
                    },
                    "search",
                    undefined,
                    [],
                  )}
                  data-v-ae74c5fc=""
                ></input>
              </label>
              <button
                type="button"
                className={__classes("image-button")}
                onClick={(event: any) => {
                  __call(applyFilter, event);
                }}
                data-v-ae74c5fc=""
              >
                {t("m.b5f15473fdc0")}
              </button>
              <span className={__classes("image-total")} data-v-ae74c5fc="">
                {t("m.76e547a8fa54")}
                {total.value.toLocaleString(locale.value)}
                {t("m.51a215db46d9")}
              </span>
            </div>
            {error.value ? (
              <p
                role="alert"
                className={__classes("image-message error")}
                data-v-ae74c5fc=""
              >
                {display(error.value)}
              </p>
            ) : null}
            {notice.value ? (
              <p
                role="status"
                className={__classes("image-message")}
                data-v-ae74c5fc=""
              >
                {display(notice.value)}
              </p>
            ) : null}
            {taskError.value ? (
              <p
                role="alert"
                className={__classes("image-message error")}
                data-v-ae74c5fc=""
              >
                {display(taskError.value)}
              </p>
            ) : null}
            {activeTask.value ? (
              <article
                data-status={activeTask.value.status as any}
                aria-live="polite"
                className={__classes("image-task")}
                data-v-ae74c5fc=""
              >
                <div data-v-ae74c5fc="">
                  <strong data-v-ae74c5fc="">
                    {activeTask.value.action === "image_pull"
                      ? t("m.8d6e8b5c37c8")
                      : t("m.a1d1a5cd5594")}
                    {" · "}
                    {activeTask.value.status}
                  </strong>
                  <span data-v-ae74c5fc="">
                    {taskStateLabel(activeTask.value)}
                  </span>
                </div>
                {activeTask.value.action === "image_pull" &&
                activeTask.value.status === "running" ? (
                  <button
                    type="button"
                    disabled={cancelPending.value as any}
                    className={__classes("image-button")}
                    onClick={(event: any) => {
                      __call(cancelPull, event);
                    }}
                    data-v-ae74c5fc=""
                  >
                    {cancelPending.value
                      ? t("m.e8e08b0f61dd")
                      : t("m.a7d9cc0d2cb9")}
                  </button>
                ) : null}
                <button
                  type="button"
                  className={__classes("image-button")}
                  onClick={(event: any) => {
                    __call(toggleAudit, event);
                  }}
                  data-v-ae74c5fc=""
                >
                  {auditVisible.value
                    ? t("m.54e74c1e7504")
                    : t("m.97f395dec8ce")}
                </button>
              </article>
            ) : null}
            {activeTask.value && auditVisible.value ? (
              <div
                aria-label={t("m.30bf21ac5f41") as any}
                className={__classes("image-audit")}
                data-v-ae74c5fc=""
              >
                {auditLoading.value ? (
                  <p data-v-ae74c5fc="">{t("m.cf62890bdb61")}</p>
                ) : auditError.value ? (
                  <p
                    role="alert"
                    className={__classes("error")}
                    data-v-ae74c5fc=""
                  >
                    {display(auditError.value)}
                  </p>
                ) : (
                  <ol data-v-ae74c5fc="">
                    <Fragment>
                      {__each(auditEvents.value, (event) => (
                        <li key={event.id as any} data-v-ae74c5fc="">
                          <span data-v-ae74c5fc="">
                            {event.event}
                            {event.toStatus ? (
                              <Fragment>
                                {" · "}
                                {event.fromStatus || t("m.d2bb025a2e51")}
                                {" → "}
                                {event.toStatus}
                              </Fragment>
                            ) : null}
                          </span>
                          <time
                            dateTime={event.occurredAt as any}
                            data-v-ae74c5fc=""
                          >
                            {new Date(event.occurredAt).toLocaleString(
                              locale.value,
                            )}
                          </time>
                        </li>
                      ))}
                    </Fragment>
                  </ol>
                )}
              </div>
            ) : null}
            {images.value.length ? (
              <div className={__classes("image-list")} data-v-ae74c5fc="">
                <Fragment>
                  {__each(images.value, (image) => (
                    <article
                      key={image.id as any}
                      className={__classes("image-row")}
                      data-v-ae74c5fc=""
                    >
                      <div
                        className={__classes("image-row-head")}
                        data-v-ae74c5fc=""
                      >
                        <strong data-v-ae74c5fc="">
                          {image.tags.length
                            ? image.tags.join(", ")
                            : t("m.247781b10fdf")}
                        </strong>
                        <span data-v-ae74c5fc="">{formatSize(image.size)}</span>
                      </div>
                      <div
                        className={__classes("image-row-meta")}
                        data-v-ae74c5fc=""
                      >
                        <span
                          className={__classes("image-id")}
                          data-v-ae74c5fc=""
                        >
                          {image.id}
                        </span>
                        <span data-v-ae74c5fc="">
                          {image.containers}
                          {t("m.7a5eafabc37f")}
                        </span>
                        {image.createdAt ? (
                          <span data-v-ae74c5fc="">
                            {t("m.dc4d662d071e")}
                            {new Date(image.createdAt * 1000).toLocaleString(
                              locale.value,
                            )}
                          </span>
                        ) : null}
                      </div>
                      {image.digests.length ? (
                        <p
                          className={__classes("image-digests")}
                          data-v-ae74c5fc=""
                        >
                          {image.digests.join(" · ")}
                        </p>
                      ) : null}
                      <button
                        type="button"
                        disabled={
                          image.containers > 0 ||
                          (deleting.value[image.id] as any)
                        }
                        title={
                          image.containers > 0
                            ? t("m.1e695283a66a")
                            : (t("m.62f417a3db56") as any)
                        }
                        className={__classes("image-button danger")}
                        onClick={(event: any) => {
                          submitDelete(image);
                        }}
                        data-v-ae74c5fc=""
                      >
                        {deleting.value[image.id]
                          ? t("m.26ef00b7d061")
                          : image.containers > 0
                            ? t("m.fa48e8938940")
                            : t("m.dc61b6b9b500")}
                      </button>
                    </article>
                  ))}
                </Fragment>
              </div>
            ) : !loading.value && !error.value ? (
              <p className={__classes("images-empty")} data-v-ae74c5fc="">
                {filter.value ? t("m.bf11e3fed6b2") : t("m.715c063e56ec")}
              </p>
            ) : null}
            <UiLoadState loading={loading.value || (!loaded.value && !error.value)} ready={loaded.value} failed={!!error.value} />
            <nav
              aria-label={t("m.1fa7b5f8794e") as any}
              className={__classes("image-pagination")}
              data-v-ae74c5fc=""
            >
              <button
                type="button"
                disabled={page.value === 0 || (loading.value as any)}
                className={__classes("image-button")}
                onClick={(event: any) => {
                  movePage(-1);
                }}
                data-v-ae74c5fc=""
              >
                {t("m.c9b9ae7a6144")}
              </button>
              <span data-v-ae74c5fc="">
                {t("m.af50a0fe2a50")}
                {page.value + 1}
                {" / "}
                {pageCount.value}
                {t("m.d24d3c99460c")}
              </span>
              <button
                type="button"
                disabled={
                  page.value + 1 >= pageCount.value || (loading.value as any)
                }
                className={__classes("image-button")}
                onClick={(event: any) => {
                  movePage(1);
                }}
                data-v-ae74c5fc=""
              >
                {t("m.8a8542f69648")}
              </button>
            </nav>
          </section>
        </>
      );
    },
    input,
    ["nodeId"],
    [],
    {},
    true,
  );
}
