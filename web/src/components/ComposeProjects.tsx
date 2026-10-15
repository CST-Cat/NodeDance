import { api, type ContainerTask, type DashboardPreference } from "../api";
import {
  composeApi,
  type ComposeOperation,
  type ComposeProject,
} from "../compose-api";
import { requestConfirm, requestPrompt } from "../dialogs";
import { display, t } from "../i18n";
import { afterMount, beforeDispose, cell, derived } from "../state";
import ComposeCreateForm from "./ComposeCreateForm";
import ComposeEditor from "./ComposeEditor";
import DockerActionButtons, {
  type DockerPrimaryAction,
} from "./DockerActionButtons";

import { Fragment } from "react";
import { __classes, __each, __indeterminate, useController } from "../state";
import "./ComposeProjects.css";
type Props = {
  nodeId: string;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function ComposeProjects(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const projects = cell<ComposeProject[]>([]);
      const loading = cell(true);
      const error = cell("");
      const stale = cell(true);
      const editorProjectKey = cell("");
      const pending = cell<Record<string, string>>({});
      const taskByProject = cell<Record<string, ContainerTask>>({});
      const selectedKeys = cell<string[]>([]);
      const createOpen = cell(false);
      const selectionBusy = cell(false);
      const preferences = cell<DashboardPreference[]>([]);
      const projectErrors = cell<
        Record<
          string,
          {
            message: string;
            action: ComposeOperation;
          }
        >
      >({});
      const selectedProjects = derived(() =>
        projects.value.filter((project) =>
          selectedKeys.value.includes(project.ref.key),
        ),
      );
      const allSelected = derived(
        () =>
          projects.value.length > 0 &&
          selectedProjects.value.length === projects.value.length,
      );
      function toggleProject(key: string, selected: boolean) {
        selectedKeys.value = selected
          ? [...new Set([...selectedKeys.value, key])]
          : selectedKeys.value.filter((value) => value !== key);
      }
      function projectTitle(project: ComposeProject) {
        return (
          preferences.value.find(
            (item) =>
              item.targetKind === "compose_project" &&
              item.identity === "compose_project:" + project.ref.key,
          )?.alias || project.ref.name
        );
      }
      function canRun(project: ComposeProject) {
        return (
          !stale.value &&
          !project.dataStale &&
          project.configAvailable &&
          !pending.value[project.ref.key]
        );
      }
      function selectionCan(action: DockerPrimaryAction | "deploy" | "edit") {
        if (selectionBusy.value || !selectedProjects.value.length) return false;
        if (action === "rename") return selectedProjects.value.length === 1;
        if (action === "edit")
          return (
            selectedProjects.value.length === 1 &&
            selectedProjects.value.every(canRun)
          );
        return selectedProjects.value.every(canRun);
      }
      async function runSelection(action: DockerPrimaryAction | "deploy") {
        if (!selectionCan(action)) return;
        const keys = selectedProjects.value.map((project) => project.ref.key);
        selectionBusy.value = true;
        try {
          if (action === "rename") {
            const project = selectedProjects.value[0];
            const alias = await requestPrompt(
              t("compose.aliasPrompt"),
              projectTitle(project),
            );
            if (alias === null) return;
            const existing = preferences.value.find(
              (item) =>
                item.targetKind === "compose_project" &&
                item.identity === "compose_project:" + project.ref.key,
            );
            const preference: DashboardPreference = {
              nodeId: props.nodeId,
              targetKind: "compose_project",
              identity: "compose_project:" + project.ref.key,
              alias,
              icon: "",
              notes: "",
              serviceUrl: "",
              sshHost: "",
              sshPort: 0,
              sshUser: "",
              group: "",
              sortOrder: 0,
              visible: true,
              pinned: false,
              ...existing,
            };
            preference.alias = alias;
            await api.saveNodePreference(props.nodeId, preference);
            preferences.value = [
              ...preferences.value.filter(
                (item) => item.identity !== preference.identity,
              ),
              preference,
            ];
            return;
          }
          if (action !== "start") {
            const names = selectedProjects.value
              .map((project) => project.ref.name)
              .join("、");
            const message =
              action === "delete"
                ? t("compose.confirmDelete", {
                    "0": names,
                  })
                : t("compose.confirmBatch", {
                    "0": operationLabel(action),
                    "1": names,
                  });
            if (!(await requestConfirm(message))) return;
          }
          await refresh();
          const current = keys.map((key) =>
            projects.value.find((project) => project.ref.key === key),
          );
          if (
            !current.every(
              (project): project is ComposeProject =>
                !!project && canRun(project),
            )
          ) {
            error.value = t("compose.selectionChanged");
            return;
          }
          for (const project of current) await runAction(project, action, true);
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : String(reason);
        } finally {
          selectionBusy.value = false;
        }
      }
      let refreshTimer: ReturnType<typeof setInterval> | undefined;
      let disposed = false;
      const editorProject = derived(
        () =>
          projects.value.find(
            (project) => project.ref.key === editorProjectKey.value,
          ) ?? null,
      );
      async function refresh() {
        loading.value = projects.value.length === 0;
        error.value = "";
        try {
          const [result, saved] = await Promise.all([
            composeApi.projects(props.nodeId),
            api.nodePreferences(props.nodeId),
          ]);
          if (disposed) return;
          projects.value = result.projects;
          preferences.value = saved.preferences;
          selectedKeys.value = selectedKeys.value.filter((key) =>
            result.projects.some((project) => project.ref.key === key),
          );
          stale.value = Boolean(result.dataStale);
        } catch (reason) {
          if (disposed) return;
          stale.value = true;
          error.value =
            reason instanceof Error ? reason.message : t("m.777bbe3013b4");
        } finally {
          if (!disposed) loading.value = false;
        }
      }
      function operationLabel(action: ComposeOperation) {
        return (
          {
            start: t("m.56410fc65314"),
            stop: t("m.ca4d973c0b00"),
            restart: t("m.562822892865"),
            deploy: t("m.3ca7718593c0"),
            config_save: t("m.6e584e3d5ce6"),
            delete: t("m.2f9daa828907"),
          } as const
        )[action];
      }
      function operationConfirmation(
        project: ComposeProject,
        action: ComposeOperation,
      ): string | null {
        const services =
          project.services
            .map((service) =>
              t("m.6d88c1288a7f", {
                "0": service.name,
                "1": service.instances.length,
              }),
            )
            .join("、") || t("m.2681214710d2");
        if (action === "stop")
          return t("m.4525ce3335ca", {
            "0": project.ref.name,
            "1": services,
          });
        if (action === "restart")
          return t("m.3c8411939878", {
            "0": project.ref.name,
            "1": services,
          });
        if (action === "deploy")
          return t("m.bbff0417eee6", {
            "0": project.ref.name,
            "1": services,
          });
        return null;
      }
      function scheduleTaskPoll(projectKey: string, taskId: string) {
        const timer = setTimeout(async () => {
          timers.delete(timer);
          try {
            const current = await api.nodeTask(props.nodeId, taskId);
            if (disposed) return;
            taskByProject.value = {
              ...taskByProject.value,
              [projectKey]: current,
            };
            if (current.status === "queued" || current.status === "running") {
              scheduleTaskPoll(projectKey, taskId);
            } else {
              pending.value = {
                ...pending.value,
                [projectKey]: "",
              };
              await refresh();
            }
          } catch (reason) {
            if (disposed) return;
            pending.value = {
              ...pending.value,
              [projectKey]: "",
            };
            error.value =
              reason instanceof Error ? reason.message : t("m.54c881f3fc7c");
          }
        }, 800);
        timers.add(timer);
      }
      const timers = new Set<ReturnType<typeof setTimeout>>();
      async function runAction(
        project: ComposeProject,
        action: ComposeOperation,
        confirmed = false,
      ) {
        if (
          stale.value ||
          project.dataStale ||
          !project.configAvailable ||
          pending.value[project.ref.key]
        )
          return;
        const confirmation = operationConfirmation(project, action);
        if (!confirmed && confirmation && !(await requestConfirm(confirmation)))
          return;
        const current = projects.value.find(
          (item) => item.ref.key === project.ref.key,
        );
        if (!current || !canRun(current)) return;
        if (
          action === "delete" &&
          !confirmed &&
          !(await requestConfirm(
            t("compose.confirmDelete", {
              "0": project.ref.name,
            }),
          ))
        )
          return;
        const errors = {
          ...projectErrors.value,
        };
        delete errors[project.ref.key];
        projectErrors.value = errors;
        pending.value = {
          ...pending.value,
          [project.ref.key]: action,
        };
        error.value = "";
        try {
          const accepted = await composeApi.createTask(
            props.nodeId,
            project.ref.key,
            {
              action,
              ...(action === "delete"
                ? {
                    deleteConfirmationKey: project.ref.key,
                  }
                : {}),
            },
          );
          taskByProject.value = {
            ...taskByProject.value,
            [project.ref.key]: await api.nodeTask(
              props.nodeId,
              accepted.taskId,
            ),
          };
          const task = taskByProject.value[project.ref.key];
          if (task.status === "queued" || task.status === "running")
            scheduleTaskPoll(project.ref.key, accepted.taskId);
          else {
            pending.value = {
              ...pending.value,
              [project.ref.key]: "",
            };
            await refresh();
          }
        } catch (reason) {
          pending.value = {
            ...pending.value,
            [project.ref.key]: "",
          };
          projectErrors.value = {
            ...projectErrors.value,
            [project.ref.key]: {
              action,
              message:
                reason instanceof Error
                  ? reason.message
                  : t("m.96556b2e2aac", {
                      "0": operationLabel(action),
                    }),
            },
          };
        }
      }
      function taskMessage(task?: ContainerTask) {
        if (!task) return "";
        if (task.status === "succeeded")
          return t("m.10e0cfbae303", {
            "0": operationLabel(
              task.action.replace(/^compose_/, "") as ComposeOperation,
            ),
            "1": task.result.observedState || task.result.code,
          });
        if (task.status === "failed") {
          if (
            task.action === "compose_deploy" &&
            ["recovered", "recreated", "restored_offline"].includes(
              task.result.observedState || "",
            )
          ) {
            const restored =
              task.result.observedState === "restored_offline"
                ? t("m.74e19906ba04")
                : t("m.a40db8641914");
            return t("m.3c54e7a0bc54", {
              "0": task.result.resourceRevision || task.result.code,
              "1": restored,
            });
          }
          return t("m.5bc4c4816eeb", {
            "0": task.result.code || t("m.f50ce21ae910"),
          });
        }
        if (task.status === "unknown") {
          if (task.result.observedState === "recovery_required") {
            return t("m.29dc41c6f776", {
              "0": task.result.observedState,
              "1": task.result.resourceRevision || t("m.fb2425b0c8e1"),
            });
          }
          return t("m.d004f1dc1829");
        }
        return `${operationLabel(task.action.replace(/^compose_/, "") as ComposeOperation)}：${task.status}`;
      }
      afterMount(() => {
        void refresh();
        refreshTimer = setInterval(() => void refresh(), 15_000);
      });
      beforeDispose(() => {
        disposed = true;
        if (refreshTimer) clearInterval(refreshTimer);
        for (const timer of timers) clearTimeout(timer);
        timers.clear();
      });
      return () => (
        <>
          <section
            aria-labelledby="compose-projects-title"
            data-testid="compose-projects"
            className={__classes("compose-projects section-panel")}
            data-v-429264a6=""
          >
            <header className={__classes("section-toolbar")} data-v-429264a6="">
              <div data-v-429264a6="">
                <span className={__classes("eyebrow")} data-v-429264a6="">
                  {t("label.135e86eb8ca8")}
                </span>
                <h2 id="compose-projects-title" data-v-429264a6="">
                  {t("compose.projectsTitle")}
                </h2>
              </div>
              <button
                type="button"
                disabled={loading.value as any}
                className={__classes("container-action")}
                onClick={(event: any) => {
                  void refresh();
                }}
                data-v-429264a6=""
              >
                {loading.value ? t("m.7f3c8312816f") : t("m.aee887434131")}
              </button>
            </header>
            {stale.value ? (
              <p
                role="status"
                className={__classes("compose-stale")}
                data-v-429264a6=""
              >
                {t("m.11a3711c480d")}
              </p>
            ) : null}
            {error.value ? (
              <p
                role="alert"
                className={__classes("compose-error")}
                data-v-429264a6=""
              >
                {display(error.value)}
              </p>
            ) : null}
            {loading.value && !projects.value.length ? (
              <p className={__classes("compose-empty")} data-v-429264a6="">
                {t("m.6025e55b6f68")}
              </p>
            ) : !projects.value.length ? (
              <p className={__classes("compose-empty")} data-v-429264a6="">
                {t("m.55f37d0877f7")}
              </p>
            ) : null}
            <div
              className={__classes("compose-selection-bar")}
              data-v-429264a6=""
            >
              <label data-v-429264a6="">
                <input
                  type="checkbox"
                  checked={allSelected.value as any}
                  {...__indeterminate(
                    selectedProjects.value.length > 0 && !allSelected.value,
                  )}
                  disabled={
                    selectionBusy.value || (!projects.value.length as any)
                  }
                  onChange={(event: any) => {
                    selectedKeys.value = (event.target as HTMLInputElement)
                      .checked
                      ? projects.value.map((project) => project.ref.key)
                      : [];
                  }}
                  data-v-429264a6=""
                ></input>
                {t("compose.selectAll")}
              </label>
              <span role="status" data-v-429264a6="">
                {t("compose.selected", {
                  "0": selectedProjects.value.length,
                  "1": projects.value.length,
                })}
              </span>
              <button
                type="button"
                disabled={
                  selectionBusy.value || (!selectedProjects.value.length as any)
                }
                className={__classes("container-action")}
                onClick={(event: any) => {
                  selectedKeys.value = [];
                }}
                data-v-429264a6=""
              >
                {t("docker.clearSelection")}
              </button>
            </div>
            <div
              role="group"
              aria-label={t("compose.toolbar") as any}
              data-testid="compose-toolbar"
              className={__classes("compose-toolbar")}
              data-v-429264a6=""
            >
              <DockerActionButtons
                can={selectionCan as any}
                renameLabel={t("compose.alias") as any}
                onAction={(event: any) => {
                  void runSelection(event);
                }}
                data-v-429264a6=""
              ></DockerActionButtons>
              <button
                type="button"
                disabled={!selectionCan("deploy") as any}
                data-action="deploy"
                className={__classes("container-action")}
                onClick={(event: any) => {
                  void runSelection("deploy");
                }}
                data-v-429264a6=""
              >
                {t("m.3ca7718593c0")}
              </button>
              <button
                type="button"
                disabled={!selectionCan("edit") as any}
                data-action="edit"
                className={__classes("container-action")}
                onClick={(event: any) => {
                  editorProjectKey.value = selectedProjects.value[0].ref.key;
                }}
                data-v-429264a6=""
              >
                {t("m.b2b5b9fafa31")}
              </button>
              <button
                type="button"
                disabled={stale.value as any}
                aria-expanded={createOpen.value as any}
                aria-controls="compose-create-project"
                className={__classes("container-action")}
                onClick={(event: any) => {
                  createOpen.value = !createOpen.value;
                }}
                data-v-429264a6=""
              >
                {t(
                  createOpen.value
                    ? "compose.closeCreate"
                    : "compose.newProject",
                )}
              </button>
            </div>
            {createOpen.value ? (
              <div id="compose-create-project" data-v-429264a6="">
                <ComposeCreateForm
                  nodeId={__props.nodeId as any}
                  disabled={stale.value as any}
                  onCreated={(event: any) => {
                    void refresh();
                  }}
                  data-v-429264a6=""
                ></ComposeCreateForm>
              </div>
            ) : null}
            <div
              role="list"
              className={__classes("compose-resource-list")}
              data-v-429264a6=""
            >
              <Fragment>
                {__each(projects.value, (project) => (
                  <article
                    role="listitem"
                    key={project.ref.key as any}
                    data-project-key={project.ref.key as any}
                    data-selected={
                      selectedKeys.value.includes(project.ref.key) as any
                    }
                    data-stale={project.dataStale || (stale.value as any)}
                    className={__classes("compose-project-card")}
                    data-v-429264a6=""
                  >
                    <header
                      className={__classes("compose-project-heading")}
                      data-v-429264a6=""
                    >
                      <label
                        className={__classes("compose-project-select")}
                        data-v-429264a6=""
                      >
                        <input
                          type="checkbox"
                          checked={
                            selectedKeys.value.includes(project.ref.key) as any
                          }
                          disabled={selectionBusy.value as any}
                          aria-label={
                            t("compose.selectProject", {
                              "0": projectTitle(project),
                            }) as any
                          }
                          onChange={(event: any) => {
                            toggleProject(
                              project.ref.key,
                              (event.target as HTMLInputElement).checked,
                            );
                          }}
                          data-v-429264a6=""
                        ></input>
                      </label>
                      <div
                        className={__classes("compose-project-copy")}
                        data-v-429264a6=""
                      >
                        <span
                          className={__classes("eyebrow")}
                          data-v-429264a6=""
                        >
                          {t(
                            project.configAvailable
                              ? "compose.configAvailable"
                              : "compose.containersDiscovered",
                          )}
                        </span>
                        <h3 data-v-429264a6="">{projectTitle(project)}</h3>
                        {projectTitle(project) !== project.ref.name ? (
                          <p data-v-429264a6="">{project.ref.name}</p>
                        ) : null}
                        <p data-v-429264a6="">{project.ref.workingDirectory}</p>
                      </div>
                    </header>
                    {projectErrors.value[project.ref.key] ? (
                      <div
                        role="alert"
                        className={__classes("compose-project-error")}
                        data-v-429264a6=""
                      >
                        <p data-v-429264a6="">
                          {display(
                            projectErrors.value[project.ref.key].message,
                          )}
                        </p>
                        <button
                          type="button"
                          disabled={
                            selectionBusy.value || (!canRun(project) as any)
                          }
                          className={__classes("container-action")}
                          onClick={(event: any) => {
                            void runAction(
                              project,
                              projectErrors.value[project.ref.key].action,
                            );
                          }}
                          data-v-429264a6=""
                        >
                          {t("compose.retry")}
                        </button>
                      </div>
                    ) : null}
                    {!project.configAvailable ? (
                      <p
                        className={__classes("compose-warning")}
                        data-v-429264a6=""
                      >
                        {t("m.083c34e37d43")}
                      </p>
                    ) : null}
                    {pending.value[project.ref.key] ? (
                      <p
                        role="status"
                        className={__classes("compose-task-line")}
                        data-v-429264a6=""
                      >
                        {t("m.e6b02a08e69a")}
                        {operationLabel(
                          pending.value[project.ref.key] as ComposeOperation,
                        )}
                        {"…"}
                      </p>
                    ) : null}
                    {taskByProject.value[project.ref.key] ? (
                      <p
                        data-status={
                          taskByProject.value[project.ref.key].status as any
                        }
                        role="status"
                        className={__classes("compose-task-line")}
                        data-v-429264a6=""
                      >
                        {display(
                          taskMessage(taskByProject.value[project.ref.key]),
                        )}
                      </p>
                    ) : null}
                    {project.services.length ? (
                      <div
                        className={__classes("compose-services")}
                        data-v-429264a6=""
                      >
                        <Fragment>
                          {__each(project.services, (service) => (
                            <section
                              key={service.name as any}
                              className={__classes("compose-service")}
                              data-v-429264a6=""
                            >
                              <h4 data-v-429264a6="">
                                {service.name}{" "}
                                <small data-v-429264a6="">
                                  {service.instances.length}
                                  {t("m.3cbb1fb681cc")}
                                </small>
                              </h4>
                              <ul data-v-429264a6="">
                                <Fragment>
                                  {__each(service.instances, (instance) => (
                                    <li
                                      key={instance.containerId as any}
                                      data-v-429264a6=""
                                    >
                                      <span data-v-429264a6="">
                                        {instance.containerName}
                                      </span>
                                      <code data-v-429264a6="">
                                        {instance.containerId.slice(0, 12)}
                                      </code>
                                      <span
                                        data-state={instance.state as any}
                                        data-v-429264a6=""
                                      >
                                        {instance.state}
                                        {instance.health ? (
                                          <Fragment>
                                            {" · "}
                                            {instance.health}
                                          </Fragment>
                                        ) : null}
                                      </span>
                                    </li>
                                  ))}
                                </Fragment>
                              </ul>
                            </section>
                          ))}
                        </Fragment>
                      </div>
                    ) : (
                      <p
                        className={__classes("compose-empty")}
                        data-v-429264a6=""
                      >
                        {t("m.831759cffa59")}
                      </p>
                    )}
                  </article>
                ))}
              </Fragment>
            </div>
            {editorProject.value ? (
              <ComposeEditor
                key={
                  (__props.nodeId + ":" + editorProject.value.ref.key) as any
                }
                nodeId={__props.nodeId as any}
                project={editorProject.value as any}
                data-v-429264a6=""
              ></ComposeEditor>
            ) : null}
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
