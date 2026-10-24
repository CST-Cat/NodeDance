import { api, operationRequest, type AgentNode } from "../api";
import { t } from "../i18n";
import { useViewNavigation, useViewTab } from "../navigation";
import { finishIntent, intentKey, requestIntent } from "../operation-intent";
import { exactJSON, operationCatalog, type CoreOperation } from "../operations";
import {
  afterMount,
  beforeDispose,
  cell,
  derived,
  observable,
  watchState,
} from "../state";
import { beijingTime } from "../time";
import ContractFields from "./ContractFields";

import { useController } from "../state";
import { SettingsGroup } from "./SettingsGroup";
import { TabsContent } from "./arc/tabs/tabs";
import { Skeleton } from "./arc/skeleton/skeleton";
import "./PersonalSettings.css";
import "./HostManagement.css";
import {
  ManagementPage,
  Button,
  Checkbox,
  Select,
  Alert,
  Tabs,
  TabsList,
  TabsTrigger,
} from "./ManagementUI";
type Props = {} & { children?: import("react").ReactNode; [key: string]: any };
export default function HostManagement(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const nodes = cell<AgentNode[]>([]),
        selected = cell<string[]>([]),
        nodeId = cell(""),
        operations = cell<CoreOperation[]>([]);
      const viewNavigation = useViewNavigation();
      const hostTabs = [
        "settings",
        "assets",
        "commands",
        "probes",
        "enrollment",
      ] as const;
      const tab = useViewTab("tab", "settings", () => hostTabs);
      const busy = cell(false),
        error = cell(""),
        notice = cell("");
      const settings = observable<Record<string, unknown>>({}),
        asset = observable<Record<string, unknown>>({});
      const command = observable<Record<string, unknown>>({
        script: "",
        timeoutSeconds: 60,
        confirm: false,
      });
      const probe = observable<Record<string, unknown>>({
        name: "",
        kind: "icmp",
        target: "icmp://",
        intervalSeconds: 30,
        timeoutSeconds: 3,
        enabled: true,
      });
      const grant = observable<Record<string, unknown>>({
        displayPrefix: "node",
        maxUses: 10,
        lifetimeSeconds: 600,
        coreUrl: window.location.origin,
      });
      interface Job {
        id: string;
        status: string;
        output?: string;
        exitCode?: number;
        error?: string;
        truncated?: boolean;
      }
      interface Batch {
        id: string;
        results: Array<{
          nodeId: string;
          jobId?: string;
          probeId?: string;
          error?: string;
          stale: boolean;
          job?: Job;
        }>;
      }
      interface Grant {
        id: string;
        displayPrefix: string;
        maxUses: number;
        uses: number;
        expiresAt: string;
        revoked: boolean;
        token?: string;
      }
      const batches = cell<Batch[]>([]),
        currentBatch = cell<Batch>(),
        grantList = cell<Grant[]>([]),
        grantResult = cell<{
          grant: Grant;
          installCommand: string;
          dockerInstallCommand: string;
        }>();
      const diagnostics = cell("");
      function schema(name: string) {
        return operations.value.find((o) => o.name === name)?.bodySchema;
      }
      const settingsSchema = derived(() => schema("agents.configure")),
        assetSchema = derived(() => schema("nodes.save-asset"));
      const loadingNode = cell(false);
      const pending = derived(() => busy.value || loadingNode.value);
      let nodeRequest: AbortController | undefined;
      const listRequests = new Map<string, AbortController>();
      let disposed = false;
      beforeDispose(() => {
        disposed = true;
        nodeRequest?.abort();
        for (const request of listRequests.values()) request.abort();
      });
      async function perform(work: () => Promise<void>) {
        if (disposed || busy.value || loadingNode.value) return;
        busy.value = true;
        error.value = "";
        notice.value = "";
        try {
          await work();
        } catch (e) {
          error.value = e instanceof Error ? e.message : String(e);
        } finally {
          busy.value = false;
        }
      }
      async function readList<T>(
        name: string,
        key: string,
        assign: (items: T[]) => void,
      ) {
        listRequests.get(name)?.abort();
        const controller = new AbortController();
        listRequests.set(name, controller);
        try {
          const result = await operationRequest<Record<string, T[]>>(
            name,
            {},
            { signal: controller.signal },
          );
          if (!disposed && !controller.signal.aborted) assign(result[key]!);
        } catch (reason) {
          if (!disposed && !controller.signal.aborted) throw reason;
        } finally {
          if (listRequests.get(name) === controller) listRequests.delete(name);
        }
      }
      async function loadNode() {
        nodeRequest?.abort();
        const controller = new AbortController();
        nodeRequest = controller;
        const id = nodeId.value;
        diagnostics.value = "";
        loadingNode.value = true;
        error.value = "";
        notice.value = "";
        for (const value of [settings, asset])
          for (const k of Object.keys(value)) delete value[k];
        try {
          const results = await Promise.allSettled([
            operationRequest<{ settings: Record<string, unknown> }>(
              "agents.settings",
              { params: { nodeId: id } },
              { signal: controller.signal },
            ),
            operationRequest<Record<string, unknown>>(
              "nodes.asset",
              { params: { nodeId: id } },
              { signal: controller.signal },
            ),
          ]);
          if (disposed || nodeRequest !== controller) return;
          if (results[0].status === "fulfilled")
            Object.assign(settings, results[0].value.settings);
          if (results[1].status === "fulfilled")
            Object.assign(asset, results[1].value);
          const failed = results.find((r) => r.status === "rejected");
          if (failed?.status === "rejected") throw failed.reason;
        } catch (reason) {
          if (!disposed && nodeRequest === controller)
            error.value =
              reason instanceof Error ? reason.message : String(reason);
        } finally {
          if (!disposed && nodeRequest === controller)
            loadingNode.value = false;
        }
      }
      async function save(name: string, body: Record<string, unknown>) {
        await perform(async () => {
          const data = {
            ...body,
          };
          for (const [key, field] of Object.entries(
            schema(name)?.properties ?? {},
          ))
            if (
              field.nullable &&
              field.format === "date-time" &&
              data[key] === ""
            )
              data[key] = null;
          await operationRequest(
            name,
            {
              params: {
                nodeId: nodeId.value,
              },
            },
            {
              body: exactJSON(data),
            },
          );
          notice.value = t("host.saved");
        });
      }
      async function submitBatch(
        name: "commands.batch" | "probes.batch",
        body: Record<string, unknown>,
      ) {
        await perform(async () => {
          const payload = exactJSON(body),
            intent = await requestIntent(name, {}, payload);
          if (disposed) return;
          const result = await operationRequest<Batch>(
            name,
            {},
            {
              headers: {
                "Idempotency-Key": intentKey(intent),
              },
              body: payload,
            },
          );
          finishIntent(intent);
          if (disposed) return;
          currentBatch.value = result;
          if (name === "commands.batch")
            await readList<Batch>("commands.batches", "batches", (items) => {
              batches.value = items;
            });
          else notice.value = t("host.saved");
        });
      }
      async function submitCommand() {
        await submitBatch("commands.batch", {
          ...command,
          nodeIds: [...selected.value],
        });
      }
      async function submitProbe() {
        const body: Record<string, unknown> = {
          ...probe,
          nodeIds: [...selected.value],
        };
        if (!["http", "https"].includes(String(probe.kind)))
          delete body.expectedHttpStatus;
        await submitBatch("probes.batch", body);
      }
      async function inspectBatch(id: string, cancel = false) {
        await perform(async () => {
          currentBatch.value = await operationRequest<Batch>(
            cancel ? "commands.batch-cancel" : "commands.batch-status",
            {
              params: {
                batchId: id,
              },
            },
          );
        });
      }
      async function createGrant() {
        await perform(async () => {
          const result = await operationRequest<typeof grantResult.value>(
            "agents.create-grant",
            {},
            {
              body: exactJSON(grant),
            },
          );
          if (disposed) return;
          grantResult.value = result;
          await readList<Grant>("agents.grants", "grants", (items) => {
            grantList.value = items;
          });
        });
      }
      async function revokeGrant(id: string) {
        await perform(async () => {
          await operationRequest("agents.revoke-grant", {
            params: {
              grantId: id,
            },
          });
          if (disposed) return;
          await readList<Grant>("agents.grants", "grants", (items) => {
            grantList.value = items;
          });
        });
      }
      function selectNode() {
        viewNavigation.go({
          ...viewNavigation.route.value,
          nodeId: nodeId.value || undefined,
        });
      }
      watchState(
        () => viewNavigation.route.value.nodeId,
        (id) => {
          if (!nodes.value.length) return;
          const next =
            nodes.value.find((n) => n.nodeId === id)?.nodeId ??
            nodes.value[0]!.nodeId;
          if (next !== nodeId.value) {
            nodeId.value = next;
          }
        },
      );
      watchState(
        [nodeId, tab],
        () => {
          if (
            nodeId.value &&
            (tab.value === "settings" || tab.value === "assets")
          ) {
            void loadNode();
          } else {
            nodeRequest?.abort();
            nodeRequest = undefined;
            loadingNode.value = false;
          }
        },
        { immediate: true },
      );
      watchState(
        tab,
        (current) => {
          error.value = "";
          const request =
            current === "enrollment"
              ? readList<Grant>("agents.grants", "grants", (items) => {
                  grantList.value = items;
                })
              : current === "commands" || current === "probes"
                ? readList<Batch>("commands.batches", "batches", (items) => {
                    batches.value = items;
                  })
                : undefined;
          void request?.catch((reason) => {
            if (!disposed && tab.value === current)
              error.value =
                reason instanceof Error ? reason.message : String(reason);
          });
        },
        { immediate: true },
      );
      afterMount(async () => {
        await perform(async () => {
          const [list, catalog] = await Promise.all([
            api.nodes(),
            operationCatalog(),
          ]);
          if (disposed) return;
          nodes.value = list.nodes.filter(
            (n) => !["pending", "revoked"].includes(n.status),
          );
          operations.value = catalog.operations;
          nodeId.value =
            nodes.value.find(
              (n) => n.nodeId === viewNavigation.route.value.nodeId,
            )?.nodeId ??
            nodes.value[0]?.nodeId ??
            "";
        });
      });
      const heading = (text: string) => (
        <div className="workspace-section-heading">
          <h2>{text}</h2>
        </div>
      );
      return () => (
        <ManagementPage
          registryStyles
          className="host-page preferences-page workspace-settings"
        >
          <header className="workspace-heading">
            <h1>{t("host.title")}</h1>
          </header>
          {error.value ? (
            <Alert
              role="alert"
              className="host-error"
              tone="danger"
              title={t("management.error")}
            >
              {error.value}
            </Alert>
          ) : null}
          {notice.value ? (
            <Alert role="status" tone="info" title={t("management.notice")}>
              {notice.value}
            </Alert>
          ) : null}
          <Tabs
            className="host-tabs"
            value={tab.value}
            onValueChange={(value) => {
              tab.value = value as typeof tab.value;
            }}
          >
            <TabsList
              aria-label={t("host.title")}
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
              {hostTabs.map((item) => (
                <TabsTrigger key={item} value={item}>
                  {t(`host.tab.${item}`)}
                </TabsTrigger>
              ))}
            </TabsList>
            <TabsContent
              value={tab.value}
              className="settings-sections host-sections"
            >
              {tab.value === "settings" || tab.value === "assets" ? (
                <>
                  <section className="workspace-section">
                    <SettingsGroup>
                      <div className="parameter-row host-node">
                        <label htmlFor="host-node-picker">
                          {t("host.node")}
                        </label>
                        <Select
                          id="host-node-picker"
                          aria-label={t("host.node")}
                          disabled={pending.value}
                          value={nodeId.value}
                          onValueChange={(value) => {
                            nodeId.value = value;
                            selectNode();
                          }}
                          options={nodes.value.map((node) => ({
                            value: node.nodeId,
                            label: `${node.displayName} · ${node.status}`,
                          }))}
                        />
                      </div>
                    </SettingsGroup>
                  </section>
                  {nodeId.value ? (
                    <form
                      className="workspace-section host-card host-settings-form"
                      onSubmit={(event) => {
                        event.preventDefault();
                        void save(
                          tab.value === "settings"
                            ? "agents.configure"
                            : "nodes.save-asset",
                          tab.value === "settings" ? settings : asset,
                        );
                      }}
                    >
                      {heading(t(`host.tab.${tab.value}`))}
                      <p className="host-hint">
                        {t(
                          tab.value === "settings"
                            ? "host.settingsHint"
                            : "host.assetsHint",
                        )}
                      </p>
                      {loadingNode.value ? (
                        <Skeleton label={t("load.initial")} lines={3} />
                      ) : null}
                      <ContractFields
                        schema={
                          tab.value === "settings"
                            ? settingsSchema.value
                            : assetSchema.value
                        }
                        values={tab.value === "settings" ? settings : asset}
                        disabled={pending.value}
                      />
                      <div className="settings-section-actions host-actions">
                        {tab.value === "settings" ? (
                          <Button
                            type="button"
                            variant="secondary"
                            disabled={pending.value}
                            onClick={() =>
                              void perform(async () => {
                                diagnostics.value = JSON.stringify(
                                  await operationRequest("agents.diagnostics", {
                                    params: { nodeId: nodeId.value },
                                  }),
                                  null,
                                  2,
                                );
                              })
                            }
                          >
                            {t("host.diagnostics")}
                          </Button>
                        ) : null}
                        <Button
                          type="submit"
                          className="primary-button"
                          disabled={
                            pending.value ||
                            !Object.keys(
                              tab.value === "settings" ? settings : asset,
                            ).length
                          }
                        >
                          {t("host.save")}
                        </Button>
                      </div>
                    </form>
                  ) : (
                    <SettingsGroup>
                      <p>{t("host.noNodes")}</p>
                    </SettingsGroup>
                  )}
                  {tab.value === "settings" && diagnostics.value ? (
                    <section className="workspace-section">
                      {heading(t("host.diagnostics"))}
                      <SettingsGroup>
                        <pre>{diagnostics.value}</pre>
                      </SettingsGroup>
                    </section>
                  ) : null}
                </>
              ) : tab.value === "commands" || tab.value === "probes" ? (
                <>
                  <section className="workspace-section">
                    {heading(t("host.nodes"))}
                    <p className="host-hint">{t("host.selectionHint")}</p>
                    <SettingsGroup>
                      {nodes.value.length ? (
                        nodes.value.map((node) => (
                          <div
                            key={node.nodeId}
                            className="parameter-row host-check"
                          >
                            <label htmlFor={`host-select-${node.nodeId}`}>
                              {node.displayName} · {node.status}
                            </label>
                            <div className="host-toggle">
                              <Checkbox
                                id={`host-select-${node.nodeId}`}
                                aria-label={node.displayName}
                                disabled={pending.value}
                                checked={selected.value.includes(node.nodeId)}
                                onCheckedChange={(checked) => {
                                  selected.value =
                                    checked === true
                                      ? [...selected.value, node.nodeId]
                                      : selected.value.filter(
                                          (id) => id !== node.nodeId,
                                        );
                                }}
                              />
                            </div>
                          </div>
                        ))
                      ) : (
                        <p>{t("host.noNodes")}</p>
                      )}
                    </SettingsGroup>
                  </section>
                  <form
                    className="workspace-section host-card host-batch-form"
                    onSubmit={(event) => {
                      event.preventDefault();
                      void (tab.value === "commands"
                        ? submitCommand()
                        : submitProbe());
                    }}
                  >
                    {heading(t(`host.tab.${tab.value}`))}
                    <p className="host-hint">
                      {t(
                        tab.value === "commands"
                          ? "host.commandHint"
                          : "host.probeHint",
                      )}
                    </p>
                    <ContractFields
                      schema={schema(
                        tab.value === "commands"
                          ? "commands.batch"
                          : "probes.batch",
                      )}
                      values={tab.value === "commands" ? command : probe}
                      omit={
                        tab.value === "commands"
                          ? ["nodeIds"]
                          : [
                              "nodeIds",
                              ...(["http", "https"].includes(String(probe.kind))
                                ? []
                                : ["expectedHttpStatus"]),
                            ]
                      }
                      disabled={pending.value}
                    />
                    <div className="settings-section-actions host-actions">
                      <Button
                        type="submit"
                        className="primary-button"
                        disabled={pending.value || !selected.value.length}
                      >
                        {t("host.submit")} · {selected.value.length}
                      </Button>
                    </div>
                  </form>
                  {currentBatch.value ? (
                    <section className="workspace-section host-batch-result">
                      <div className="workspace-section-heading">
                        <h2>{t("host.results")}</h2>
                      </div>
                      <code>{currentBatch.value.id}</code>
                      {currentBatch.value.results.some(
                        (result) => result.jobId,
                      ) ? (
                        <div className="settings-section-actions">
                          <Button
                            variant="secondary"
                            disabled={pending.value}
                            onClick={() =>
                              void inspectBatch(currentBatch.value!.id)
                            }
                          >
                            {t("host.refresh")}
                          </Button>
                          <Button
                            variant="secondary"
                            disabled={pending.value}
                            onClick={() =>
                              void inspectBatch(currentBatch.value!.id, true)
                            }
                          >
                            {t("host.cancel")}
                          </Button>
                        </div>
                      ) : null}
                      {currentBatch.value.results.length ? (
                        <SettingsGroup>
                          {currentBatch.value.results.map((result) => (
                            <article
                              key={result.nodeId}
                              className="host-result"
                            >
                              <strong>
                                {nodes.value.find(
                                  (node) => node.nodeId === result.nodeId,
                                )?.displayName ?? result.nodeId}
                              </strong>
                              <div className="host-result-status">
                                <span>
                                  {result.job?.status
                                    ? t(`task.state.${result.job.status}`) ===
                                      `task.state.${result.job.status}`
                                      ? result.job.status
                                      : t(`task.state.${result.job.status}`)
                                    : result.probeId || result.jobId || "—"}
                                </span>
                                {result.stale ? (
                                  <span>{t("host.stale")}</span>
                                ) : null}
                              </div>
                              {result.error || result.job?.error ? (
                                <Alert
                                  tone="danger"
                                  title={t("management.error")}
                                >
                                  {result.error || result.job?.error}
                                </Alert>
                              ) : null}
                              {result.job?.exitCode !== undefined ? (
                                <p>
                                  {t("host.exit")}: {result.job.exitCode}
                                </p>
                              ) : null}
                              {result.job?.output ? (
                                <pre>{result.job.output}</pre>
                              ) : null}
                              {result.job?.truncated ? (
                                <p>{t("host.truncated")}</p>
                              ) : null}
                            </article>
                          ))}
                        </SettingsGroup>
                      ) : null}
                    </section>
                  ) : null}
                  {tab.value === "commands" && batches.value.length ? (
                    <section className="workspace-section">
                      {heading(t("host.history"))}
                      <SettingsGroup>
                        {batches.value.map((batch) => (
                          <div key={batch.id} className="parameter-row">
                            <span className="host-history-id">{batch.id}</span>
                            <div className="settings-section-actions">
                              <Button
                                variant="secondary"
                                disabled={pending.value}
                                onClick={() => void inspectBatch(batch.id)}
                              >
                                {t("host.results")} · {batch.results.length}
                              </Button>
                            </div>
                          </div>
                        ))}
                      </SettingsGroup>
                    </section>
                  ) : null}
                </>
              ) : (
                <>
                  <form
                    className="workspace-section host-card host-grant-form"
                    onSubmit={(event) => {
                      event.preventDefault();
                      void createGrant();
                    }}
                  >
                    {heading(t("host.tab.enrollment"))}
                    <p className="host-hint">{t("host.enrollmentHint")}</p>
                    <ContractFields
                      schema={schema("agents.create-grant")}
                      values={grant}
                      disabled={pending.value}
                    />
                    <div className="settings-section-actions host-actions">
                      <Button
                        type="submit"
                        className="primary-button"
                        disabled={pending.value}
                      >
                        {t("host.submit")}
                      </Button>
                    </div>
                  </form>
                  {grantResult.value ? (
                    <section className="workspace-section host-grant-result">
                      {heading(t("host.enrollmentResult"))}
                      <p className="host-hint">{t("host.secretHint")}</p>
                      <SettingsGroup>
                        <pre>{grantResult.value.grant.token}</pre>
                        <pre>{grantResult.value.installCommand}</pre>
                        <pre>{grantResult.value.dockerInstallCommand}</pre>
                      </SettingsGroup>
                    </section>
                  ) : null}
                  {grantList.value.length ? (
                    <section className="workspace-section host-grant-list">
                      {heading(t("host.grants"))}
                      <SettingsGroup>
                        {grantList.value.map((grant) => (
                          <article key={grant.id} className="host-result">
                            <strong>{grant.displayPrefix}</strong>
                            <p>
                              {grant.uses} / {grant.maxUses}
                            </p>
                            <time>
                              {beijingTime(grant.expiresAt)} ·{" "}
                              {t("time.beijing")}
                            </time>
                            <div className="settings-section-actions">
                              <Button
                                variant="secondary"
                                disabled={pending.value || grant.revoked}
                                onClick={() => void revokeGrant(grant.id)}
                              >
                                {t("host.revoke")}
                              </Button>
                            </div>
                          </article>
                        ))}
                      </SettingsGroup>
                    </section>
                  ) : null}
                </>
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
