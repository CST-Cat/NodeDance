import { ExpandedTextarea } from "./ExpandedTextarea";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  Globe2,
  ShieldCheck,
  RefreshCw,
  Plus,
  Upload,
  KeyRound,
  Play,
} from "lucide-react";

import { Button } from "@/components/arc/button/button";
import { Alert } from "@/components/arc/alert/alert";
import { SettingsGroup, SettingsRows } from "./SettingsGroup";
import { Checkbox } from "@/components/arc/checkbox/checkbox";
import { Dialog, DialogContent } from "@/components/arc/dialog/dialog";
import { EmptyState } from "@/components/arc/empty-state/empty-state";
import { FileDropzone } from "@/components/arc/file-dropzone/file-dropzone";
import { Input } from "@/components/arc/input/input";
import { JsonViewer } from "@/components/arc/json-viewer/json-viewer";
import { Select } from "@/components/arc/select/select";
import {
  SortableDataTable,
  type DataColumn,
} from "@/components/arc/sortable-data-table/sortable-data-table";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/arc/tabs/tabs";

import { operationRequest } from "../api";
import {
  operationBody,
  operationCatalog,
  schemaHasSecrets,
  type CoreOperation,
} from "../operations";
import { t } from "../i18n";
import "./PersonalSettings.css";
import "./DomainCertificates.css";

type Row = Record<string, unknown> & {
  id: string;
  revision?: number;
  name?: string;
  status?: string;
};
type Choice = { value: string; label: string };
type Inventory = {
  domains: Row[];
  certificates: Row[];
  accounts: Row[];
  providers: Row[];
  jobs: Row[];
  channels: Row[];
};
const initial: Inventory = {
  domains: [],
  certificates: [],
  accounts: [],
  providers: [],
  jobs: [],
  channels: [],
};
type Action = {
  operation: CoreOperation;
  params: Record<string, string>;
  seed: Record<string, string>;
};
const message = (error: unknown) =>
  error instanceof Error ? error.message : String(error);
const opLabel = (op: CoreOperation) =>
  t(`api.operation.${op.name}`) === `api.operation.${op.name}`
    ? op.description
    : t(`api.operation.${op.name}`);
const fieldLabel = (key: string) =>
  t(`domains.field.${key}`) === `domains.field.${key}`
    ? key
    : t(`domains.field.${key}`);

export default function DomainCertificates() {
  const [tab, setTab] = useState("domains"),
    [inventory, setInventory] = useState<Inventory>(initial);
  const [operations, setOperations] = useState<CoreOperation[]>([]),
    [search, setSearch] = useState("");
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(true),
    [action, setAction] = useState<Action | null>(null);
  const [loaded, setLoaded] = useState<
    Partial<Record<keyof Inventory, boolean>>
  >({});
  const [container, setContainer] = useState<HTMLElement | null>(null),
    [result, setResult] = useState<unknown>();
  const controller = useRef<AbortController | null>(null);
  const refreshing = useRef(false);
  const refresh = useCallback(async () => {
    controller.current?.abort();
    const pending = new AbortController();
    controller.current = pending;
    refreshing.current = true;
    setBusy(true);
    setError("");
    try {
      const catalog = await operationCatalog();
      if (pending.signal.aborted) return;
      setOperations(
        catalog.operations.filter(
          (op) =>
            op.name.startsWith("domains.") ||
            op.name.startsWith("certificates."),
        ),
      );
      const load = async <K extends keyof Inventory>(
        key: K,
        operation: string,
      ) => {
        try {
          const response = await operationRequest<Record<K, Row[]>>(
            operation,
            {},
            { signal: pending.signal },
          );
          if (!pending.signal.aborted) {
            setInventory((current) => ({ ...current, [key]: response[key] }));
            setLoaded((current) => ({ ...current, [key]: true }));
          }
        } catch (reason) {
          if (!pending.signal.aborted) setError(message(reason));
        }
      };
      await Promise.all([
        load("domains", "domains.list"),
        load("certificates", "certificates.list"),
        load("accounts", "certificates.accounts"),
        load("providers", "certificates.providers"),
        load("jobs", "certificates.jobs"),
        load("channels", "channels.list"),
      ]);
    } catch (e) {
      if (!pending.signal.aborted) setError(message(e));
    } finally {
      if (!pending.signal.aborted) {
        refreshing.current = false;
        setBusy(false);
      }
    }
  }, []);
  useEffect(() => {
    void refresh();
    return () => controller.current?.abort();
  }, [refresh]);
  useEffect(() => {
    if (
      !inventory.jobs.some(
        (job) => job.status === "queued" || job.status === "running",
      )
    )
      return;
    const timer = setInterval(() => {
      if (
        refreshing.current ||
        !controller.current ||
        controller.current.signal.aborted
      )
        return;
      void refresh();
    }, 4000);
    return () => clearInterval(timer);
  }, [inventory.jobs, refresh]);
  function open(name: string, row?: Row, seed?: Record<string, string>) {
    const operation = operations.find((op) => op.name === name);
    if (!operation) {
      setError(t("domains.operationMissing"));
      return;
    }
    const values: Record<string, string> = {};
    for (const key of Object.keys(operation.bodySchema?.properties ?? {})) {
      if (key === "confirm" || key === "apiToken" || key === "privateKeyPem")
        continue;
      if (row && row[key] !== undefined)
        values[key] =
          typeof row[key] === "string"
            ? String(row[key])
            : JSON.stringify(row[key]);
    }
    setAction({
      operation,
      params: row ? { id: row.id } : {},
      seed: { ...values, ...seed },
    });
    setResult(undefined);
  }
  function actions(row: Row, names: string[]) {
    return (
      <div className="domain-row-actions">
        {names.map((name) => {
          const op = operations.find((item) => item.name === name);
          return op ? (
            <Button
              key={name}
              size="sm"
              variant="ghost"
              onClick={() => open(name, row)}
            >
              {opLabel(op)}
            </Button>
          ) : null;
        })}
      </div>
    );
  }
  const visible = (rows: Row[]) =>
    rows.filter((row) =>
      JSON.stringify(row).toLowerCase().includes(search.toLowerCase()),
    );
  const cols = (kind: string): DataColumn<Row>[] => {
    if (kind === "domains")
      return [
        {
          key: "name",
          label: t("domains.field.name"),
          sortable: true,
          render: (_, row) => (
            <div className="domain-record-name">
              <strong>{row.name}</strong>
              <small>{String(row.label || row.id)}</small>
            </div>
          ),
        },
        { key: "notes", label: t("domains.field.notes") },
        {
          key: "actions",
          label: t("domains.actions"),
          render: (_, row) =>
            actions(row, ["domains.dns", "domains.update", "domains.delete"]),
        },
      ];
    if (kind === "certificates")
      return [
        {
          key: "name",
          label: t("domains.field.name"),
          sortable: true,
          render: (_, row) => (
            <div className="domain-record-name">
              <strong>{row.name}</strong>
              <small>
                {Array.isArray(row.dnsNames) ? row.dnsNames.join(", ") : ""}
              </small>
            </div>
          ),
        },
        {
          key: "status",
          label: t("domains.status"),
          sortable: true,
          render: (value) => (
            <span>
              {t(`domains.status.${value}`)}
            </span>
          ),
        },
        {
          key: "notAfter",
          label: t("domains.expiry"),
          sortable: true,
          render: (value) => String(value || "—").slice(0, 10),
        },
        { key: "daysRemaining", label: t("domains.remaining"), sortable: true },
        {
          key: "actions",
          label: t("domains.actions"),
          render: (_, row) =>
            actions(row, [
              "certificates.get",
              "certificates.pem",
              "certificates.export",
              ...(row.source === "acme" && row.status !== "revoked"
                ? ["certificates.renew"]
                : []),
              "certificates.policy",
              ...(row.status !== "revoked" ? ["certificates.revoke"] : []),
              "certificates.delete",
            ]),
        },
      ];
    if (kind === "jobs")
      return [
        {
          key: "id",
          label: "ID",
          render: (value) => <code>{String(value)}</code>,
        },
        { key: "kind", label: t("domains.jobKind") },
        {
          key: "status",
          label: t("domains.status"),
          render: (value) => <span>{t(`domains.status.${value}`)}</span>,
        },
        { key: "error", label: t("domains.details") },
        {
          key: "actions",
          label: t("domains.actions"),
          render: (_, row) =>
            actions(row, [
              "certificates.job",
              ...(row.status === "queued" || row.status === "running"
                ? ["certificates.job-cancel"]
                : row.status === "failed" || row.status === "canceled"
                  ? ["certificates.job-retry"]
                  : []),
            ]),
        },
      ];
    return [
      { key: "name", label: t("domains.field.name"), sortable: true },
      {
        key: kind === "accounts" ? "email" : "zoneId",
        label: fieldLabel(kind === "accounts" ? "email" : "zoneId"),
      },
      {
        key: "actions",
        label: t("domains.actions"),
        render: (_, row) =>
          actions(
            row,
            kind === "accounts"
              ? ["certificates.account-delete"]
              : [
                  "certificates.provider-update",
                  "certificates.provider-delete",
                ],
          ),
      },
    ];
  };
  const table = (kind: keyof Inventory) => (
    <div className="domain-table-wrap">
      <SortableDataTable
        rows={visible(inventory[kind])}
        columns={cols(kind)}
        rowKey="id"
        caption={t(`domains.${kind}`)}
        emptyMessage={busy ? t("domains.loading") : t("domains.noResults")}
      />
    </div>
  );
  const choice = (key: keyof Inventory): Choice[] =>
    inventory[key].map((row) => ({
      value: row.id,
      label: `${row.name || row.id}${row.label ? ` · ${row.label}` : ""}`,
    }));
  return (
    <section
      ref={setContainer}
      className="domain-certificates domain-theme product-arc arc-ui registry-controls preferences-page workspace-settings workspace-page"
      aria-labelledby="domain-certificates-title"
      data-testid="domain-certificates"
      aria-busy={busy}
    >
      <header className="workspace-heading domain-certificates-heading">
        <h1 id="domain-certificates-title">{t("domains.title")}</h1>
        <Button
          variant="secondary"
          onClick={() => void refresh()}
          disabled={busy}
        >
          <RefreshCw size={16} />
          {t("domains.refresh")}
        </Button>
      </header>
      <SettingsGroup className="domain-summary">
        <div className="parameter-row">
          <span>{t("domains.domains")}</span>
          <span className="domain-total">
            {loaded.domains ? inventory.domains.length : "—"}
          </span>
        </div>
        <div className="parameter-row">
          <span>{t("domains.certificates")}</span>
          <span className="domain-total">
            {loaded.certificates ? inventory.certificates.length : "—"}
          </span>
        </div>
        <div className="parameter-row">
          <span>{t("domains.expiring")}</span>
          <span className="domain-total">
            {loaded.certificates
              ? inventory.certificates.filter(
                  (c) => c.status === "expiring" || c.status === "expired",
                ).length
              : "—"}
          </span>
        </div>
      </SettingsGroup>
      {error ? (
        <Alert role="alert" tone="danger" title={t("domains.loadError")}>
          {error}
        </Alert>
      ) : null}
      <Tabs className="domain-tabs" value={tab} onValueChange={setTab}>
        <TabsList aria-label={t("domains.sections")}>
          <TabsTrigger value="domains">
            <Globe2 size={16} aria-hidden="true" />
            {t("domains.domains")}
          </TabsTrigger>
          <TabsTrigger value="certificates">
            <ShieldCheck size={16} aria-hidden="true" />
            {t("domains.certificates")}
          </TabsTrigger>
          <TabsTrigger value="settings">
            <KeyRound size={16} aria-hidden="true" />
            {t("domains.settings")}
          </TabsTrigger>
          <TabsTrigger value="jobs">
            <Play size={16} aria-hidden="true" />
            {t("domains.jobs")}
          </TabsTrigger>
        </TabsList>
        <div className="domain-toolbar">
          <SettingsGroup>
            <label className="parameter-row">
              <span>{t("domains.search")}</span>
              <Input
                aria-label={t("domains.search")}
                type="search"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
            </label>
            <div className="parameter-row">
              <label htmlFor="domain-all-operations">
                {t("domains.allOperations")}
              </label>
              <Select
                id="domain-all-operations"
                portalContainer={container}
                aria-label={t("domains.allOperations")}
                value=""
                options={operations.map((op) => ({
                  value: op.name,
                  label: `${opLabel(op)} · ${op.name}`,
                }))}
                onValueChange={(name) => open(name)}
              />
            </div>
          </SettingsGroup>
        </div>
        <TabsContent value="domains">
          <section className="workspace-section">
            <div className="workspace-section-heading">
              <h2>{t("domains.domainList")}</h2>
            </div>
            <div className="settings-section-actions domain-card-actions">
              <Button variant="secondary" onClick={() => open("domains.batch")}>
                {t("domains.bulkAdd")}
              </Button>
              <Button onClick={() => open("domains.create")}>
                <Plus size={16} />
                {t("domains.add")}
              </Button>
            </div>
            {!inventory.domains.length && !busy && !error ? (
              <SettingsGroup>
                <EmptyState
                  icon={<Globe2 size={24} />}
                  title={t("domains.noDomains")}
                  description={t("domains.domainDescription")}
                />
              </SettingsGroup>
            ) : (
              table("domains")
            )}
          </section>
        </TabsContent>
        <TabsContent value="certificates">
          <section className="workspace-section">
            <div className="workspace-section-heading">
              <h2>{t("domains.certificateList")}</h2>
            </div>
            <div className="settings-section-actions domain-card-actions">
              <Button
                variant="secondary"
                onClick={() => open("certificates.import")}
              >
                <Upload size={16} />
                {t("domains.import")}
              </Button>
              <Button onClick={() => open("certificates.issue")}>
                <Plus size={16} />
                {t("domains.issue")}
              </Button>
            </div>
            {!inventory.certificates.length && !busy && !error ? (
              <SettingsGroup>
                <EmptyState
                  icon={<ShieldCheck size={24} />}
                  title={t("domains.noCertificates")}
                  description={t("domains.certificateDescription")}
                />
              </SettingsGroup>
            ) : (
              table("certificates")
            )}
          </section>
        </TabsContent>
        <TabsContent value="settings">
          <div className="settings-sections domain-settings">
            <section className="workspace-section">
              <div className="workspace-section-heading">
                <h2>{t("domains.accounts")}</h2>
              </div>
              <div className="settings-section-actions domain-card-actions">
                <Button onClick={() => open("certificates.account-create")}>
                  <Plus size={16} />
                  {t("domains.addAccount")}
                </Button>
              </div>
              {table("accounts")}
            </section>
            <section className="workspace-section">
              <div className="workspace-section-heading">
                <h2>{t("domains.providers")}</h2>
              </div>
              <div className="settings-section-actions domain-card-actions">
                <Button onClick={() => open("certificates.provider-create")}>
                  <Plus size={16} />
                  {t("domains.addProvider")}
                </Button>
              </div>
              {table("providers")}
            </section>
          </div>
        </TabsContent>
        <TabsContent value="jobs">
          <section className="workspace-section">
            <div className="workspace-section-heading">
              <h2>{t("domains.jobs")}</h2>
            </div>
            {table("jobs")}
          </section>
        </TabsContent>
      </Tabs>
      {action ? (
        <DomainOperation
          key={`${action.operation.name}/${action.params.id || "new"}`}
          action={action}
          container={container}
          domains={choice("domains")}
          accounts={choice("accounts")}
          providers={choice("providers")}
          channels={choice("channels")}
          onClose={() => setAction(null)}
          onResult={(value) => {
            setResult(value);
            void refresh();
          }}
        />
      ) : null}
      {result !== undefined ? (
        <section className="workspace-section">
          <div className="workspace-section-heading">
            <h2>{t("domains.result")}</h2>
          </div>
          <JsonViewer
            data={result}
            label={t("domains.result")}
            defaultExpandDepth={2}
            maxHeight={320}
          />
        </section>
      ) : null}
    </section>
  );
}

function DomainOperation({
  action,
  container,
  domains,
  accounts,
  providers,
  channels,
  onClose,
  onResult,
}: {
  action: Action;
  container: HTMLElement | null;
  domains: Choice[];
  accounts: Choice[];
  providers: Choice[];
  channels: Choice[];
  onClose: () => void;
  onResult: (result: unknown) => void;
}) {
  const { operation } = action;
  const [params, setParams] = useState(action.params),
    [values, setValues] = useState(action.seed),
    [raw, setRaw] = useState("{}"),
    [rawMode, setRawMode] = useState(false);
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);
  const fields = operation.bodySchema?.properties ?? {};
  const set = (name: string, value: string) =>
    setValues((old) => ({ ...old, [name]: value }));
  async function execute(event: React.FormEvent) {
    event.preventDefault();
    setError("");
    setBusy(true);
    const pending = new AbortController();
    request.current = pending;
    try {
      const body = operationBody(operation, values, raw, rawMode);
      const data = await operationRequest<unknown>(
        operation.name,
        { params },
        { body, signal: pending.signal },
      );
      if (pending.signal.aborted) return;
      if (data instanceof Blob) {
        const url = URL.createObjectURL(data),
          link = document.createElement("a");
        link.href = url;
        link.download =
          operation.name === "certificates.export"
            ? `certificate-${params.id}.zip`
            : `certificate-${params.id}.pem`;
        link.click();
        setTimeout(() => URL.revokeObjectURL(url), 1000);
        onResult({ downloaded: link.download });
      } else onResult(data ?? { status: "succeeded" });
      onClose();
    } catch (e) {
      if (!pending.signal.aborted) setError(message(e));
    } finally {
      if (!pending.signal.aborted) setBusy(false);
    }
  }
  const fieldRow = (
    name: string,
    label: string,
    control: import("react").ReactNode,
  ) => (
    <div key={name} className="parameter-row">
      <label htmlFor={`domain-field-${name}`}>{label}</label>
      <div className="domain-field-control">{control}</div>
    </div>
  );
  function field(name: string) {
    if (name === "revision" && !operation.bodySchema?.required?.includes(name))
      return null;
    const definition = fields[name],
      label = `${fieldLabel(name)}${operation.bodySchema?.required?.includes(name) ? " *" : ""}`,
      value = values[name] ?? "";
    const id = `domain-field-${name}`;
    let choices: Choice[] | undefined;
    if (name === "accountId") choices = accounts;
    else if (name === "providerId") choices = providers;
    else if (definition.enum && definition.type !== "boolean")
      choices = definition.enum.map((value) => ({
        value: String(value),
        label: String(value),
      }));
    if (definition.type === "boolean")
      return fieldRow(
        name,
        label,
        <Checkbox
          id={id}
          aria-label={label}
          checked={value === "true"}
          onCheckedChange={(checked) => set(name, String(checked === true))}
        />,
      );
    if (choices)
      return fieldRow(
        name,
        label,
        <Select
          id={id}
          portalContainer={container}
          aria-label={label}
          value={value}
          options={choices}
          onValueChange={(value) => set(name, value)}
        />,
      );
    if (name === "domainIds" || name === "channelIds") {
      const choices = name === "domainIds" ? domains : channels;
      let selected: string[] = [];
      try {
        selected = JSON.parse(value || "[]");
      } catch {}
      return (
        <div key={name} className="parameter-row">
          <span>{label}</span>
          <div className="domain-choices" role="group" aria-label={label}>
            {choices.map((choice) => (
              <Checkbox
                key={choice.value}
                label={choice.label}
                checked={selected.includes(choice.value)}
                onCheckedChange={(checked) =>
                  set(
                    name,
                    JSON.stringify(
                      checked
                        ? [...selected, choice.value]
                        : selected.filter((item) => item !== choice.value),
                    ),
                  )
                }
              />
            ))}
            {!choices.length ? <small>{t("domains.noChoices")}</small> : null}
          </div>
        </div>
      );
    }
    if (name === "certificatePem" || name === "privateKeyPem")
      return fieldRow(
        name,
        label,
        <div className="domain-pem">
          <FileDropzone
            label={fieldLabel(name)}
            description={t("domains.choosePem")}
            note={t("domains.privateMemory")}
            accept=".pem,.crt,.cer,.key,text/plain,application/x-pem-file"
            multiple={false}
            maxFiles={1}
            maxSize={definition.maxBytes}
            onFilesChange={(files) => {
              const file = files[0];
              if (file)
                void file
                  .text()
                  .then((text) => set(name, text))
                  .catch((e) => setError(message(e)));
              else set(name, "");
            }}
          />
          <ExpandedTextarea
            id={id}
            value={value}
            onChange={(event) => set(name, event.target.value)}
            rows={4}
            autoComplete="off"
            spellCheck={false}
          />
        </div>,
      );
    if (
      definition.type === "array" ||
      definition.type === "object" ||
      name === "notes"
    )
      return fieldRow(
        name,
        label,
        <ExpandedTextarea
          id={id}
          value={value}
          onChange={(event) => set(name, event.target.value)}
          rows={4}
          description={
            definition.type === "array" || definition.type === "object"
              ? t("domains.jsonInput")
              : undefined
          }
        />,
      );
    return fieldRow(
      name,
      label,
      <Input
        id={id}
        value={value}
        type={
          definition.writeOnly
            ? "password"
            : definition.type === "integer"
              ? "number"
              : "text"
        }
        inputMode={name === "email" ? "email" : undefined}
        min={definition.minimum}
        max={definition.maximum}
        autoComplete="off"
        onChange={(event) => set(name, event.target.value)}
        description={
          definition.default !== undefined
            ? `${t("domains.default")}: ${definition.default}`
            : name === "challenge"
              ? t("domains.challengeHelp")
              : undefined
        }
      />,
    );
  }
  return (
    <Dialog
      open={true}
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent
        container={container}
        className="domain-action-dialog"
        title={opLabel(operation)}
        onEscapeKeyDown={(event) => {
          if (busy) event.preventDefault();
        }}
        onPointerDownOutside={(event) => event.preventDefault()}
      >
        <form
          onSubmit={(event) => void execute(event)}
          className="domain-operation-form"
        >
          <div className="workspace-group-rows">
            <SettingsRows>
              {operation.parameters.map((name) =>
                fieldRow(
                  name,
                  fieldLabel(name),
                  <Input
                    id={`domain-field-${name}`}
                    value={params[name] ?? ""}
                    onChange={(event) =>
                      setParams((old) => ({
                        ...old,
                        [name]: event.target.value,
                      }))
                    }
                  />,
                ),
              )}
              {operation.bodySchema ? (
                <>
                  {fieldRow(
                    "rawMode",
                    t("domains.rawJson"),
                    <Checkbox
                      id="domain-field-rawMode"
                      aria-label={t("domains.rawJson")}
                      checked={rawMode}
                      onCheckedChange={(value) => {
                        if (value === true) {
                          const draft = Object.fromEntries(
                            Object.entries(values).map(([key, value]) => {
                              const type = fields[key]?.type;
                              if (type === "string") return [key, value];
                              try {
                                return [key, JSON.parse(value)];
                              } catch {
                                return [key, value];
                              }
                            }),
                          );
                          setRaw(JSON.stringify(draft, null, 2));
                        }
                        setRawMode(value === true);
                      }}
                    />,
                  )}
                  {rawMode
                    ? fieldRow(
                        "rawBody",
                        "JSON",
                        <ExpandedTextarea
                          id="domain-field-rawBody"
                          description={
                            schemaHasSecrets(operation.bodySchema)
                              ? t("domains.privateMemory")
                              : undefined
                          }
                          value={raw}
                          onChange={(event) => setRaw(event.target.value)}
                          rows={12}
                          spellCheck={false}
                        />,
                      )
                    : Object.keys(fields)
                        .sort(
                          (a, b) =>
                            Number(
                              !!operation.bodySchema?.required?.includes(b),
                            ) -
                            Number(
                              !!operation.bodySchema?.required?.includes(a),
                            ),
                        )
                        .map(field)}
                </>
              ) : null}
            </SettingsRows>
          </div>
          {operation.name === "certificates.issue" ? (
            <p>{t("domains.challengeHelp")}</p>
          ) : null}
          {error ? <p role="alert">{error}</p> : null}
          <div className="settings-section-actions domain-form-actions">
            <Button
              type="button"
              variant="secondary"
              disabled={busy}
              onClick={onClose}
            >
              {t("domains.cancel")}
            </Button>
            <Button type="submit" disabled={busy}>
              {busy ? t("domains.submitting") : t("domains.execute")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
