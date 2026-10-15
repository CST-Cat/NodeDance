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

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import "./HostManagement.css";
import UiSelect from "./UiSelect";
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
      let disposed = false;
      beforeDispose(() => {
        disposed = true;
      });
      async function perform(work: () => Promise<void>) {
        if (disposed || busy.value) return;
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
      async function loadNode() {
        await perform(async () => {
          const id = nodeId.value;
          for (const value of [settings, asset])
            for (const k of Object.keys(value)) delete value[k];
          const results = await Promise.allSettled([
            operationRequest<{
              settings: Record<string, unknown>;
            }>("agents.settings", {
              params: {
                nodeId: id,
              },
            }),
            operationRequest<Record<string, unknown>>("nodes.asset", {
              params: {
                nodeId: id,
              },
            }),
          ]);
          if (results[0].status === "fulfilled")
            Object.assign(settings, results[0].value.settings);
          if (results[1].status === "fulfilled")
            Object.assign(asset, results[1].value);
          const failed = results.find((r) => r.status === "rejected");
          if (failed?.status === "rejected") throw failed.reason;
        });
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
            batches.value = (
              await operationRequest<{
                batches: Batch[];
              }>("commands.batches")
            ).batches;
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
          grantList.value = (
            await operationRequest<{
              grants: Grant[];
            }>("agents.grants")
          ).grants;
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
          grantList.value = (
            await operationRequest<{
              grants: Grant[];
            }>("agents.grants")
          ).grants;
        });
      }
      function selectNode() {
        viewNavigation.go({
          ...viewNavigation.route.value,
          nodeId: nodeId.value || undefined,
        });
        void loadNode();
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
            void loadNode();
          }
        },
      );
      afterMount(async () => {
        await perform(async () => {
          const [list, catalog, history, grants] = await Promise.all([
            api.nodes(),
            operationCatalog(),
            operationRequest<{
              batches: Batch[];
            }>("commands.batches"),
            operationRequest<{
              grants: Grant[];
            }>("agents.grants"),
          ]);
          if (disposed) return;
          nodes.value = list.nodes.filter(
            (n) => !["pending", "revoked"].includes(n.status),
          );
          nodeId.value =
            nodes.value.find(
              (n) => n.nodeId === viewNavigation.route.value.nodeId,
            )?.nodeId ??
            nodes.value[0]?.nodeId ??
            "";
          operations.value = catalog.operations;
          batches.value = history.batches;
          grantList.value = grants.grants;
        });
        if (nodeId.value) await loadNode();
      });
      return () => (
        <>
          <section
            className={__classes("host-page workspace-page")}
            data-v-bd412aaa=""
          >
            <header data-v-bd412aaa="">
              <div data-v-bd412aaa="">
                <h1 data-v-bd412aaa="">{t("host.title")}</h1>
                <p data-v-bd412aaa="">{t("host.subtitle")}</p>
              </div>
            </header>
            <nav
              aria-label={t("host.title") as any}
              className={__classes("host-tabs")}
              data-v-bd412aaa=""
            >
              <Fragment>
                {__each(hostTabs, (item) => (
                  <button
                    key={item as any}
                    aria-pressed={tab.value === (item as any)}
                    onClick={(event: any) => {
                      tab.value = item;
                    }}
                    data-v-bd412aaa=""
                  >
                    {t(`host.tab.${item}`)}
                  </button>
                ))}
              </Fragment>
            </nav>
            {error.value ? (
              <p
                role="alert"
                className={__classes("host-error")}
                data-v-bd412aaa=""
              >
                {error.value}
              </p>
            ) : null}
            {notice.value ? (
              <p role="status" data-v-bd412aaa="">
                {notice.value}
              </p>
            ) : null}
            {tab.value === "settings" || tab.value === "assets" ? (
              <section className={__classes("host-card")} data-v-bd412aaa="">
                <label className={__classes("host-node")} data-v-bd412aaa="">
                  {t("host.node")}
                  <UiSelect
                    disabled={busy.value as any}
                    onChange={(event: any) => {
                      __call(selectNode, event);
                    }}
                    modelValue={nodeId.value}
                    onUpdateModelValue={(__nextValue: any) => {
                      nodeId.value = __nextValue;
                    }}
                    modelModifiers={{}}
                    data-v-bd412aaa=""
                  >
                    <Fragment>
                      {__each(nodes.value, (node) => (
                        <option
                          key={node.nodeId as any}
                          value={node.nodeId as any}
                          data-v-bd412aaa=""
                        >
                          {node.displayName}
                          {" · "}
                          {node.status}
                        </option>
                      ))}
                    </Fragment>
                  </UiSelect>
                </label>
                {nodeId.value ? (
                  <Fragment>
                    {tab.value === "settings" ? (
                      <form
                        onSubmit={(event: any) => {
                          event.preventDefault();
                          save("agents.configure", settings);
                        }}
                        data-v-bd412aaa=""
                      >
                        <p data-v-bd412aaa="">{t("host.settingsHint")}</p>
                        <ContractFields
                          schema={settingsSchema.value as any}
                          values={settings as any}
                          data-v-bd412aaa=""
                        ></ContractFields>
                        <div
                          className={__classes("host-actions")}
                          data-v-bd412aaa=""
                        >
                          <button
                            disabled={
                              busy.value ||
                              (!Object.keys(settings).length as any)
                            }
                            className={__classes("primary-button")}
                            data-v-bd412aaa=""
                          >
                            {t("host.save")}
                          </button>
                          <button
                            type="button"
                            disabled={busy.value as any}
                            onClick={(event: any) => {
                              perform(async () => {
                                diagnostics.value = JSON.stringify(
                                  await operationRequest("agents.diagnostics", {
                                    params: {
                                      nodeId: nodeId.value,
                                    },
                                  }),
                                  null,
                                  2,
                                );
                              });
                            }}
                            data-v-bd412aaa=""
                          >
                            {t("host.diagnostics")}
                          </button>
                        </div>
                        {diagnostics.value ? (
                          <pre data-v-bd412aaa="">{diagnostics.value}</pre>
                        ) : null}
                      </form>
                    ) : (
                      <form
                        onSubmit={(event: any) => {
                          event.preventDefault();
                          save("nodes.save-asset", asset);
                        }}
                        data-v-bd412aaa=""
                      >
                        <p data-v-bd412aaa="">{t("host.assetsHint")}</p>
                        <ContractFields
                          schema={assetSchema.value as any}
                          values={asset as any}
                          data-v-bd412aaa=""
                        ></ContractFields>
                        <button
                          disabled={
                            busy.value || (!Object.keys(asset).length as any)
                          }
                          className={__classes("primary-button")}
                          data-v-bd412aaa=""
                        >
                          {t("host.save")}
                        </button>
                      </form>
                    )}
                  </Fragment>
                ) : (
                  <p data-v-bd412aaa="">{t("host.noNodes")}</p>
                )}
              </section>
            ) : tab.value === "commands" || tab.value === "probes" ? (
              <Fragment>
                <div className={__classes("host-grid")} data-v-bd412aaa="">
                  <section
                    className={__classes("host-card")}
                    data-v-bd412aaa=""
                  >
                    <h2 data-v-bd412aaa="">{t("host.nodes")}</h2>
                    <p data-v-bd412aaa="">{t("host.selectionHint")}</p>
                    <Fragment>
                      {__each(nodes.value, (node) => (
                        <label
                          key={node.nodeId as any}
                          className={__classes("host-check")}
                          data-v-bd412aaa=""
                        >
                          <input
                            type="checkbox"
                            value={node.nodeId as any}
                            {...__input(
                              selected.value,
                              (__nextValue: any) => {
                                selected.value = __nextValue;
                              },
                              "checkbox",
                              node.nodeId,
                              [],
                            )}
                            data-v-bd412aaa=""
                          ></input>
                          {node.displayName}
                          {" · "}
                          {node.status}
                        </label>
                      ))}
                    </Fragment>
                  </section>
                  <form
                    className={__classes("host-card")}
                    onSubmit={(event: any) => {
                      event.preventDefault();
                      tab.value === "commands"
                        ? submitCommand()
                        : submitProbe();
                    }}
                    data-v-bd412aaa=""
                  >
                    <h2 data-v-bd412aaa="">{t(`host.tab.${tab.value}`)}</h2>
                    <p data-v-bd412aaa="">
                      {tab.value === "commands"
                        ? t("host.commandHint")
                        : t("host.probeHint")}
                    </p>
                    <ContractFields
                      schema={
                        schema(
                          tab.value === "commands"
                            ? "commands.batch"
                            : "probes.batch",
                        ) as any
                      }
                      values={
                        tab.value === "commands" ? command : (probe as any)
                      }
                      omit={
                        tab.value === "commands"
                          ? ["nodeIds"]
                          : ([
                              "nodeIds",
                              ...(["http", "https"].includes(String(probe.kind))
                                ? []
                                : ["expectedHttpStatus"]),
                            ] as any)
                      }
                      data-v-bd412aaa=""
                    ></ContractFields>
                    <button
                      disabled={busy.value || (!selected.value.length as any)}
                      className={__classes("primary-button")}
                      data-v-bd412aaa=""
                    >
                      {t("host.submit")}
                      {" · "}
                      {selected.value.length}
                    </button>
                  </form>
                </div>
                {currentBatch.value ? (
                  <section
                    className={__classes("host-card")}
                    data-v-bd412aaa=""
                  >
                    <div
                      className={__classes("host-actions")}
                      data-v-bd412aaa=""
                    >
                      <h2 data-v-bd412aaa="">{t("host.results")}</h2>
                      {currentBatch.value!.results.some((r) => r.jobId) ? (
                        <button
                          disabled={busy.value as any}
                          onClick={(event: any) => {
                            inspectBatch(currentBatch.value!.id);
                          }}
                          data-v-bd412aaa=""
                        >
                          {t("host.refresh")}
                        </button>
                      ) : null}
                      {currentBatch.value!.results.some((r) => r.jobId) ? (
                        <button
                          disabled={busy.value as any}
                          onClick={(event: any) => {
                            inspectBatch(currentBatch.value!.id, true);
                          }}
                          data-v-bd412aaa=""
                        >
                          {t("host.cancel")}
                        </button>
                      ) : null}
                    </div>
                    <Fragment>
                      {__each(currentBatch.value!.results, (result) => (
                        <article
                          key={result.nodeId as any}
                          className={__classes("host-result")}
                          data-v-bd412aaa=""
                        >
                          <strong data-v-bd412aaa="">
                            {nodes.value.find((n) => n.nodeId === result.nodeId)
                              ?.displayName ?? result.nodeId}
                          </strong>
                          <p data-v-bd412aaa="">
                            {result.job?.status ||
                              result.probeId ||
                              result.jobId ||
                              "—"}{" "}
                            {result.stale ? (
                              <span data-v-bd412aaa="">
                                {"· "}
                                {t("host.stale")}
                              </span>
                            ) : null}
                          </p>
                          {result.error ? (
                            <p
                              className={__classes("host-error")}
                              data-v-bd412aaa=""
                            >
                              {result.error}
                            </p>
                          ) : null}
                          {result.job?.exitCode !== undefined ? (
                            <p data-v-bd412aaa="">
                              {t("host.exit")}
                              {": "}
                              {result.job.exitCode}
                            </p>
                          ) : null}
                          {result.job?.output ? (
                            <pre data-v-bd412aaa="">{result.job.output}</pre>
                          ) : null}
                          {result.job?.truncated ? (
                            <p data-v-bd412aaa="">{t("host.truncated")}</p>
                          ) : null}
                        </article>
                      ))}
                    </Fragment>
                  </section>
                ) : null}
                {tab.value === "commands" ? (
                  <section
                    className={__classes("host-card")}
                    data-v-bd412aaa=""
                  >
                    <h2 data-v-bd412aaa="">{t("host.history")}</h2>
                    <Fragment>
                      {__each(batches.value, (batch) => (
                        <button
                          key={batch.id as any}
                          disabled={busy.value as any}
                          onClick={(event: any) => {
                            inspectBatch(batch.id);
                          }}
                          data-v-bd412aaa=""
                        >
                          {batch.id.slice(0, 12)}
                          {" · "}
                          {batch.results.length}
                        </button>
                      ))}
                    </Fragment>
                  </section>
                ) : null}
              </Fragment>
            ) : (
              <Fragment>
                <form
                  className={__classes("host-card")}
                  onSubmit={(event: any) => {
                    event.preventDefault();
                    __call(createGrant, event);
                  }}
                  data-v-bd412aaa=""
                >
                  <h2 data-v-bd412aaa="">{t("host.tab.enrollment")}</h2>
                  <p data-v-bd412aaa="">{t("host.enrollmentHint")}</p>
                  <ContractFields
                    schema={schema("agents.create-grant") as any}
                    values={grant as any}
                    data-v-bd412aaa=""
                  ></ContractFields>
                  <button
                    disabled={busy.value as any}
                    className={__classes("primary-button")}
                    data-v-bd412aaa=""
                  >
                    {t("host.submit")}
                  </button>
                  {grantResult.value ? (
                    <section data-v-bd412aaa="">
                      <p data-v-bd412aaa="">{t("host.secretHint")}</p>
                      <pre data-v-bd412aaa="">
                        {grantResult.value.grant.token}
                      </pre>
                      <pre data-v-bd412aaa="">
                        {grantResult.value.installCommand}
                      </pre>
                      <pre data-v-bd412aaa="">
                        {grantResult.value.dockerInstallCommand}
                      </pre>
                    </section>
                  ) : null}
                </form>
                <section className={__classes("host-card")} data-v-bd412aaa="">
                  <Fragment>
                    {__each(grantList.value, (g) => (
                      <article
                        key={g.id as any}
                        className={__classes("host-result")}
                        data-v-bd412aaa=""
                      >
                        <strong data-v-bd412aaa="">{g.displayPrefix}</strong>
                        <p data-v-bd412aaa="">
                          {g.uses}
                          {" / "}
                          {g.maxUses}
                          {" · "}
                          {beijingTime(g.expiresAt) + " · " + t("time.beijing")}
                        </p>
                        <button
                          disabled={busy.value || (g.revoked as any)}
                          onClick={(event: any) => {
                            revokeGrant(g.id);
                          }}
                          data-v-bd412aaa=""
                        >
                          {t("host.revoke")}
                        </button>
                      </article>
                    ))}
                  </Fragment>
                </section>
              </Fragment>
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
