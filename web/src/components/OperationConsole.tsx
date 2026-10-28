import { ExpandedTextarea } from "./ExpandedTextarea";
import { operationRequest, type User } from "../api";
import { display, locale, t } from "../i18n";
import { intentKey, requestIntent } from "../operation-intent";
import {
  completionInput,
  completionStatus,
  exactJSON,
  operationBody,
  operationCatalog,
  operationUpload,
  operationURL,
  schemaHasSecrets,
  type CoreOperation,
  type OperationInput,
} from "../operations";
import {
  afterMount,
  afterRender,
  beforeDispose,
  cell,
  derived,
  observable,
  watchState,
} from "../state";

import { useController } from "../state";
import "./OperationConsole.css";
import "./PersonalSettings.css";
import { SettingsGroup } from "./SettingsGroup";
import {
  ManagementPage,
  Button,
  Input,
  Checkbox,
  Select,
  Alert,
} from "./ManagementUI";
type Props = { role: User["role"] } & {
  children?: import("react").ReactNode;
  [key: string]: any;
};
export default function OperationConsole(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const operations = cell<CoreOperation[]>([]);
      const filter = cell("");
      const selected = cell("core.health");
      const params = observable<Record<string, string>>({});
      const query = observable<Record<string, string>>({});
      const values = observable<Record<string, string>>({});
      const requestHeaders = observable<Record<string, string>>({});
      const rawBody = cell("");
      const rawMode = cell(false);
      const waitForCompletion = cell(false);
      interface PendingObservation {
        operation: string;
        name: string;
        input: OperationInput;
      }
      const pendingObservations = cell<PendingObservation[]>([]);
      const observationStorage = "nodedance.pending-observations";
      interface PendingSubmission {
        operation: string;
        key: string;
        input: OperationInput;
      }
      const pendingSubmissions = cell<PendingSubmission[]>([]);
      const submissionStorage = "nodedance.pending-submissions";
      function saveSubmissions() {
        try {
          sessionStorage.setItem(
            submissionStorage,
            JSON.stringify(pendingSubmissions.value.slice(-100)),
          );
        } catch {}
      }
      function forgetSubmission(item: PendingSubmission) {
        pendingSubmissions.value = pendingSubmissions.value.filter(
          (v) => v.operation !== item.operation || v.key !== item.key,
        );
        saveSubmissions();
      }
      function rememberSubmission(item: PendingSubmission) {
        if (
          Object.keys({
            ...item.input.params,
            ...item.input.query,
          }).some((k) => /ticket|token|password|secret|credential/i.test(k))
        )
          return;
        forgetSubmission(item);
        pendingSubmissions.value.push(item);
        saveSubmissions();
      }
      async function restoreSubmission(item: PendingSubmission) {
        selected.value = item.operation;
        await afterRender();
        Object.assign(params, item.input.params);
        Object.assign(query, item.input.query);
        requestHeaders["Idempotency-Key"] = item.key;
        lastKey = "";
        error.value = t("operations.restoreHint");
      }
      function saveObservations() {
        try {
          sessionStorage.setItem(
            observationStorage,
            JSON.stringify(pendingObservations.value.slice(-100)),
          );
        } catch {}
      }
      function observationKey(item: PendingObservation) {
        return item.operation + JSON.stringify(item.input);
      }
      function forgetObservation(item: PendingObservation) {
        pendingObservations.value = pendingObservations.value.filter(
          (v) => observationKey(v) !== observationKey(item),
        );
        saveObservations();
      }
      function rememberObservation(item: PendingObservation) {
        if (
          Object.keys({
            ...item.input.params,
            ...item.input.query,
          }).some((k) => /ticket|token|password|secret|credential/i.test(k))
        )
          return;
        forgetObservation(item);
        pendingObservations.value.push(item);
        saveObservations();
      }
      let lastIntent = "",
        lastKey = "";
      let requestAbort: AbortController | undefined;
      const file = cell<File>();
      const output = cell("");
      const error = cell("");
      const busy = cell(false);
      const connected = cell(false);
      const frame = cell("{}");
      const input = cell("");
      let socket: WebSocket | undefined;
      let streamId = "";
      const pendingTerminal = cell<{
        nodeId: string;
        streamId: string;
      }>();
      let terminalIntent = "";
      let revision = 0;
      const operation = derived(() =>
        operations.value.find((item) => item.name === selected.value),
      );
      const matches = derived(() =>
        operations.value.filter((item) =>
          `${item.name} ${item.description} ${display(item.description)}`
            .toLowerCase()
            .includes(filter.value.toLowerCase()),
        ),
      );
      const properties = derived(() =>
        rawMode.value
          ? []
          : Object.entries(operation.value?.bodySchema?.properties ?? {}),
      );
      function fieldLabel(name: string) {
        const key = "operations.field." + name,
          translated = t(key);
        return locale.value === "zh-CN" && translated !== key
          ? `${name} · ${translated}`
          : name;
      }
      function enumLabel(value: unknown) {
        const key = "operations.value." + String(value),
          translated = t(key);
        return translated === key ? String(value) : translated;
      }
      function disconnect() {
        requestAbort?.abort();
        requestAbort = undefined;
        busy.value = false;
        revision++;
        socket?.close();
        socket = undefined;
        connected.value = false;
        streamId = "";
        if (pendingTerminal.value) {
          void operationRequest("terminal.cancel", {
            params: pendingTerminal.value,
          }).catch(() => {});
          pendingTerminal.value = undefined;
        }
      }
      watchState(selected, () => {
        disconnect();
        output.value = "";
        error.value = "";
        file.value = undefined;
        rawBody.value = "";
        rawMode.value = false;
        waitForCompletion.value = false;
        lastIntent = "";
        lastKey = "";
        for (const record of [params, query, values, requestHeaders])
          for (const key of Object.keys(record)) delete record[key];
      });
      watchState(operation, (current) => {
        for (const [key, field] of Object.entries(
          current?.bodySchema?.properties ?? {},
        ))
          if (field.default !== undefined && !(key in values))
            values[key] =
              field.type === "string"
                ? String(field.default)
                : JSON.stringify(field.default);
      });
      function append(value: unknown) {
        const text = typeof value === "string" ? value : exactJSON(value);
        output.value = (output.value + text + "\n").slice(-262144);
      }
      async function execute() {
        const current = operation.value;
        if (!current || busy.value || connected.value) return;
        busy.value = true;
        error.value = "";
        output.value = "";
        const generation = ++revision;
        try {
          const request = {
            params: {
              ...params,
            },
            query: Object.fromEntries(
              Object.entries(query).filter(([, value]) => value !== ""),
            ),
          };
          const headers: Record<string, string> = Object.fromEntries(
            Object.entries(requestHeaders).filter(([, value]) => value !== ""),
          );
          const jsonBody = operationBody(
            current,
            values,
            rawBody.value,
            rawMode.value,
          );
          let requestBody: BodyInit | undefined = jsonBody;
          if (
            current.transport === "upload" ||
            current.transport === "multipart"
          ) {
            if (!file.value) throw new Error(t("operations.fileRequired"));
            requestBody = operationUpload(current, file.value);
          }
          if (current.requiresIdempotencyKey) {
            const signature = await requestIntent(
              current.name,
              request,
              requestBody,
              new Headers(headers),
            );
            if (
              !headers["Idempotency-Key"] ||
              (signature !== lastIntent &&
                headers["Idempotency-Key"] === lastKey)
            )
              headers["Idempotency-Key"] = intentKey(signature);
            requestHeaders["Idempotency-Key"] = headers["Idempotency-Key"]!;
            lastIntent = signature;
            lastKey = headers["Idempotency-Key"]!;
            rememberSubmission({
              operation: current.name,
              key: lastKey,
              input: request,
            });
          }
          if (generation !== revision) return;
          const controller = new AbortController();
          requestAbort = controller;
          if (
            current.transport === "websocket" ||
            current.transport === "terminal"
          ) {
            let url: URL;
            if (current.transport === "terminal") {
              const signature = await requestIntent(
                current.name,
                request,
                requestBody,
                new Headers(headers),
              );
              if (generation !== revision) return;
              if (pendingTerminal.value && terminalIntent !== signature) {
                await operationRequest("terminal.cancel", {
                  params: pendingTerminal.value,
                });
                pendingTerminal.value = undefined;
              }
              const ticket = pendingTerminal.value
                ? await operationRequest<{
                    streamId: string;
                    ticket: string;
                  }>(
                    "terminal.resume",
                    {
                      params: pendingTerminal.value,
                    },
                    {
                      body: "{}",
                      signal: controller.signal,
                    },
                  )
                : await operationRequest<{
                    streamId: string;
                    ticket: string;
                  }>(current.name, request, {
                    body: requestBody,
                    headers,
                    signal: controller.signal,
                  });
              terminalIntent = signature;
              streamId = ticket.streamId;
              pendingTerminal.value = {
                nodeId: request.params.nodeId!,
                streamId: ticket.streamId,
              };
              if (generation !== revision) {
                await operationRequest("terminal.cancel", {
                  params: {
                    nodeId: request.params.nodeId!,
                    streamId: ticket.streamId,
                  },
                });
                return;
              }
              url = await operationURL("terminal.connect", {
                query: {
                  ticket: ticket.ticket,
                },
              });
            } else url = await operationURL(current.name, request);
            if (generation !== revision) return;
            const connection = new WebSocket(url);
            socket = connection;
            connection.onopen = () => {
              if (generation === revision) connected.value = true;
            };
            connection.onmessage = (event) => {
              if (generation !== revision) return;
              try {
                const data = JSON.parse(String(event.data));
                if (
                  current.transport === "terminal" &&
                  data.frame?.action === "output"
                ) {
                  append(
                    new TextDecoder().decode(
                      Uint8Array.from(atob(data.frame.data), (char) =>
                        char.charCodeAt(0),
                      ),
                    ),
                  );
                } else append(data);
              } catch {
                append(String(event.data));
              }
            };
            connection.onclose = () => {
              if (generation === revision) {
                connected.value = false;
                socket = undefined;
              }
            };
            connection.onerror = () => {
              if (generation === revision)
                error.value = t("operations.connectionFailed");
            };
          } else {
            const result = await operationRequest<unknown>(
              current.name,
              request,
              {
                body: requestBody,
                headers,
                signal: controller.signal,
              },
              current.transport === "upload" ? 1800000 : 60000,
            );
            if (generation !== revision) return;
            if (current.requiresIdempotencyKey)
              forgetSubmission({
                operation: current.name,
                key: headers["Idempotency-Key"]!,
                input: request,
              });
            if (result instanceof Blob) {
              const url = URL.createObjectURL(result),
                link = document.createElement("a");
              link.href = url;
              link.download = (query.path || current.name).split("/").pop()!;
              link.click();
              setTimeout(() => URL.revokeObjectURL(url), 1000);
              append(t("operations.downloaded"));
            } else {
              append(result ?? t("operations.completed"));
              if (current.completion) {
                const follow = completionInput(
                  current,
                  request,
                  jsonBody,
                  result,
                );
                const pending = {
                  operation: current.name,
                  ...follow,
                };
                if (
                  !current.completion.terminalStatuses.includes(
                    completionStatus(current, result),
                  )
                )
                  rememberObservation(pending);
                if (waitForCompletion.value)
                  await observe(current, pending, generation, controller);
              }
            }
          }
        } catch (reason) {
          if (generation === revision)
            error.value =
              reason instanceof Error ? reason.message : String(reason);
        } finally {
          if (generation === revision) busy.value = false;
        }
      }
      async function observe(
        current: CoreOperation,
        pending: PendingObservation,
        generation: number,
        controller: AbortController,
      ) {
        const follow = operations.value.find((o) => o.name === pending.name);
        if (!follow || !["GET", "HEAD"].includes(follow.method))
          throw new Error(t("operations.completionRead"));
        while (generation === revision) {
          let observed: unknown;
          try {
            observed = await operationRequest(pending.name, pending.input, {
              signal: controller.signal,
            });
          } catch (reason) {
            if (controller.signal.aborted) throw reason;
            if (
              reason &&
              typeof reason === "object" &&
              "status" in reason &&
              Number(reason.status) < 500
            )
              throw reason;
            await new Promise((resolve) => setTimeout(resolve, 1000));
            continue;
          }
          if (generation !== revision) return;
          append(observed);
          const status = completionStatus(current, observed);
          if (current.completion!.terminalStatuses.includes(status)) {
            forgetObservation(pending);
            if (!current.completion!.successStatuses.includes(status))
              throw new Error(`${t("operations.finishedStatus")}: ${status}`);
            return;
          }
          await new Promise((resolve) => setTimeout(resolve, 1000));
        }
      }
      async function resumeObservation(pending: PendingObservation) {
        if (busy.value) return;
        const current = operations.value.find(
          (o) => o.name === pending.operation,
        );
        if (!current?.completion) {
          error.value = t("operations.completionMissing");
          return;
        }
        const generation = ++revision;
        const controller = new AbortController();
        requestAbort = controller;
        busy.value = true;
        error.value = "";
        output.value = "";
        try {
          await observe(current, pending, generation, controller);
        } catch (reason) {
          if (generation === revision) error.value = reason instanceof Error ? reason.message : String(reason);
        } finally {
          if (generation === revision) busy.value = false;
        }
      }
      function send() {
        try {
          if (!socket || socket.readyState !== WebSocket.OPEN) return;
          const data =
            operation.value?.transport === "terminal"
              ? {
                  streamId,
                  action: "input",
                  data: btoa(
                    String.fromCharCode(
                      ...new TextEncoder().encode(input.value + "\n"),
                    ),
                  ),
                }
              : JSON.parse(frame.value);
          if (streamId && typeof data === "object" && !data.streamId)
            data.streamId = streamId;
          socket.send(JSON.stringify(data));
          input.value = "";
        } catch (reason) {
          error.value = reason instanceof Error ? reason.message : String(reason);
        }
      }
      function newExecution() {
        if (lastIntent)
          requestHeaders["Idempotency-Key"] = intentKey(lastIntent, true);
        else requestHeaders["Idempotency-Key"] = crypto.randomUUID();
        lastKey = requestHeaders["Idempotency-Key"]!;
      }
      let disposed = false;
      let catalogTimer: ReturnType<typeof setInterval> | undefined;
      let displayedCatalog: Awaited<ReturnType<typeof operationCatalog>> | undefined;
      async function refreshCatalog(force = false) {
        const catalog = await operationCatalog(force);
        if (disposed || catalog === displayedCatalog) return;
        displayedCatalog = catalog;
        operations.value = catalog.operations.filter(
          (operation) => __props.role === "administrator" || operation.requiredRole !== "administrator",
        );
      }
      function refreshVisibleCatalog() {
        if (!document.hidden) void refreshCatalog(true).catch(() => {});
      }
      afterMount(async () => {
        document.addEventListener("visibilitychange", refreshVisibleCatalog);
        window.addEventListener("pageshow", refreshVisibleCatalog);
        catalogTimer = setInterval(() => { if (!document.hidden) void refreshCatalog().catch(() => {}); }, 30_000);
        try {
          await refreshCatalog();
          try {
            const records = JSON.parse(
              sessionStorage.getItem(submissionStorage) ?? "[]",
            );
            if (Array.isArray(records))
              pendingSubmissions.value = records
                .slice(-100)
                .filter(
                  (v) =>
                    v &&
                    typeof v.operation === "string" &&
                    typeof v.key === "string" &&
                    v.key.length <= 200 &&
                    v.input &&
                    typeof v.input === "object",
                );
          } catch {}
          try {
            const items = JSON.parse(
              sessionStorage.getItem(observationStorage) ?? "[]",
            );
            if (Array.isArray(items))
              pendingObservations.value = items
                .slice(-100)
                .filter(
                  (v) =>
                    v &&
                    typeof v.operation === "string" &&
                    typeof v.name === "string" &&
                    v.input &&
                    typeof v.input === "object",
                );
          } catch {}
        } catch (reason) {
          error.value = reason instanceof Error ? reason.message : String(reason);
        }
      });
      beforeDispose(() => {
        disposed = true;
        if (catalogTimer) clearInterval(catalogTimer);
        document.removeEventListener("visibilitychange", refreshVisibleCatalog);
        window.removeEventListener("pageshow", refreshVisibleCatalog);
        disconnect();
      });
      const fieldRow = (
        label: string,
        control: import("react").ReactNode,
        key?: string,
        description?: string,
      ) => (
        <label key={key} className="parameter-row operation-field">
          <span>{label}</span>
          <div className="operation-control">
            {control}
            {description ? <small>{description}</small> : null}
          </div>
        </label>
      );
      return () => {
        const op = operation.value;
        const disabled = busy.value || connected.value;
        return (
          <ManagementPage
            registryStyles
            className="operation-console preferences-page workspace-settings"
          >
            <header className="workspace-heading">
              <h1>{t("nav.operations")}</h1>
            </header>
            {pendingSubmissions.value.length ? (
              <section className="workspace-section pending-observations">
                <div className="workspace-section-heading">
                  <h2>{t("operations.uncertain")}</h2>
                </div>
                <SettingsGroup>
                  {pendingSubmissions.value.map((item) => (
                    <div
                      key={item.operation + item.key}
                      className="operation-pending-row"
                    >
                      <span>
                        {item.operation}{" "}
                        {Object.values({
                          ...item.input.params,
                          ...item.input.query,
                        }).join(" / ")}
                      </span>
                      <div className="settings-section-actions">
                        <Button
                          type="button"
                          variant="secondary"
                          disabled={busy.value}
                          onClick={() => void restoreSubmission(item)}
                        >
                          {t("operations.restoreKey")}
                        </Button>
                        <Button
                          type="button"
                          variant="secondary"
                          disabled={busy.value}
                          onClick={() => forgetSubmission(item)}
                        >
                          {t("operations.forget")}
                        </Button>
                      </div>
                    </div>
                  ))}
                </SettingsGroup>
              </section>
            ) : null}
            {pendingObservations.value.length ? (
              <section className="workspace-section pending-observations">
                <div className="workspace-section-heading">
                  <h2>{t("operations.pending")}</h2>
                </div>
                <SettingsGroup>
                  {pendingObservations.value.map((item) => (
                    <div
                      key={observationKey(item)}
                      className="operation-pending-row"
                    >
                      <span>
                        {item.operation}{" "}
                        {Object.values({
                          ...item.input.params,
                          ...item.input.query,
                        }).join(" / ")}
                      </span>
                      <div className="settings-section-actions">
                        <Button
                          type="button"
                          variant="secondary"
                          disabled={busy.value}
                          onClick={() => void resumeObservation(item)}
                        >
                          {t("operations.resume")}
                        </Button>
                        <Button
                          type="button"
                          variant="secondary"
                          disabled={busy.value}
                          onClick={() => forgetObservation(item)}
                        >
                          {t("operations.forget")}
                        </Button>
                      </div>
                    </div>
                  ))}
                </SettingsGroup>
              </section>
            ) : null}
            <div className="operation-layout settings-sections">
              <aside className="workspace-section operation-picker">
                <div className="workspace-section-heading">
                  <h2>{t("management.operationPicker")}</h2>
                </div>
                <SettingsGroup>
                  {fieldRow(
                    t("operations.search"),
                    <Input
                      type="search"
                      aria-label={t("operations.search")}
                      value={filter.value}
                      onChange={(event) => {
                        filter.value = event.target.value;
                      }}
                    />,
                  )}
                  {fieldRow(
                    t("nav.operations"),
                    <Select
                      aria-label={t("nav.operations")}
                      value={selected.value}
                      onValueChange={(value) => {
                        selected.value = value;
                      }}
                      options={matches.value.map((item) => ({
                        value: item.name,
                        label: `${display(item.description)} · ${item.name}`,
                      }))}
                    />,
                  )}
                </SettingsGroup>
              </aside>
              {op ? (
                <section className="operation-form workspace-section settings-integrated">
                  <div className="workspace-section-heading">
                    <h2>{display(op.description)}</h2>
                  </div>
                  <code>{op.name}</code>
                  <form
                    noValidate
                    onSubmit={(event) => {
                      event.preventDefault();
                      void execute();
                    }}
                  >
                    {op.parameters.length || op.query.length ? (
                      <section className="workspace-section operation-field-group">
                        <div className="workspace-section-heading">
                          <h3>{t("operations.targetFields")}</h3>
                        </div>
                        <SettingsGroup>
                          {op.parameters.map((name) =>
                            fieldRow(
                              fieldLabel(name),
                              <Input
                                required
                                disabled={disabled}
                                value={params[name] ?? ""}
                                onChange={(event) => {
                                  params[name] = event.target.value;
                                }}
                              />,
                              name,
                            ),
                          )}
                          {op.query.map((name) =>
                            fieldRow(
                              fieldLabel(name),
                              <Input
                                type={
                                  /ticket|token/i.test(name)
                                    ? "password"
                                    : "text"
                                }
                                disabled={disabled}
                                value={query[name] ?? ""}
                                onChange={(event) => {
                                  query[name] = event.target.value;
                                }}
                              />,
                              name,
                            ),
                          )}
                        </SettingsGroup>
                      </section>
                    ) : null}
                    {op.bodySchema ? (
                      <section className="workspace-section operation-field-group">
                        <div className="workspace-section-heading">
                          <h3>{t("operations.requestFields")}</h3>
                        </div>
                        <SettingsGroup>
                          {op.bodySchema.type === "object" ? (
                            <Checkbox
                              label={t("operations.rawJSON")}
                              disabled={busy.value}
                              checked={rawMode.value}
                              onCheckedChange={(value) => {
                                rawMode.value = value === true;
                              }}
                            />
                          ) : null}
                          {properties.value.map(([name, schema]) => {
                            const required =
                              op.bodySchema?.required?.includes(name);
                            const label = `${fieldLabel(name)}${required ? " *" : ""}`;
                            const value = values[name] ?? "";
                            const change = (value: string) => {
                              values[name] = value;
                            };
                            return fieldRow(
                              label,
                              schemaHasSecrets(schema) ? (
                                <Input
                                  type="password"
                                  disabled={disabled}
                                  autoComplete="off"
                                  value={value}
                                  onChange={(event) =>
                                    change(event.target.value)
                                  }
                                />
                              ) : schema.enum || schema.type === "boolean" ? (
                                <Select
                                  required={required}
                                  disabled={disabled}
                                  value={value}
                                  onValueChange={change}
                                  options={[
                                    { value: "", label: "—" },
                                    ...(schema.enum ?? ["true", "false"]).map(
                                      (value) => ({
                                        value: String(value),
                                        label: enumLabel(value),
                                      }),
                                    ),
                                  ]}
                                />
                              ) : schema.type === "object" ||
                                schema.type === "array" ? (
                                <ExpandedTextarea
                                  rows={4}
                                  required={required}
                                  disabled={disabled}
                                  value={value}
                                  onChange={(event) =>
                                    change(event.target.value)
                                  }
                                />
                              ) : (
                                <Input
                                  type="text"
                                  inputMode={
                                    schema.type === "integer"
                                      ? "numeric"
                                      : schema.type === "number"
                                        ? "decimal"
                                        : undefined
                                  }
                                  disabled={disabled}
                                  autoComplete="off"
                                  value={value}
                                  onChange={(event) =>
                                    change(event.target.value)
                                  }
                                />
                              ),
                              name,
                              schema.description
                                ? display(schema.description)
                                : undefined,
                            );
                          })}
                          {rawMode.value || op.bodySchema.type !== "object"
                            ? fieldRow(
                                "JSON",
                                schemaHasSecrets(op.bodySchema) ? (
                                  <Input
                                    type="password"
                                    disabled={busy.value}
                                    autoComplete="off"
                                    value={rawBody.value}
                                    onChange={(event) => {
                                      rawBody.value = event.target.value;
                                    }}
                                  />
                                ) : (
                                  <ExpandedTextarea
                                    rows={5}
                                    disabled={busy.value}
                                    value={rawBody.value}
                                    onChange={(event) => {
                                      rawBody.value = event.target.value;
                                    }}
                                  />
                                ),
                              )
                            : null}
                        </SettingsGroup>
                      </section>
                    ) : null}
                    {Object.keys(op.headers ?? {}).length ? (
                      <section className="workspace-section operation-field-group">
                        <div className="workspace-section-heading">
                          <h3>{t("operations.headerFields")}</h3>
                        </div>
                        <SettingsGroup>
                          {Object.entries(op.headers ?? {}).map(
                            ([name, schema]) =>
                              fieldRow(
                                fieldLabel(name),
                                <Input
                                  type={
                                    schemaHasSecrets(schema)
                                      ? "password"
                                      : "text"
                                  }
                                  disabled={disabled}
                                  autoComplete="off"
                                  value={requestHeaders[name] ?? ""}
                                  onChange={(event) => {
                                    requestHeaders[name] = event.target.value;
                                  }}
                                />,
                                name,
                                schema.description
                                  ? display(schema.description)
                                  : undefined,
                              ),
                          )}
                        </SettingsGroup>
                      </section>
                    ) : null}
                    {op.completion ||
                    ["upload", "multipart"].includes(op.transport) ? (
                      <SettingsGroup>
                        {op.completion ? (
                          <Checkbox
                            label={t("operations.wait")}
                            disabled={busy.value}
                            checked={waitForCompletion.value}
                            onCheckedChange={(value) => {
                              waitForCompletion.value = value === true;
                            }}
                          />
                        ) : null}
                        {["upload", "multipart"].includes(op.transport)
                          ? fieldRow(
                              t("operations.file"),
                              <Input
                                type="file"
                                required
                                disabled={disabled}
                                onChange={(event) => {
                                  file.value = event.target.files?.[0];
                                }}
                              />,
                            )
                          : null}
                      </SettingsGroup>
                    ) : null}
                    {op.requiresIdempotencyKey ? (
                      <small>{t("operations.retryIntent")}</small>
                    ) : null}
                    <div className="settings-section-actions operation-actions">
                      {op.requiresIdempotencyKey ? (
                        <Button
                          type="button"
                          variant="secondary"
                          disabled={disabled}
                          onClick={newExecution}
                        >
                          {t("operations.newExecution")}
                        </Button>
                      ) : null}
                      {connected.value || pendingTerminal.value ? (
                        <Button
                          type="button"
                          variant="secondary"
                          onClick={disconnect}
                        >
                          {op.transport === "terminal"
                            ? t("operations.endTerminal")
                            : t("operations.disconnect")}
                        </Button>
                      ) : null}
                      {busy.value ? (
                        <Button
                          type="button"
                          variant="secondary"
                          onClick={disconnect}
                        >
                          {t("operations.stopWaiting")}
                        </Button>
                      ) : null}
                      <Button type="submit" disabled={disabled}>
                        {busy.value
                          ? t("operations.running")
                          : t("operations.execute")}
                      </Button>
                    </div>
                  </form>
                  {connected.value && op.duplex ? (
                    <form
                      className="operation-stream-form"
                      onSubmit={(event) => {
                        event.preventDefault();
                        send();
                      }}
                    >
                      {fieldRow(
                        op.transport === "websocket"
                          ? "JSON"
                          : t("operations.input"),
                        op.transport === "websocket" ? (
                          <ExpandedTextarea
                            rows={3}
                            aria-label="JSON"
                            value={frame.value}
                            onChange={(event) => {
                              frame.value = event.target.value;
                            }}
                          />
                        ) : (
                          <Input
                            aria-label={t("operations.input")}
                            value={input.value}
                            onChange={(event) => {
                              input.value = event.target.value;
                            }}
                          />
                        ),
                      )}
                      <div className="settings-section-actions">
                        <Button type="submit" variant="secondary">
                          {t("operations.send")}
                        </Button>
                      </div>
                    </form>
                  ) : null}
                </section>
              ) : null}
              {error.value ? (
                <Alert role="alert" tone="danger" title={t("management.error")}>
                  {display(error.value)}
                </Alert>
              ) : null}
              {output.value ? (
                <section className="workspace-section operation-output">
                  <div className="workspace-section-heading">
                    <h2>{t("domains.result")}</h2>
                  </div>
                  <SettingsGroup>
                    <pre data-testid="operation-result">{output.value}</pre>
                  </SettingsGroup>
                </section>
              ) : null}
            </div>
          </ManagementPage>
        );
      };
    },
    input,
    ["role"],
    [],
    {},
    true,
  );
}
