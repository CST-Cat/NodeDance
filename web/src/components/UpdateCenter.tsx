import { ManagementPage } from "./ManagementUI";
import "./PersonalSettings.css";
import { api, operationRequest, type AgentNode } from "../api";
import { locale, t } from "../i18n";
import {
  afterMount,
  beforeDispose,
  cell,
  derived,
  watchState,
  useController,
} from "../state";
import { Dialog, DialogContent, DialogTrigger } from "./arc/dialog/dialog";
import { useRef, type ReactNode } from "react";
import {
  AnimatePresence,
  motion,
  useReducedMotion,
  useIsPresent,
} from "motion/react";
import { motionTokens } from "./arc/lib/motion-tokens";
import { Skeleton } from "./arc/skeleton/skeleton";
import { Button, Input, Select, Badge, Alert } from "./ManagementUI";
import { SettingsGroup } from "./SettingsGroup";
function releaseItems(notes: string) {
  const sections = notes.split(/^### (?:English|简体中文)\s*$/m);
  const text = sections[locale.value === "zh-CN" ? 2 : 1] ?? notes;
  return text
    .trim()
    .split(/\n(?=- )/)
    .map((item) => item.trim().replace(/^- /, ""));
}
function UpdateRecord({ children }: { children: ReactNode }) {
  const present = useIsPresent();
  const reduced = useReducedMotion();
  return (
    <motion.div
      className="update-record-slot"
      inert={!present}
      aria-hidden={!present || undefined}
      initial={false}
      animate={{ height: "auto" }}
      exit={{ height: 0 }}
      transition={
        reduced
          ? { duration: 0 }
          : {
              height: {
                ...motionTokens.spring.smooth,
                delay:
                  motionTokens.duration.fast + motionTokens.duration.instant,
              },
            }
      }
    >
      <motion.article
        className="update-job settings-detail-list"
        initial={false}
        exit={reduced ? { opacity: 0 } : { x: "100%", opacity: 0 }}
        transition={
          reduced
            ? { duration: 0 }
            : {
                x: {
                  ...motionTokens.spring.smooth,
                  delay: motionTokens.duration.fast,
                },
                opacity: {
                  duration: motionTokens.duration.fast,
                  delay: motionTokens.duration.fast,
                },
              }
        }
      >
        {children}
      </motion.article>
    </motion.div>
  );
}
export default function UpdateCenter() {
  const dialogContainer = useRef<HTMLElement>(null);
  return useController(
    () => {
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
        backupDeleting?: boolean;
        backupDeleted?: boolean;
      }
      const component = cell("core"),
        nodeId = cell(""),
        version = cell("");
      const nodes = cell<AgentNode[]>([]),
        check = cell<ReleaseCheck>(),
        jobs = cell<Job[]>([]);
      const visibleJobs = derived(() =>
        jobs.value.filter((job) => !job.backupDeleted),
      );
      const historyMounted = cell(false);
      watchState(visibleJobs, (value) => {
        if (value.length) historyMounted.value = true;
      });
      const checking = cell(false);
      let checkSequence = 0;
      const pollError = cell("");
      const error = cell(""),
        busy = cell(false),
        rollbackJob = cell("");
      const rollbackError = cell("");
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
            jobs.value = result.jobs;
            pollError.value = "";
          }
        } catch (e) {
          if (current === epoch) pollError.value = message(e);
        } finally {
          if (!stopped && current === epoch)
            timer = setTimeout(
              history,
              jobs.value.some((job) => !job.finished) ? 3000 : 15000,
            );
        }
      }
      interface PublishedRelease {
        component: string;
        version: string;
        url: string;
        notes: string;
      }
      const changelog = cell<
        Partial<Record<"core" | "agent", PublishedRelease>>
      >({});
      const changelogError = cell<Partial<Record<"core" | "agent", string>>>(
        {},
      );
      async function loadChangelog(component: "core" | "agent") {
        changelogError.value = { ...changelogError.value, [component]: "" };
        try {
          const release = await operationRequest<PublishedRelease>(
            "updates.release",
            { query: { component } },
          );
          if (!stopped)
            changelog.value = { ...changelog.value, [component]: release };
        } catch (e) {
          if (!stopped)
            changelogError.value = {
              ...changelogError.value,
              [component]: message(e),
            };
        }
      }
      const deleteJob = cell("");
      const deleteError = cell("");
      async function deleteBackup() {
        if (busy.value || !deleteJob.value) return;
        const id = deleteJob.value,
          current = ++epoch;
        if (timer) clearTimeout(timer);
        busy.value = true;
        deleteError.value = "";
        try {
          const deleted = await operationRequest<Job>(
            "updates.delete-backup",
            {},
            {
              body: JSON.stringify({
                ...query.value,
                jobId: id,
                confirm: true,
              }),
            },
          );
          if (stopped || current !== epoch) return;
          deleteJob.value = "";
          jobs.value = jobs.value.map((job) => (job.id === id ? deleted : job));
        } catch (e) {
          if (!stopped && current === epoch) deleteError.value = message(e);
        } finally {
          if (!stopped && current === epoch) {
            busy.value = false;
            void history();
          }
        }
      }
      async function refresh() {
        if (!selected.value || checking.value || busy.value) return;
        error.value = "";
        checking.value = true;
        const sequence = ++checkSequence;
        const current = epoch,
          requestedVersion = version.value;
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
          if (current === epoch && sequence === checkSequence && requestedVersion === version.value) {
            check.value = result;
          }
        } catch (e) {
          if (current === epoch && sequence === checkSequence) error.value = message(e);
        } finally {
          if (current === epoch && sequence === checkSequence) checking.value = false;
        }
      }
      async function submit(rollback = false) {
        if (
          busy.value ||
          !selected.value ||
          (rollback && !rollbackJob.value)
        )
          return;
        busy.value = true;
        error.value = "";
        checkSequence++;
        checking.value = false;
        const action = rollback ? "rollback" : "apply";
        rollbackError.value = "";
        const body = {
          ...query.value,
          confirm: true,
          ...(rollback
            ? {
                jobId: rollbackJob.value,
              }
            : {
                ...(version.value.trim() ? { version: version.value.trim() } : {}),
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
          rollbackJob.value = "";
        } catch (e) {
          if (current === epoch) {
            if (rollback) rollbackError.value = message(e);
            else error.value = message(e);
          }
        } finally {
          if (current === epoch) busy.value = false;
        }
      }
      watchState(query, () => {
        epoch++;
        check.value = undefined;
        checkSequence++;
        checking.value = false;
        jobs.value = [];
        historyMounted.value = false;
        rollbackJob.value = "";
        rollbackError.value = "";
        deleteJob.value = "";
        deleteError.value = "";
        if (timer) clearTimeout(timer);
        void history();
      });
      afterMount(() => {
        void api.nodes().then((result) => {
          if (stopped) return;
          nodes.value = result.nodes;
          nodeId.value = nodes.value[0]?.nodeId ?? "";
        }).catch((e) => {
          if (!stopped) error.value = message(e);
        });
        void loadChangelog("core");
        void loadChangelog("agent");
        void history();
      });
      beforeDispose(() => {
        stopped = true;
        epoch++;
        if (timer) clearTimeout(timer);
      });
      return () => (
        <ManagementPage
          registryStyles
          className="preferences-page workspace-settings"
        >
          <header className="workspace-heading">
            <h1>{t("updates.title")}</h1>
          </header>
          <div className="settings-sections">
            <section
              ref={dialogContainer}
              className="workspace-section settings-section settings-integrated update-center"
              aria-labelledby="settings-updates-heading"
            >
              <div className="workspace-section-heading">
                <h2 id="settings-updates-heading">
                  {t("management.updateTarget")}
                </h2>
              </div>
              {pollError.value ? (
                <Alert role="status" title={t("management.notice")}>
                  {pollError.value}
                </Alert>
              ) : null}
              {error.value ? (
                <Alert role="alert" tone="danger" title={t("management.error")}>
                  {error.value}
                </Alert>
              ) : null}
              <SettingsGroup className="update-controls update-card">
                <div className="parameter-row">
                  <label htmlFor="update-component">
                    {t("updates.component")}
                  </label>
                  <div className="update-component-control">
                    <Select
                      id="update-component"
                      aria-label={t("updates.component")}
                      value={component.value}
                      onValueChange={(value) => {
                        component.value = value;
                      }}
                      options={[
                        { value: "core", label: "Core" },
                        { value: "agent", label: "Agent" },
                      ]}
                    />
                  </div>
                </div>
                {component.value === "agent" ? (
                  <div className="parameter-row">
                    <label htmlFor="update-node">{t("updates.node")}</label>
                    <Select
                      id="update-node"
                      aria-label={t("updates.node")}
                      value={nodeId.value}
                      onValueChange={(value) => {
                        nodeId.value = value;
                      }}
                      options={nodes.value.map((node) => ({
                        value: node.nodeId,
                        label: `${node.displayName} · ${node.status}`,
                      }))}
                    />
                  </div>
                ) : null}
                <label className="parameter-row">
                  <span>{t("updates.version")}</span>
                  <Input
                    value={version.value}
                    placeholder={t("updates.latest")}
                    onChange={(event) => {
                      version.value = event.target.value;
                      checkSequence++;
                      checking.value = false;
                      check.value = undefined;
                    }}
                  />
                </label>
                {check.value ? (
                  <div className="update-details settings-detail-list">
                    <div className="parameter-row">
                      <span>{t("updates.current")}</span>
                      <span>{check.value.current || "—"}</span>
                    </div>
                    <div className="parameter-row">
                      <span>{t("updates.latest")}</span>
                      <span>{check.value.latest}</span>
                    </div>
                    <div className="parameter-row">
                      <span>{t("updates.deployment")}</span>
                      <span>{check.value.mode || "—"}</span>
                    </div>
                    {check.value.reason ? (
                      <p role="status">{check.value.reason}</p>
                    ) : null}
                    {!check.value.configured ? (
                      <p>{t("updates.install")}</p>
                    ) : !check.value.available ? (
                      <p>{t("updates.currentNotice")}</p>
                    ) : null}
                  </div>
                ) : null}
                {checking.value && !error.value ? (
                  <Skeleton
                    label={t(check.value ? "load.refreshing" : "load.initial")}
                    lines={check.value ? 1 : 3}
                  />
                ) : null}
              </SettingsGroup>
              <div className="settings-section-actions update-target-actions">
                {selected.value ? (
                  <Button
                    className="update-submit"
                    disabled={busy.value}
                    onClick={() => void submit()}
                  >
                    {t("updates.apply")}
                  </Button>
                ) : null}
                <Button
                  disabled={busy.value || checking.value || !selected.value}
                  onClick={() => void refresh()}
                >
                  {t("updates.check")}
                </Button>
              </div>
            </section>
            <section
              className="workspace-section settings-section settings-integrated update-changelog"
              aria-labelledby="update-changelog-heading"
            >
              <div className="workspace-section-heading">
                <h2 id="update-changelog-heading">{t("updates.changelog")}</h2>
              </div>
              {(["core", "agent"] as const).map((name) => (
                <div className="settings-detail-list" key={name}>
                  <div className="settings-record-row">
                    <h3>{name === "core" ? "Core" : "Agent"}</h3>
                    {changelog.value[name] ? (
                      <a
                        href={changelog.value[name]!.url}
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        {changelog.value[name]!.version}
                      </a>
                    ) : null}
                  </div>
                  {changelogError.value[name] ? (
                    <>
                      <Alert
                        role="alert"
                        tone="danger"
                        title={t("management.error")}
                      >
                        {changelogError.value[name]}
                      </Alert>
                      <div className="settings-section-actions">
                        <Button
                          variant="secondary"
                          onClick={() => void loadChangelog(name)}
                        >
                          {t("updates.retry")}
                        </Button>
                      </div>
                    </>
                  ) : changelog.value[name] ? (
                    <ul className="release-notes">
                      {releaseItems(changelog.value[name]!.notes).map(
                        (item, index) => (
                          <li key={index}>{item}</li>
                        ),
                      )}
                    </ul>
                  ) : (
                    <Skeleton label={t("updates.changelogLoading")} lines={3} />
                  )}
                </div>
              ))}
            </section>
            {historyMounted.value ? (
              <section
                className="workspace-section settings-section settings-integrated update-history"
                aria-labelledby="update-history-heading"
              >
                <div className="workspace-section-heading">
                  <h2 id="update-history-heading">{t("updates.history")}</h2>
                </div>
                <SettingsGroup>
                  <div className="update-records">
                    <AnimatePresence
                      initial={false}
                      onExitComplete={() => {
                        if (!visibleJobs.value.length)
                          historyMounted.value = false;
                      }}
                    >
                      {visibleJobs.value.map((job) => (
                        <UpdateRecord key={job.id}>
                          <strong>
                            {job.current} → {job.target}
                          </strong>
                          <span>
                            {new Date(job.started).toLocaleString()} · {job.id}
                          </span>
                          <div
                            className="settings-record-status"
                            role="status"
                            data-status={job.status}
                          >
                            <Badge
                              tone={
                                ["failed", "rollback_failed"].includes(
                                  job.status,
                                )
                                  ? "danger"
                                  : job.status === "succeeded"
                                    ? "success"
                                    : "neutral"
                              }
                            >
                              {t(`task.state.${job.status}`) ===
                              `task.state.${job.status}`
                                ? job.status
                                : t(`task.state.${job.status}`)}
                            </Badge>
                            {!job.finished &&
                            ["queued", "running"].includes(job.status) &&
                            job.stage !== job.status ? (
                              <span>{job.stage}</span>
                            ) : null}
                          </div>
                          {job.error ? (
                            <Alert tone="danger" title={t("management.error")}>
                              {job.error}
                            </Alert>
                          ) : null}
                          <div className="settings-section-actions">
                            {job.status === "succeeded" &&
                            job.stage === "complete" &&
                            !job.backupDeleting ? (
                              <Dialog
                                open={rollbackJob.value === job.id}
                                onOpenChange={(open) => {
                                  rollbackJob.value = open ? job.id : "";
                                  rollbackError.value = "";
                                }}
                              >
                                <DialogTrigger asChild>
                                  <Button
                                    variant="secondary"
                                    disabled={busy.value}
                                  >
                                    {t("updates.rollback")}
                                  </Button>
                                </DialogTrigger>
                                <DialogContent
                                  container={dialogContainer.current}
                                  className="update-confirm-dialog"
                                  title={t("updates.rollback")}
                                  description={t("updates.rollbackWarning")}
                                >
                                  {rollbackError.value ? (
                                    <Alert
                                      role="alert"
                                      tone="danger"
                                      title={t("management.error")}
                                    >
                                      {rollbackError.value}
                                    </Alert>
                                  ) : null}
                                  <div className="settings-section-actions">
                                    <Button
                                      variant="secondary"
                                      onClick={() => {
                                        rollbackJob.value = "";
                                      }}
                                    >
                                      {t("updates.cancel")}
                                    </Button>
                                    <Button
                                      disabled={busy.value}
                                      onClick={() => void submit(true)}
                                    >
                                      {t("updates.rollback")}
                                    </Button>
                                  </div>
                                </DialogContent>
                              </Dialog>
                            ) : null}
                            {job.finished &&
                            ["succeeded", "rolled_back", "failed"].includes(
                              job.status,
                            ) ? (
                              <Dialog
                                open={deleteJob.value === job.id}
                                onOpenChange={(open) => {
                                  deleteJob.value = open ? job.id : "";
                                  deleteError.value = "";
                                }}
                              >
                                <DialogTrigger asChild>
                                  <Button
                                    variant="secondary"
                                    disabled={busy.value}
                                  >
                                    {t("updates.deleteBackup")}
                                  </Button>
                                </DialogTrigger>
                                <DialogContent
                                  container={dialogContainer.current}
                                  className="update-confirm-dialog"
                                  title={t("updates.deleteBackup")}
                                  description={t("updates.deleteBackupConfirm")}
                                >
                                  {deleteError.value ? (
                                    <Alert
                                      role="alert"
                                      tone="danger"
                                      title={t("management.error")}
                                    >
                                      {deleteError.value}
                                    </Alert>
                                  ) : null}
                                  <div className="settings-section-actions">
                                    <Button
                                      variant="secondary"
                                      onClick={() => {
                                        deleteJob.value = "";
                                      }}
                                    >
                                      {t("updates.cancel")}
                                    </Button>
                                    <Button
                                      disabled={busy.value}
                                      onClick={() => void deleteBackup()}
                                    >
                                      {t("updates.deleteBackup")}
                                    </Button>
                                  </div>
                                </DialogContent>
                              </Dialog>
                            ) : null}
                          </div>
                        </UpdateRecord>
                      ))}
                    </AnimatePresence>
                  </div>
                </SettingsGroup>
              </section>
            ) : null}
          </div>
        </ManagementPage>
      );
    },
    {},
    [],
    [],
    {},
    true,
  );
}
