import { api, operationRequest } from "../api";
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

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import "./OperationConsole.css";
import {
  ManagementPage,
  Button,
  Input,
  Textarea,
  Checkbox,
  Select,
  Card,
  Badge,
  Alert,
  Tabs,
  TabsList,
  TabsTrigger,
  checkboxProps,
  selectOptions,
} from "./ManagementUI";
type Props = {} & { children?: import("react").ReactNode; [key: string]: any };
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
          if (field.default !== undefined)
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
          if (generation === revision) error.value = String(reason);
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
          error.value = String(reason);
        }
      }
      function newExecution() {
        if (lastIntent)
          requestHeaders["Idempotency-Key"] = intentKey(lastIntent, true);
        else requestHeaders["Idempotency-Key"] = crypto.randomUUID();
        lastKey = requestHeaders["Idempotency-Key"]!;
      }
      afterMount(async () => {
        try {
          const [catalog, identity] = await Promise.all([
            operationCatalog(),
            api.me(),
          ]);
          operations.value = catalog.operations.filter(
            (operation) =>
              identity.user.role === "administrator" ||
              operation.requiredRole !== "administrator",
          );
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
          error.value = String(reason);
        }
      });
      beforeDispose(disconnect);
      return () => (
        <>
          <ManagementPage
            className={__classes("operation-console workspace-page")}
            data-v-62585b82=""
          >
            <header>
              <h1>{t("nav.operations")}</h1>
            </header>
            <p className={__classes("muted")} data-v-62585b82="">
              {t("operations.description")}
            </p>
            {pendingSubmissions.value.length ? (
              <div
                className={__classes("pending-observations")}
                data-v-62585b82=""
              >
                <p className={__classes("muted")} data-v-62585b82="">
                  {t("operations.uncertain")}
                </p>
                <Fragment>
                  {__each(pendingSubmissions.value, (item) => (
                    <div
                      key={(item.operation + item.key) as any}
                      data-v-62585b82=""
                    >
                      <span data-v-62585b82="">
                        {item.operation}
                        {" · "}
                        {Object.values({
                          ...item.input.params,
                          ...item.input.query,
                        }).join(" / ")}
                      </span>
                      <Button
                        type="button"
                        disabled={busy.value as any}
                        onClick={(event: any) => {
                          restoreSubmission(item);
                        }}
                        data-v-62585b82=""
                        variant="secondary"
                      >
                        {t("operations.restoreKey")}
                      </Button>
                      <Button
                        type="button"
                        disabled={busy.value as any}
                        onClick={(event: any) => {
                          forgetSubmission(item);
                        }}
                        data-v-62585b82=""
                        variant="secondary"
                      >
                        {t("operations.forget")}
                      </Button>
                    </div>
                  ))}
                </Fragment>
              </div>
            ) : null}
            {pendingObservations.value.length ? (
              <div
                className={__classes("pending-observations")}
                data-v-62585b82=""
              >
                <p className={__classes("muted")} data-v-62585b82="">
                  {t("operations.pending")}
                </p>
                <Fragment>
                  {__each(pendingObservations.value, (item) => (
                    <div key={observationKey(item) as any} data-v-62585b82="">
                      <span data-v-62585b82="">
                        {item.operation}
                        {" · "}
                        {Object.values({
                          ...item.input.params,
                          ...item.input.query,
                        }).join(" / ")}
                      </span>{" "}
                      <Button
                        type="button"
                        disabled={busy.value as any}
                        onClick={(event: any) => {
                          resumeObservation(item);
                        }}
                        data-v-62585b82=""
                        variant="secondary"
                      >
                        {t("operations.resume")}
                      </Button>{" "}
                      <Button
                        type="button"
                        disabled={busy.value as any}
                        onClick={(event: any) => {
                          forgetObservation(item);
                        }}
                        data-v-62585b82=""
                        variant="secondary"
                      >
                        {t("operations.forget")}
                      </Button>
                    </div>
                  ))}
                </Fragment>
              </div>
            ) : null}
            <div className={__classes("operation-layout")} data-v-62585b82="">
              <aside data-v-62585b82="">
                <Card
                  title={t("management.operationPicker")}
                  className="management-card operation-picker"
                >
                  <Input
                    type="search"
                    placeholder={t("operations.search") as any}
                    aria-label={t("operations.search") as any}
                    {...__input(
                      filter.value,
                      (__nextValue: any) => {
                        filter.value = __nextValue;
                      },
                      "search",
                      undefined,
                      [],
                    )}
                    data-v-62585b82=""
                  ></Input>
                  <Select
                    aria-label={t("nav.operations") as any}
                    value={String(selected.value ?? "")}
                    onValueChange={(next) => {
                      const __nextValue: any =
                        typeof selected.value === "number"
                          ? Number(next)
                          : next;
                      selected.value = __nextValue;
                    }}
                    data-v-62585b82=""
                    options={selectOptions(
                      <>
                        <Fragment>
                          {__each(matches.value, (item) => (
                            <option
                              key={item.name as any}
                              value={item.name as any}
                              data-v-62585b82=""
                            >
                              {display(item.description)}
                              {" · "}
                              {item.name}
                            </option>
                          ))}
                        </Fragment>
                      </>,
                    )}
                  ></Select>
                </Card>
              </aside>
              {operation.value ? (
                <Card
                  title={display(operation.value!.description)}
                  className="management-card"
                >
                  <div
                    className={__classes("operation-form")}
                    data-v-62585b82=""
                  >
                    <p className={__classes("muted")} data-v-62585b82="">
                      <code data-v-62585b82="">{operation.value!.name}</code>
                    </p>
                    <form
                      onSubmit={(event: any) => {
                        event.preventDefault();
                        __call(execute, event);
                      }}
                      data-v-62585b82=""
                    >
                      {operation.value!.parameters.length ||
                      operation.value!.query.length ? (
                        <fieldset
                          className={__classes("operation-field-group")}
                          data-v-62585b82=""
                        >
                          <legend data-v-62585b82="">
                            {t("operations.targetFields")}
                          </legend>
                          <Fragment>
                            {__each(operation.value!.parameters, (name) => (
                              <label key={name as any} data-v-62585b82="">
                                {fieldLabel(name)}{" "}
                                <Input
                                  required={true}
                                  disabled={
                                    busy.value || (connected.value as any)
                                  }
                                  {...__input(
                                    params[name],
                                    (__nextValue: any) => {
                                      params[name] = __nextValue;
                                    },
                                    "",
                                    undefined,
                                    [],
                                  )}
                                  data-v-62585b82=""
                                ></Input>
                              </label>
                            ))}
                          </Fragment>
                          <Fragment>
                            {__each(operation.value!.query, (name) => (
                              <label key={name as any} data-v-62585b82="">
                                {fieldLabel(name)}{" "}
                                <Input
                                  type={
                                    /ticket|token/i.test(name)
                                      ? "password"
                                      : ("text" as any)
                                  }
                                  disabled={
                                    busy.value || (connected.value as any)
                                  }
                                  {...__input(
                                    query[name],
                                    (__nextValue: any) => {
                                      query[name] = __nextValue;
                                    },
                                    "",
                                    undefined,
                                    [],
                                  )}
                                  data-v-62585b82=""
                                ></Input>
                              </label>
                            ))}
                          </Fragment>
                        </fieldset>
                      ) : null}
                      {operation.value!.bodySchema ? (
                        <fieldset
                          className={__classes("operation-field-group")}
                          data-v-62585b82=""
                        >
                          <legend data-v-62585b82="">
                            {t("operations.requestFields")}
                          </legend>
                          {operation.value!.bodySchema?.type === "object" ? (
                            <label
                              className={__classes("check-row")}
                              data-v-62585b82=""
                            >
                              <Checkbox
                                disabled={busy.value as any}
                                {...checkboxProps(
                                  rawMode.value,
                                  (__nextValue: any) => {
                                    rawMode.value = __nextValue;
                                  },
                                  undefined,
                                )}
                                data-v-62585b82=""
                              ></Checkbox>
                              {t("operations.rawJSON")}
                            </label>
                          ) : null}
                          <Fragment>
                            {__each(properties.value, ([name, schema]) => (
                              <label
                                key={name as any}
                                data-field-type={
                                  schema.enum ? "enum" : (schema.type as any)
                                }
                                data-v-62585b82=""
                              >
                                {fieldLabel(name)}
                                {operation.value!.bodySchema?.required?.includes(
                                  name,
                                )
                                  ? " *"
                                  : ""}{" "}
                                {schemaHasSecrets(schema) ? (
                                  <Input
                                    type="password"
                                    disabled={
                                      busy.value || (connected.value as any)
                                    }
                                    autoComplete="off"
                                    {...__input(
                                      values[name],
                                      (__nextValue: any) => {
                                        values[name] = __nextValue;
                                      },
                                      "password",
                                      undefined,
                                      [],
                                    )}
                                    data-v-62585b82=""
                                  ></Input>
                                ) : schema.enum || schema.type === "boolean" ? (
                                  <Select
                                    required={
                                      operation.value!.bodySchema?.required?.includes(
                                        name,
                                      ) as any
                                    }
                                    disabled={
                                      busy.value || (connected.value as any)
                                    }
                                    value={String(values[name] ?? "")}
                                    onValueChange={(next) => {
                                      const __nextValue: any =
                                        typeof values[name] === "number"
                                          ? Number(next)
                                          : next;
                                      values[name] = __nextValue;
                                    }}
                                    data-v-62585b82=""
                                    options={selectOptions(
                                      <>
                                        <option value="" data-v-62585b82="">
                                          {"—"}
                                        </option>
                                        <Fragment>
                                          {__each(
                                            schema.enum ?? ["true", "false"],
                                            (value) => (
                                              <option
                                                key={String(value) as any}
                                                value={String(value) as any}
                                                data-v-62585b82=""
                                              >
                                                {enumLabel(value)}
                                              </option>
                                            ),
                                          )}
                                        </Fragment>
                                      </>,
                                    )}
                                  ></Select>
                                ) : schema.type === "object" ||
                                  schema.type === "array" ? (
                                  <Textarea
                                    rows={4}
                                    required={
                                      operation.value!.bodySchema?.required?.includes(
                                        name,
                                      ) as any
                                    }
                                    disabled={
                                      busy.value || (connected.value as any)
                                    }
                                    {...__input(
                                      values[name],
                                      (__nextValue: any) => {
                                        values[name] = __nextValue;
                                      },
                                      "",
                                      undefined,
                                      [],
                                    )}
                                    data-v-62585b82=""
                                  ></Textarea>
                                ) : (
                                  <Input
                                    type="text"
                                    inputMode={
                                      schema.type === "integer"
                                        ? "numeric"
                                        : schema.type === "number"
                                          ? "decimal"
                                          : (undefined as any)
                                    }
                                    minLength={schema.minLength as any}
                                    maxLength={schema.maxLength as any}
                                    disabled={
                                      busy.value || (connected.value as any)
                                    }
                                    autoComplete="off"
                                    {...__input(
                                      values[name],
                                      (__nextValue: any) => {
                                        values[name] = __nextValue;
                                      },
                                      "text",
                                      undefined,
                                      [],
                                    )}
                                    data-v-62585b82=""
                                  ></Input>
                                )}
                                {schema.description ? (
                                  <small data-v-62585b82="">
                                    {display(schema.description)}
                                  </small>
                                ) : null}
                              </label>
                            ))}
                          </Fragment>
                          {operation.value!.bodySchema &&
                          (rawMode.value ||
                            operation.value!.bodySchema.type !== "object") ? (
                            <label data-v-62585b82="">
                              {"JSON "}
                              {schemaHasSecrets(operation.value!.bodySchema) ? (
                                <Input
                                  type="password"
                                  disabled={busy.value as any}
                                  autoComplete="off"
                                  {...__input(
                                    rawBody.value,
                                    (__nextValue: any) => {
                                      rawBody.value = __nextValue;
                                    },
                                    "password",
                                    undefined,
                                    [],
                                  )}
                                  data-v-62585b82=""
                                ></Input>
                              ) : (
                                <Textarea
                                  rows={5}
                                  disabled={busy.value as any}
                                  {...__input(
                                    rawBody.value,
                                    (__nextValue: any) => {
                                      rawBody.value = __nextValue;
                                    },
                                    "",
                                    undefined,
                                    [],
                                  )}
                                  data-v-62585b82=""
                                ></Textarea>
                              )}
                            </label>
                          ) : null}
                        </fieldset>
                      ) : null}
                      {Object.keys(operation.value!.headers ?? {}).length ? (
                        <fieldset
                          className={__classes("operation-field-group")}
                          data-v-62585b82=""
                        >
                          <legend data-v-62585b82="">
                            {t("operations.headerFields")}
                          </legend>
                          <Fragment>
                            {__each(
                              Object.entries(operation.value!.headers ?? {}),
                              ([name, schema]) => (
                                <label key={name as any} data-v-62585b82="">
                                  {fieldLabel(name)}
                                  <Input
                                    type={
                                      schemaHasSecrets(schema)
                                        ? "password"
                                        : ("text" as any)
                                    }
                                    disabled={
                                      busy.value || (connected.value as any)
                                    }
                                    autoComplete="off"
                                    {...__input(
                                      requestHeaders[name],
                                      (__nextValue: any) => {
                                        requestHeaders[name] = __nextValue;
                                      },
                                      "",
                                      undefined,
                                      [],
                                    )}
                                    data-v-62585b82=""
                                  ></Input>
                                  <small data-v-62585b82="">
                                    {display(schema.description)}
                                  </small>
                                </label>
                              ),
                            )}
                          </Fragment>
                        </fieldset>
                      ) : null}
                      {operation.value!.requiresIdempotencyKey ? (
                        <Fragment>
                          <small data-v-62585b82="">
                            {t("operations.retryIntent")}
                          </small>
                          <Button
                            type="button"
                            disabled={busy.value || (connected.value as any)}
                            onClick={(event: any) => {
                              __call(newExecution, event);
                            }}
                            data-v-62585b82=""
                            variant="secondary"
                          >
                            {t("operations.newExecution")}
                          </Button>
                        </Fragment>
                      ) : null}
                      {operation.value!.completion ? (
                        <label
                          className={__classes("check-row")}
                          data-v-62585b82=""
                        >
                          <Checkbox
                            disabled={busy.value as any}
                            {...checkboxProps(
                              waitForCompletion.value,
                              (__nextValue: any) => {
                                waitForCompletion.value = __nextValue;
                              },
                              undefined,
                            )}
                            data-v-62585b82=""
                          ></Checkbox>
                          {t("operations.wait")}
                        </label>
                      ) : null}
                      {["upload", "multipart"].includes(
                        operation.value!.transport,
                      ) ? (
                        <label data-v-62585b82="">
                          {t("operations.file")}
                          <Input
                            type="file"
                            required={true}
                            disabled={busy.value || (connected.value as any)}
                            onChange={(event: any) => {
                              file.value = (
                                event.target as HTMLInputElement
                              ).files?.[0];
                            }}
                            data-v-62585b82=""
                          ></Input>
                        </label>
                      ) : null}
                      <Button
                        type="submit"
                        disabled={busy.value || (connected.value as any)}
                        className={__classes("primary-button")}
                        data-v-62585b82=""
                        variant="primary"
                      >
                        {busy.value
                          ? t("operations.running")
                          : t("operations.execute")}
                      </Button>
                      {connected.value || pendingTerminal.value ? (
                        <Button
                          type="button"
                          onClick={(event: any) => {
                            __call(disconnect, event);
                          }}
                          data-v-62585b82=""
                          variant="secondary"
                        >
                          {operation.value!.transport === "terminal"
                            ? t("operations.endTerminal")
                            : t("operations.disconnect")}
                        </Button>
                      ) : null}
                      {busy.value ? (
                        <Button
                          type="button"
                          onClick={(event: any) => {
                            __call(disconnect, event);
                          }}
                          data-v-62585b82=""
                          variant="secondary"
                        >
                          {t("operations.stopWaiting")}
                        </Button>
                      ) : null}
                    </form>
                    {connected.value && operation.value!.duplex ? (
                      <form
                        onSubmit={(event: any) => {
                          event.preventDefault();
                          __call(send, event);
                        }}
                        data-v-62585b82=""
                      >
                        {operation.value!.transport === "websocket" ? (
                          <Textarea
                            rows={3}
                            aria-label="JSON"
                            {...__input(
                              frame.value,
                              (__nextValue: any) => {
                                frame.value = __nextValue;
                              },
                              "",
                              undefined,
                              [],
                            )}
                            data-v-62585b82=""
                          ></Textarea>
                        ) : (
                          <Input
                            aria-label={t("operations.input") as any}
                            {...__input(
                              input.value,
                              (__nextValue: any) => {
                                input.value = __nextValue;
                              },
                              "",
                              undefined,
                              [],
                            )}
                            data-v-62585b82=""
                          ></Input>
                        )}
                        <Button
                          type="submit"
                          data-v-62585b82=""
                          variant="secondary"
                        >
                          {t("operations.send")}
                        </Button>
                      </form>
                    ) : null}
                    {error.value ? (
                      <Alert
                        role="alert"
                        className={__classes("error")}
                        data-v-62585b82=""
                        tone="danger"
                        title={t("management.error")}
                      >
                        {display(error.value)}
                      </Alert>
                    ) : null}
                    {output.value ? (
                      <pre data-testid="operation-result" data-v-62585b82="">
                        {output.value}
                      </pre>
                    ) : null}
                  </div>
                </Card>
              ) : null}
            </div>
          </ManagementPage>
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
