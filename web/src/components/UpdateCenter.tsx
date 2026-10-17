import { api, operationRequest, type AgentNode } from "../api";
import { t } from "../i18n";
import { afterMount, beforeDispose, cell, derived, watchState } from "../state";

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import UiSelect from "./UiSelect";
import "./UpdateCenter.css";
type Props = {} & { children?: import("react").ReactNode; [key: string]: any };
export default function UpdateCenter(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      interface ReleaseCheck {
        component: string;
        current: string;
        latest: string;
        available: boolean;
        configured: boolean;
        mode?: string;
        reason?: string;
        release: {
          url: string;
          notes: string;
        };
      }
      interface Job {
        id: string;
        component: string;
        current: string;
        target: string;
        status: string;
        stage: string;
        error?: string;
        started: string;
        finished?: string;
        backup?: string;
      }
      const component = cell("core"),
        nodeId = cell(""),
        version = cell("");
      const nodes = cell<AgentNode[]>([]),
        check = cell<ReleaseCheck>(),
        jobs = cell<Job[]>([]);
      const pollError = cell("");
      const error = cell(""),
        busy = cell(false),
        confirmed = cell(false),
        rollbackJob = cell("");
      const selected = derived(
        () => component.value === "core" || !!nodeId.value,
      );
      const query = derived(() => ({
        component: component.value,
        ...(component.value === "agent"
          ? {
              nodeId: nodeId.value,
            }
          : {}),
      }));
      let timer: ReturnType<typeof setTimeout> | undefined;
      let stopped = false,
        epoch = 0;
      function message(e: unknown) {
        return e instanceof Error ? e.message : String(e);
      }
      async function history() {
        if (!selected.value) return;
        const current = epoch;
        try {
          const result = await operationRequest<{
            jobs: Job[];
          }>("updates.status", {
            query: query.value,
          });
          if (current === epoch) {
            const completed = result.jobs.some(
              (job) =>
                job.finished &&
                jobs.value.some(
                  (previous) => previous.id === job.id && !previous.finished,
                ),
            );
            jobs.value = result.jobs;
            pollError.value = "";
            if (completed) void refresh();
          }
        } catch (e) {
          if (current === epoch) pollError.value = message(e);
        } finally {
          if (!stopped && current === epoch) timer = setTimeout(history, 3000);
        }
      }
      const changelog = cell<{
        core?: ReleaseCheck;
        agent?: ReleaseCheck;
      }>({});
      async function loadChangelog(component: "core" | "agent") {
        const current = epoch;
        try {
          const result = await operationRequest<ReleaseCheck>("updates.check", {
            query: { component },
          });
          if (current !== epoch) return;
          changelog.value = { ...changelog.value, [component]: result };
        } catch {
          if (current === epoch)
            changelog.value = { ...changelog.value, [component]: undefined };
        }
      }
      const changelogReady = cell(false);
      async function refresh() {
        if (!selected.value) return;
        check.value = undefined;
        confirmed.value = false;
        error.value = "";
        busy.value = true;
        const current = epoch;
        try {
          const result = await operationRequest<ReleaseCheck>("updates.check", {
            query: {
              ...query.value,
              ...(version.value
                ? {
                    version: version.value,
                  }
                : {}),
            },
          });
          if (current === epoch) check.value = result;
        } catch (e) {
          if (current === epoch) error.value = message(e);
        } finally {
          if (current === epoch) busy.value = false;
        }
      }
      async function submit() {
        if (
          busy.value ||
          !selected.value ||
          (rollbackJob.value
            ? !confirmed.value
            : !check.value?.available ||
              !check.value.configured)
        )
          return;
        busy.value = true;
        error.value = "";
        const action = rollbackJob.value ? "rollback" : "apply";
        const body = {
          ...query.value,
          confirm: true,
          ...(rollbackJob.value
            ? {
                jobId: rollbackJob.value,
              }
            : {
                version: check.value?.latest,
              }),
        };
        const current = epoch;
        try {
          const job = await operationRequest<Job>(
            "updates." + action,
            {},
            {
              body: JSON.stringify(body),
            },
          );
          if (current !== epoch) return;
          jobs.value = [
            job,
            ...jobs.value.filter((item) => item.id !== job.id),
          ];
          confirmed.value = false;
          rollbackJob.value = "";
          if (job.finished) void refresh();
        } catch (e) {
          if (current === epoch) error.value = message(e);
        } finally {
          if (current === epoch) busy.value = false;
        }
      }
      watchState(query, () => {
        epoch++;
        check.value = undefined;
        jobs.value = [];
        rollbackJob.value = "";
        confirmed.value = false;
        if (timer) clearTimeout(timer);
        void history();
        void refresh();
      });
      afterMount(async () => {
        try {
          nodes.value = (await api.nodes()).nodes;
          nodeId.value = nodes.value[0]?.nodeId ?? "";
        } catch (e) {
          error.value = message(e);
        }
        await Promise.allSettled([
          loadChangelog("core"),
          loadChangelog("agent"),
        ]);
        changelogReady.value = true;
        if (timer) clearTimeout(timer);
        void history();
        void refresh();
      });
      beforeDispose(() => {
        stopped = true;
        epoch++;
        if (timer) clearTimeout(timer);
      });
      return () => (
        <>
          <section
            className={__classes("update-center workspace-page")}
            data-v-fffea4e7=""
          >
            <div className={__classes("update-card")} data-v-fffea4e7="">
              <h2 data-v-fffea4e7="">{t("updates.title")}</h2>
              <div className={__classes("update-controls")} data-v-fffea4e7="">
                <label data-v-fffea4e7="">
                  {t("updates.component")}
                  <UiSelect
                    modelValue={component.value}
                    onUpdateModelValue={(__nextValue: any) => {
                      component.value = __nextValue;
                    }}
                    modelModifiers={{}}
                    data-v-fffea4e7=""
                  >
                    <option value="core" data-v-fffea4e7="">
                      {"Core"}
                    </option>
                    <option value="agent" data-v-fffea4e7="">
                      {"Agent"}
                    </option>
                  </UiSelect>
                </label>
                {component.value === "agent" ? (
                  <label data-v-fffea4e7="">
                    {t("updates.node")}
                    <UiSelect
                      modelValue={nodeId.value}
                      onUpdateModelValue={(__nextValue: any) => {
                        nodeId.value = __nextValue;
                      }}
                      modelModifiers={{}}
                      data-v-fffea4e7=""
                    >
                      <Fragment>
                        {__each(nodes.value, (node) => (
                          <option
                            key={node.nodeId as any}
                            value={node.nodeId as any}
                            data-v-fffea4e7=""
                          >
                            {node.displayName}
                            {" · "}
                            {node.status}
                          </option>
                        ))}
                      </Fragment>
                    </UiSelect>
                  </label>
                ) : null}
                <label data-v-fffea4e7="">
                  {t("updates.version")}
                  <input
                    placeholder={t("updates.latest") as any}
                    {...__input(
                      version.value,
                      (__nextValue: any) => {
                        version.value = __nextValue;
                      },
                      "",
                      undefined,
                      [],
                    )}
                    data-v-fffea4e7=""
                  ></input>
                </label>
                <button
                  disabled={busy.value || (!selected.value as any)}
                  className={__classes("secondary-button")}
                  onClick={(event: any) => {
                    __call(refresh, event);
                  }}
                  data-v-fffea4e7=""
                >
                  {t("updates.check")}
                </button>
              </div>
              {pollError.value ? (
                <p
                  role="status"
                  className={__classes("muted")}
                  data-v-fffea4e7=""
                >
                  {pollError.value}
                </p>
              ) : null}
              {error.value ? (
                <p
                  role="alert"
                  className={__classes("update-error")}
                  data-v-fffea4e7=""
                >
                  {error.value}
                </p>
              ) : null}
              {check.value ? (
                <div className={__classes("update-details")} data-v-fffea4e7="">
                  <div data-v-fffea4e7="">
                    <span className={__classes("muted")} data-v-fffea4e7="">
                      {t("updates.current")}
                    </span>
                    <strong data-v-fffea4e7="">
                      {check.value.current || "—"}
                    </strong>
                  </div>
                  <div data-v-fffea4e7="">
                    <span className={__classes("muted")} data-v-fffea4e7="">
                      {t("updates.latest")}
                    </span>
                    <strong data-v-fffea4e7="">{check.value.latest}</strong>
                  </div>
                  <div data-v-fffea4e7="">
                    <span className={__classes("muted")} data-v-fffea4e7="">
                      {t("updates.deployment")}
                    </span>
                    <strong data-v-fffea4e7="">
                      {check.value.mode || "—"}
                    </strong>
                  </div>
                  {check.value.reason ? (
                    <p className={__classes("update-error")} data-v-fffea4e7="">
                      {check.value.reason}
                    </p>
                  ) : null}
                  {!check.value.configured ? (
                    <p data-v-fffea4e7="">{t("updates.install")}</p>
                  ) : !check.value.available ? (
                    <p data-v-fffea4e7="">{t("updates.currentNotice")}</p>
                  ) : null}
                  <a
                    href={check.value.release.url as any}
                    target="_blank"
                    rel="noopener noreferrer"
                    data-v-fffea4e7=""
                  >
                    {t("updates.notes")}
                  </a>
                  <details data-v-fffea4e7="">
                    <summary data-v-fffea4e7="">{t("updates.notes")}</summary>
                    <pre data-v-fffea4e7="">{check.value.release.notes}</pre>
                  </details>
                </div>
              ) : null}
              {rollbackJob.value ||
              (check.value?.available &&
                check.value.configured) ? (
                <div className={__classes("update-confirm")} data-v-fffea4e7="">
                  <p data-v-fffea4e7="">
                    {rollbackJob.value
                      ? t("updates.rollbackWarning")
                      : t("updates.updateWarning")}
                  </p>
                  {rollbackJob.value ? (
                    <label data-v-fffea4e7="">
                      <input
                        type="checkbox"
                        {...__input(
                          confirmed.value,
                          (__nextValue: any) => {
                            confirmed.value = __nextValue;
                          },
                          "checkbox",
                          undefined,
                          [],
                        )}
                        data-v-fffea4e7=""
                      ></input>{" "}
                      {t("updates.confirm")}
                    </label>
                  ) : null}
                  <button
                    disabled={
                      busy.value ||
                      (!!rollbackJob.value && (!confirmed.value as any))
                    }
                    className={__classes("primary-button")}
                    onClick={(event: any) => {
                      __call(submit, event);
                    }}
                    data-v-fffea4e7=""
                  >
                    {rollbackJob.value
                      ? t("updates.rollback")
                      : t("updates.apply")}
                  </button>
                  {rollbackJob.value ? (
                    <button
                      className={__classes("secondary-button")}
                      onClick={(event: any) => {
                        rollbackJob.value = "";
                        confirmed.value = false;
                      }}
                      data-v-fffea4e7=""
                    >
                      {t("updates.cancel")}
                    </button>
                  ) : null}
                </div>
              ) : null}
            </div>
            <div className={__classes("update-card")} data-v-fffea4e7="">
              <h2 data-v-fffea4e7="">{t("updates.changelog")}</h2>
              <Fragment>
                {__each(["core", "agent"] as const, (name) => {
                  const entry = changelog.value[name];
                  return (
                    <article
                      key={name}
                      className={__classes("changelog-entry")}
                      data-v-fffea4e7=""
                    >
                      <h3 data-v-fffea4e7="">{name === "core" ? "Core" : "Agent"}</h3>
                      {entry ? (
                        <Fragment>
                          <p
                            className={__classes("changelog-version")}
                            data-v-fffea4e7=""
                          >
                            {entry.latest}
                          </p>
                          <pre data-v-fffea4e7="">{entry.release.notes}</pre>
                        </Fragment>
                      ) : changelogReady.value ? (
                        <p className={__classes("muted")} data-v-fffea4e7="">
                          {t("updates.changelogUnavailable")}
                        </p>
                      ) : (
                        <p className={__classes("muted")} data-v-fffea4e7="">
                          {t("updates.changelogLoading")}
                        </p>
                      )}
                    </article>
                  );
                })}
              </Fragment>
              <h2 data-v-fffea4e7="">{t("updates.history")}</h2>
              {!jobs.value.length ? (
                <p className={__classes("muted")} data-v-fffea4e7="">
                  {t("updates.empty")}
                </p>
              ) : null}
              <Fragment>
                {__each(jobs.value, (job) => (
                  <article
                    key={job.id as any}
                    className={__classes("update-job")}
                    data-v-fffea4e7=""
                  >
                    <div data-v-fffea4e7="">
                      <strong data-v-fffea4e7="">
                        {job.current}
                        {" → "}
                        {job.target}
                      </strong>
                      <span data-v-fffea4e7="">
                        {job.status}
                        {" · "}
                        {job.stage}
                      </span>
                    </div>
                    <p className={__classes("muted")} data-v-fffea4e7="">
                      {new Date(job.started).toLocaleString()}
                      {" · "}
                      {job.id}
                    </p>
                    {job.error ? (
                      <p
                        className={__classes("update-error")}
                        data-v-fffea4e7=""
                      >
                        {job.error}
                      </p>
                    ) : null}
                    {job.status === "succeeded" && job.stage === "complete" ? (
                      <button
                        disabled={busy.value as any}
                        className={__classes("secondary-button")}
                        onClick={(event: any) => {
                          rollbackJob.value = job.id;
                          confirmed.value = false;
                        }}
                        data-v-fffea4e7=""
                      >
                        {t("updates.rollback")}
                      </button>
                    ) : null}
                  </article>
                ))}
              </Fragment>
            </div>
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
