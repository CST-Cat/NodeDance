import { clipboardFeedback } from "../clipboard";
import UiCopyFeedback from "./UiCopyFeedback";
import type {
  AgentNode,
  ContainerTask,
  ContainerTaskAction,
  DashboardPreference,
  DockerContainerRecord,
  DockerInventory,
  SSHConnectionInfo,
} from "../api";
import { display, locale, t } from "../i18n";
import type { MetricsView } from "../metrics-contract";
import { afterRender, beforeDispose, cell, derived, watchState, lazyView } from "../state";
import VpsTelemetry from "./VpsTelemetry";
import UiDialog from "./UiDialog";
import NodeLogo from "./NodeLogo";
import CountryLogo from "./CountryLogo";

import { Fragment } from "react";
import { requestChoice } from "../dialogs";
import { __call, __classes, __each, __style, useController } from "../state";
import "./VpsCard.css";
const loadNodeFiles = () => import("./NodeFiles");
const NodeFiles = lazyView(loadNodeFiles);

type Props = {
  node: AgentNode;
  inspected?: boolean;
  metrics?: MetricsView;
  inventory?: DockerInventory;
  nodePreference?: DashboardPreference;
  sshConnection?: SSHConnectionInfo;
  sshLoading?: boolean;
  sshUserChoice?: string;
  sshError?: string;
  containerPreferences: DashboardPreference[];
  preferenceIdentities: Record<string, string>;
  online: boolean;
  pending: boolean;
  dockerStale: boolean;
  currentTime: number;
  tasks: ContainerTask[];
  taskSubmitting: Record<string, boolean>;
  taskErrors: Record<string, string>;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function VpsCard(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const filesDialogOpen = cell(false);
      const filesPath = cell("/");
      const filesButton = cell<HTMLButtonElement>();
      async function closeFiles() {
        filesDialogOpen.value = false;
        await afterRender();
        filesButton.value?.focus({ preventScroll: true });
      }
      const dockerDialogOpen = cell(false);
      const dockerDetailsButton = cell<HTMLButtonElement>();
      async function openDockerDetails() {
        dockerDialogOpen.value = true;
        await afterRender();
        // Focus the dialog's close affordance (shared header X).
        document
          .querySelector<HTMLButtonElement>(".vps-docker-dialog .ui-dialog-close")
          ?.focus({ preventScroll: true });
      }
      async function closeDockerDetails() {
        dockerDialogOpen.value = false;
        await afterRender();
        dockerDetailsButton.value?.focus({ preventScroll: true });
      }
      const props = __props;
      const emit = __emit;
      const title = derived(
        () => props.nodePreference?.alias || props.node.displayName,
      );
      const selectedSSHUser = derived({
        get: () => props.sshUserChoice ?? "",
        set: (user) =>
          emit("chooseSshUser", {
            nodeId: props.node.nodeId,
            user,
          }),
      });
      const sshCommand = derived(() => {
        const host = props.nodePreference?.sshHost ?? "";
        const user = props.nodePreference?.sshUser ?? "";
        const port = props.nodePreference?.sshPort || 22;
        if (!host || !user)
          return (
            (selectedSSHUser.value
              ? props.sshConnection?.commands?.[selectedSSHUser.value]
              : props.sshConnection?.command) ?? ""
          );
        if (
          host.startsWith("-") ||
          !/^[A-Za-z0-9._:%-]+$/.test(host) ||
          !/^[A-Za-z_][A-Za-z0-9_.-]{0,63}$/.test(user) ||
          !Number.isInteger(port) ||
          port < 1 ||
          port > 65535
        )
          return "";
        return `ssh -p ${port} ${user}@${host}`;
      });
      // Discovery/unavailable messages retain their existing anchored hint;
      // completed copies use the shared inline feedback.
      const copied = clipboardFeedback();
      const sshFeedback = cell("");
      const sshFeedbackKind = cell<"info" | "error">("info");
      const sshFeedbackStyle = cell<Record<string, string>>({});
      const shortcutRow = cell<HTMLElement>();
      const sshShortcutButton = cell<HTMLButtonElement>();
      const sshFeedbackBubble = cell<HTMLElement>();
      let sshFeedbackTimer: ReturnType<typeof setTimeout> | undefined;
      beforeDispose(() => {
        if (sshFeedbackTimer) clearTimeout(sshFeedbackTimer);
      });
      function hideSshFeedback() {
        if (sshFeedbackTimer) {
          clearTimeout(sshFeedbackTimer);
          sshFeedbackTimer = undefined;
        }
        sshFeedback.value = "";
      }
      async function showSshFeedback(
        message: string,
        kind: "info" | "error" = "info",
      ) {
        sshFeedback.value = message;
        sshFeedbackKind.value = kind;
        if (sshFeedbackTimer) {
          clearTimeout(sshFeedbackTimer);
          sshFeedbackTimer = undefined;
        }
        // Stay invisible during measurement so it never flashes unanchored.
        sshFeedbackStyle.value = { visibility: "hidden" };
        sshFeedbackTimer = setTimeout(() => {
          sshFeedbackTimer = undefined;
          sshFeedback.value = "";
        }, 2600);
        await afterRender();
        if (sshFeedback.value !== message) return;
        const row = shortcutRow.value,
          button = sshShortcutButton.value,
          bubble = sshFeedbackBubble.value;
        if (!row || !button || !bubble) return;
        const width = bubble.offsetWidth;
        sshFeedbackStyle.value = {
          left: `${Math.round(
            Math.min(
              Math.max(
                button.offsetLeft +
                  button.offsetWidth / 2 -
                  width / 2,
                0,
              ),
              Math.max(0, row.clientWidth - width),
            ),
          )}px`,
          bottom: `${Math.round(
            row.offsetHeight - button.offsetTop + 6,
          )}px`,
          visibility: "visible",
        };
      }
      const waitingForSSH = cell(false);
      watchState(
        () => [
          props.sshConnection,
          sshCommand.value,
          props.sshLoading,
          props.sshError,
        ],
        () => {
          if (!waitingForSSH.value) return;
          if (copiableAccounts().length > 0) {
            waitingForSSH.value = false;
            // Discovery resolved into usable accounts: open the shared
            // account dialog instead of copying anything automatically.
            void copySSHCommand();
          } else if (!props.sshLoading) {
            waitingForSSH.value = false;
            void showSshFeedback(t("ssh.unavailable"), "error");
          }
        },
      );
      const displayContainers = derived(() => {
        const containers = [...(props.inventory?.containers ?? [])];
        containers.sort((left, right) => {
          const leftPreference = preferenceFor(left);
          const rightPreference = preferenceFor(right);
          if (leftPreference?.pinned !== rightPreference?.pinned)
            return leftPreference?.pinned ? -1 : 1;
          if (
            (leftPreference?.sortOrder ?? 0) !==
            (rightPreference?.sortOrder ?? 0)
          ) {
            return (
              (leftPreference?.sortOrder ?? 0) -
              (rightPreference?.sortOrder ?? 0)
            );
          }
          return containerTitle(left).localeCompare(
            containerTitle(right),
            locale.value,
          );
        });
        return containers;
      });
      function preferenceFor(
        record: DockerContainerRecord,
      ): DashboardPreference | undefined {
        const identity = props.preferenceIdentities[record.container.id];
        if (!identity) return undefined;
        const kind = record.container.compose ? "compose_service" : "container";
        return props.containerPreferences.find(
          (item) => item.targetKind === kind && item.identity === identity,
        );
      }
      function containerTitle(record: DockerContainerRecord): string {
        return (
          preferenceFor(record)?.alias ||
          record.container.name ||
          record.container.id.slice(0, 12)
        );
      }
      function uptimeSummary(): string {
        const metric = props.metrics?.metrics.uptime;
        const seconds = metric?.value?.seconds;
        if (seconds === undefined || !Number.isFinite(seconds))
          return metric?.status === "error"
            ? t("m.6aa27f14bff1")
            : t("m.4d8c1c5b4283");
        const receivedAt = Date.parse(props.metrics?.receivedAt ?? "");
        const elapsed =
          props.online && Number.isFinite(receivedAt)
            ? Math.max(0, Math.floor((props.currentTime - receivedAt) / 1000))
            : 0;
        return t("m.8391da809dd9", {
          "0": formatDuration(seconds + elapsed),
          "1": props.online ? "" : t("m.6360a8989089"),
        });
      }
      function formatDuration(seconds: number): string {
        const days = Math.floor(seconds / 86_400);
        const hours = Math.floor((seconds % 86_400) / 3_600);
        const minutes = Math.floor((seconds % 3_600) / 60);
        return days > 0
          ? t("m.295f367bb72a", {
              "0": days,
              "1": hours,
            })
          : hours > 0
            ? t("m.3cde3f0ae16c", {
                "0": hours,
                "1": minutes,
              })
            : t("m.c65627ead130", {
                "0": minutes,
              });
      }
      function dockerStatus(): string {
        if (
          props.inventory?.dockerAvailability === "unavailable" ||
          props.inventory?.staleReason === "docker_capability_unavailable"
        )
          return t("m.d3e431f9d10b");
        if (props.inventory?.dockerAvailability === "available") {
          if (props.dockerStale || props.inventory.dataStale)
            return t("m.1a6858956811");
          if (props.inventory.health?.errorKind === "api_incompatible")
            return t("m.d56e55420ff6");
          if (props.inventory.health?.reason) return t("m.04dea0459ab7");
          return t("m.81e93ba5b0a1");
        }
        return props.pending
          ? t("m.270c0a7750e1")
          : props.online
            ? t("m.c12a50c262ca")
            : t("m.de300a2b89b7");
      }
      // The single status line under the node name is driven only by the
      // node record: enrollment and connection state map to localized text.
      function nodeStateText(): string {
        if (props.pending) return t("m.a890b3be09aa");
        if (!props.online) return t("m.d07581c22bb2");
        return props.node.agentVersion || t("m.d07581c22bb2");
      }
      function healthText(record: DockerContainerRecord): string {
        const container = record.container;
        if (!container.healthcheckConfigured || container.health === "none")
          return t("m.5fac1c671a0d");
        return (
          (
            {
              healthy: t("m.aa18b6dcd893"),
              unhealthy: t("m.428fb8bfeecf"),
              starting: t("m.33439d263173"),
              unknown: t("m.4d8c1c5b4283"),
            } as Record<string, string>
          )[container.health] ?? container.health
        );
      }
      function serviceURL(record: DockerContainerRecord): string {
        const candidate = preferenceFor(record)?.serviceUrl;
        if (!candidate) return "";
        try {
          const url = new URL(candidate);
          return ["http:", "https:"].includes(url.protocol) &&
            !url.username &&
            !url.password &&
            !url.hash
            ? url.href
            : "";
        } catch {
          return "";
        }
      }
      function networkText(record: DockerContainerRecord): string {
        const container = record.container;
        if (container.hostNetwork) return t("m.7286fcd72f9e");
        const networks = container.networks ?? [];
        if (!networks.length) return t("m.ad2c9a6cff20");
        return networks
          .map((network) => {
            const addresses = [
              network.ipv4 && `IPv4 ${network.ipv4}`,
              network.ipv6 && `IPv6 ${network.ipv6}`,
            ].filter(Boolean);
            return t("m.8391da809dd9", {
              "0": network.name,
              "1": addresses.length
                ? `：${addresses.join(" · ")}`
                : t("m.0dbb481d93ec"),
            });
          })
          .join("；");
      }
      function portText(record: DockerContainerRecord): string {
        const container = record.container;
        const ports = container.ports ?? [];
        if (container.hostNetwork) {
          const prefix = t("m.91517a7693e7");
          if (!ports.length)
            return t("m.74e8e0d1e8fd", {
              "0": prefix,
            });
          const declared = ports.map((port) => {
            const label = `${port.containerPort}/${port.protocol}`;
            if (port.configured.length)
              return t("m.a35b20b5cb32", {
                "0": label,
              });
            if (port.exposed)
              return t("m.ae6cee47ad79", {
                "0": label,
              });
            return t("m.9b985579e81e", {
              "0": label,
            });
          });
          return `${prefix}；${declared.join("，")}`;
        }
        if (!ports.length) return t("m.e1548d8f0d87");
        return ports
          .map((port) => {
            if (port.published.length) {
              return port.published
                .map((binding) => {
                  const rawHost = binding.ip || "*";
                  const formattedHost =
                    rawHost.includes(":") && !rawHost.startsWith("[")
                      ? `[${rawHost}]`
                      : rawHost;
                  const scope =
                    rawHost === "*" || rawHost === "0.0.0.0" || rawHost === "::"
                      ? t("m.a54cd64d75d5")
                      : rawHost === "::1" ||
                          /^127(?:\.\d{1,3}){3}$/.test(rawHost)
                        ? t("m.b07e34088b42")
                        : "";
                  return `${formattedHost}:${binding.port} → ${port.containerPort}/${port.protocol}${scope}`;
                })
                .join(", ");
            }
            if (port.configured.length)
              return t("m.639b66233cf4", {
                "0": port.containerPort,
                "1": port.protocol,
              });
            if (port.exposed)
              return t("m.c39c143634dc", {
                "0": port.containerPort,
                "1": port.protocol,
              });
            return `${port.containerPort}/${port.protocol}`;
          })
          .join("；");
      }
      function containerUptime(record: DockerContainerRecord): string {
        const startedAt = record.container.startedAt;
        if (!record.container.running || !startedAt) return t("m.4d8c1c5b4283");
        const started = Date.parse(startedAt);
        if (!Number.isFinite(started) || started > props.currentTime)
          return t("m.4d8c1c5b4283");
        return formatDuration(Math.floor((props.currentTime - started) / 1000));
      }
      function containerCreated(record: DockerContainerRecord): string {
        if (!record.container.createdAt) return t("m.4d8c1c5b4283");
        const created = Date.parse(record.container.createdAt);
        return Number.isFinite(created)
          ? new Date(created).toLocaleString(locale.value)
          : t("m.4d8c1c5b4283");
      }
      function taskKey(containerId: string): string {
        return `${props.node.nodeId}:${containerId}`;
      }
      function taskFor(
        record: DockerContainerRecord,
      ): ContainerTask | undefined {
        return props.tasks
          .filter(
            (task) =>
              task.targetId === record.container.id ||
              (task.action === "rebuild" &&
                task.status === "succeeded" &&
                task.result.resourceRevision === record.container.id),
          )
          .sort(
            (left, right) =>
              Date.parse(right.createdAt) - Date.parse(left.createdAt),
          )[0];
      }
      function taskLabel(task: ContainerTask | undefined): string {
        if (!task) return "";
        if (task.status === "queued") return t("m.7ffc79834df2");
        if (task.status === "running")
          return t("m.e71170d00ddc", {
            "0": task.progress.phase,
          });
        if (task.status === "succeeded") return t("m.0a1dae196200");
        if (task.status === "unknown" || task.status === "timed_out")
          return t("m.400d8fa7bdd9");
        return task.status === "failed"
          ? t("m.9344597efd49")
          : t("m.6f96c2add214");
      }
      function canOperate(record: DockerContainerRecord): boolean {
        return Boolean(
          props.online &&
            props.node.agentId &&
            props.inventory?.dockerAvailability === "available" &&
            props.inventory.staleReason !== "docker_capability_unavailable" &&
            !props.dockerStale &&
            !record.container.stale &&
            !props.taskSubmitting[taskKey(record.container.id)],
        );
      }
      function emitAction(
        record: DockerContainerRecord,
        action: ContainerTaskAction,
      ) {
        if (!canOperate(record)) return;
        emit("containerAction", {
          node: props.node,
          record,
          action,
        });
      }
      type SshAccountChoice = {
        value: string;
        label: string;
        command: string;
      };
      // The saved custom connection joins the same account dialog as the
      // discovered accounts. It stays selectable while discovery is pending or
      // unavailable and is the preferred default; discovered alternatives are
      // preserved without duplicating an identical command.
      function savedAccountChoice(): SshAccountChoice | undefined {
        const host = props.nodePreference?.sshHost ?? "";
        const user = props.nodePreference?.sshUser ?? "";
        const port = props.nodePreference?.sshPort || 22;
        if (
          !host ||
          !user ||
          host.startsWith("-") ||
          !/^[A-Za-z0-9._:%-]+$/.test(host) ||
          !/^[A-Za-z_][A-Za-z0-9_.-]{0,63}$/.test(user) ||
          !Number.isInteger(port) ||
          port < 1 ||
          port > 65535
        )
          return undefined;
        return {
          value: `${user}@${host}:${port}`,
          label: port === 22 ? `${user}@${host}` : `${user}@${host}:${port}`,
          command: `ssh -p ${port} ${user}@${host}`,
        };
      }
      function sshAccountChoices(): SshAccountChoice[] {
        const accounts: SshAccountChoice[] = [];
        const saved = savedAccountChoice();
        if (saved) accounts.push(saved);
        const connection = props.sshConnection;
        if (connection) {
          const detected: SshAccountChoice[] = [
            {
              value: "",
              label: connection.user || t("ssh.clientDefault"),
              command: connection.command ?? "",
            },
          ];
          for (const user of connection.users ?? []) {
            const command = connection.commands?.[user];
            if (command && user !== connection.user)
              detected.push({ value: user, label: user, command });
          }
          for (const account of detected)
            if (
              !account.command ||
              !accounts.some((existing) => existing.command === account.command)
            )
              accounts.push(account);
        }
        return accounts;
      }
      function copiableAccounts(): SshAccountChoice[] {
        return sshAccountChoices().filter((account) => account.command);
      }
      async function copySSHCommand() {
        copied.reset();
        // Every copy is confirmed inside the shared account dialog, with one
        // or several accounts alike: the button click itself never copies,
        // and Cancel or Escape keep the clipboard untouched.
        if (copiableAccounts().length > 0) return void chooseAndCopySSH();
        // Without any known candidate, discover first; the floating bubble
        // reports the pending detection and, if nothing usable appears, the
        // unavailability instead of an unusable choice dialog.
        waitingForSSH.value = true;
        void showSshFeedback(t("ssh.discovering"));
        emit("discoverSsh", props.node);
      }
      async function chooseAndCopySSH() {
        const accounts = copiableAccounts();
        // The saved custom connection is preferred when present; a previously
        // confirmed account and the first offer follow in that order.
        const saved = savedAccountChoice();
        const preferred = saved
          ? accounts.find((account) => account.command === saved.command)
          : undefined;
        const picked = await requestChoice(
          t("ssh.chooseAccount"),
          accounts.map((account) => ({
            value: account.value,
            label: account.label,
            detail: account.command,
          })),
          (preferred ??
            accounts.find(
              (account) => account.value === selectedSSHUser.value,
            ) ??
            accounts[0])?.value ?? "",
          t("ssh.copyAction"),
        );
        if (picked === null) {
          // Cancelled and Escape stay without any copy.
          hideSshFeedback();
          return;
        }
        const account = accounts.find(
          (candidate) => candidate.value === picked,
        );
        if (!account) {
          void showSshFeedback(t("ssh.unavailable"), "error");
          return;
        }
        selectedSSHUser.value = picked;
        await copyText(account.command);
      }
      async function copyText(command: string) {
        if (!command) {
          await showSshFeedback(t("ssh.unavailable"), "error");
          return;
        }
        hideSshFeedback();
        await copied.copy(command);
      }

      function inspectCard(event: MouseEvent) {
        if (
          (event.target as HTMLElement).closest(
            "button,a,input,select,textarea,label,summary,[role=button]",
          ) ||
          (window.getSelection() && !window.getSelection()?.isCollapsed)
        )
          return;
        emit("inspect", props.node);
      }
      return () => (
        <>
          <article
            tabIndex={0}
            data-inspected={__props.inspected as any}
            data-node-id={__props.node.nodeId as any}
            data-online={__props.online as any}
            data-docker-stale={__props.dockerStale as any}
            className={__classes("vps-card")}
            onClick={(event: any) => {
              __call(inspectCard, event);
            }}
            onKeyDown={(event: any) => {
              if (event.key !== " ") return;
              if (event.target !== event.currentTarget) return;
              event.preventDefault();
              emit("inspect", __props.node);
            }}
            data-v-31185faa=""
          >
            <header className={__classes("vps-card-header")} data-v-31185faa="">
              <div className={__classes("vps-card-heading")} data-v-31185faa="">
                <span className="vps-node-icon"><CountryLogo country={__props.node.country} /></span>
                <div data-v-31185faa="">
                  {__props.nodePreference?.group ? (
                    <span className={__classes("eyebrow")} data-v-31185faa="">
                      {__props.nodePreference.group}
                    </span>
                  ) : null}
                  <h2 data-v-31185faa="">
                    <button
                      type="button"
                      className={__classes("vps-name")}
                      onClick={(event: any) => {
                        emit("view", __props.node);
                      }}
                      data-v-31185faa=""
                    >
                      {title.value}
                    </button>
                  </h2>
                </div>
                <span
                  className={__classes("vps-node-icon vps-service-icon")}
                  data-v-31185faa=""
                >
                  <NodeLogo icon={__props.nodePreference?.icon} platform={__props.metrics?.metrics.system.platform.value} os={__props.metrics?.metrics.system.os.value} />
                </span>
                <p className={__classes("vps-card-version")} data-v-31185faa="">
                  <span className={__classes("agent-version")} data-v-31185faa="">
                    {nodeStateText()}
                  </span>
                  {props.pending ? null : (
                    <Fragment>
                      {" · "}
                      {t("overview.uptime")} {uptimeSummary()}
                    </Fragment>
                  )}
                </p>
              </div>
            </header>
            {__props.nodePreference?.notes ? (
              <p className={__classes("vps-card-note")} data-v-31185faa="">
                {__props.nodePreference.notes}
              </p>
            ) : null}
            <VpsTelemetry
              nodeId={__props.node.nodeId}
              metrics={__props.metrics as any}
              online={__props.online as any}
              currentTime={__props.currentTime as any}
              data-v-31185faa=""
            ></VpsTelemetry>
            <nav
              aria-label={
                t("m.c820131d2266", {
                  "0": title.value,
                }) as any
              }
              className={__classes("vps-card-shortcuts")}
              ref={__dom(shortcutRow)}
              data-v-31185faa=""
            >
              <button
                type="button"
                disabled={!__props.online as any}
                onClick={(event: any) => {
                  emit("quickAction", {
                    node: __props.node,
                    action: "terminal",
                  });
                }}
                data-v-31185faa=""
              >
                {t("m.e2a76ef1f12e")}
              </button>
              <button
                type="button"
                disabled={!__props.online as any}
                ref={__dom(filesButton)}
                aria-haspopup="dialog"
                data-testid="node-files-shortcut"
                onPointerEnter={() => { void loadNodeFiles().then(module => module.preloadFileManager()).catch(() => {}); }}
                onFocus={() => { void loadNodeFiles().then(module => module.preloadFileManager()).catch(() => {}); }}
                onClick={() => { filesDialogOpen.value = true; }}
                data-v-31185faa=""
              >
                {t("m.39932f24fe11")}
              </button>
              <button
                type="button"
                data-testid="node-ssh-shortcut"
                aria-label={t("m.12d40d9f555d")}
                ref={__dom(sshShortcutButton)}
                title={sshCommand.value || (t("ssh.automatic") as any)}
                onClick={(event: any) => {
                  void copySSHCommand();
                }}
                data-v-31185faa=""
              >
                <UiCopyFeedback state={copied.state.value}>{t("m.12d40d9f555d")}</UiCopyFeedback>
              </button>
              <button
                type="button"
                aria-haspopup="dialog"
                data-testid="node-docker-details"
                ref={__dom(dockerDetailsButton)}
                onClick={(event: any) => {
                  void openDockerDetails();
                }}
                data-v-31185faa=""
              >
                {t("m.eee901e4cc16")}
              </button>
              {sshFeedback.value ? (
                <span
                  role="status"
                  ref={__dom(sshFeedbackBubble)}
                  data-kind={sshFeedbackKind.value as any}
                  className={__classes("vps-card-ssh-pop")}
                  style={__style(sshFeedbackStyle.value) as any}
                  data-v-31185faa=""
                >
                  {display(sshFeedback.value)}
                </span>
              ) : null}
            </nav>
          </article>
          {filesDialogOpen.value ? <NodeFiles dialog nodeId={props.node.nodeId} nodeName={title.value} path={filesPath.value}
            onClose={() => { void closeFiles(); }} onPathChange={(path: string) => { filesPath.value = path; }} /> : null}
          {dockerDialogOpen.value ? <UiDialog open
            title={`${t("m.eee901e4cc16")} · ${title.value}`}
            className="ui-dialog-wide vps-docker-dialog"
            onClose={() => { void closeDockerDetails(); }}>
              <section
                id={`docker-${__props.node.nodeId}` as any}
                data-available={
                  __props.inventory?.dockerAvailability || ("unknown" as any)
                }
                data-stale={__props.dockerStale as any}
                className={__classes("vps-card-docker")}
                data-v-31185faa=""
              >
                <header data-v-31185faa="">
                  <strong data-v-31185faa="">{t("m.7cabcc483eb4")}</strong>
                  <span data-v-31185faa="">
                    {__props.inventory?.containers.length ?? "—"}
                    {" · "}
                    {dockerStatus()}
                  </span>
                </header>
                {!__props.inventory ? (
                  <p className={__classes("vps-card-empty")} data-v-31185faa="">
                    {__props.pending
                      ? t("m.0470e99d0fc7")
                      : __props.online
                        ? t("m.5a7828919f0f")
                        : t("m.16f23efa02f5")}
                  </p>
                ) : __props.inventory.dockerAvailability === "unavailable" ||
                  __props.inventory.staleReason ===
                    "docker_capability_unavailable" ? (
                  <p className={__classes("vps-card-empty")} data-v-31185faa="">
                    {t("m.0b7e6af1b292")}
                    {__props.inventory.health?.reason ||
                      __props.inventory.staleReason ||
                      ""}
                  </p>
                ) : null}
                {__props.inventory &&
                __props.inventory.containers.length === 0 &&
                __props.inventory.dockerAvailability !== "unavailable" &&
                __props.inventory.staleReason !==
                  "docker_capability_unavailable" ? (
                  <p className={__classes("vps-card-empty")} data-v-31185faa="">
                    {__props.dockerStale
                      ? t("m.ff2c9544c1dc")
                      : t("m.20165da39974")}
                  </p>
                ) : __props.inventory && displayContainers.value.length > 0 ? (
                  <div
                    className={__classes("vps-card-container-list")}
                    data-v-31185faa=""
                  >
                    <Fragment>
                      {__each(displayContainers.value, (record) => (
                        <article
                          key={record.container.id as any}
                          data-stale={
                            __props.dockerStale ||
                            (record.container.stale as any)
                          }
                          className={__classes("vps-card-container")}
                          data-v-31185faa=""
                        >
                          <span
                            aria-hidden="true"
                            className={__classes("vps-card-container-icon")}
                            data-v-31185faa=""
                          >
                            <NodeLogo icon={preferenceFor(record)?.icon || "box"} />
                          </span>
                          <div
                            className={__classes("vps-card-container-copy")}
                            data-v-31185faa=""
                          >
                            <strong data-v-31185faa="">
                              {containerTitle(record)}
                            </strong>
                            <small data-v-31185faa="">
                              {record.container.name ||
                                record.container.id.slice(0, 12)}
                              {" · "}
                              {record.container.image || t("m.45c82e228c16")}
                              {" · "}
                              {record.container.compose
                                ? `Compose · ${record.container.compose.project}/${record.container.compose.service}`
                                : t("m.ba4ddea11c26")}
                              {" · "}
                              {healthText(record)}
                            </small>
                            <small data-v-31185faa="">
                              {networkText(record)}
                              {" · "}
                              {portText(record)}
                            </small>
                            <small data-v-31185faa="">
                              {t("m.b890b09aa795")}
                              {containerCreated(record)}
                              {t("m.f8dd4dc7e620")}
                              {containerUptime(record)}
                            </small>
                            {record.container.healthReason ||
                            record.container.unavailableReason ? (
                              <small
                                className={__classes("vps-card-reason")}
                                data-v-31185faa=""
                              >
                                {record.container.healthReason ||
                                  record.container.unavailableReason}
                              </small>
                            ) : null}
                            {serviceURL(record) ? (
                              <a
                                href={serviceURL(record) as any}
                                target="_blank"
                                rel="noopener noreferrer"
                                className={__classes("vps-card-service-link")}
                                data-v-31185faa=""
                              >
                                {t("m.e4fd01edc24f")}
                              </a>
                            ) : null}
                            {__props.dockerStale || record.container.stale ? (
                              <small
                                className={__classes("vps-card-stale")}
                                data-v-31185faa=""
                              >
                                {t("m.6969fe6bf481")}
                              </small>
                            ) : null}
                            {taskLabel(taskFor(record)) ? (
                              <small
                                role="status"
                                className={__classes("vps-card-task")}
                                data-v-31185faa=""
                              >
                                {taskLabel(taskFor(record))}
                              </small>
                            ) : null}
                            {__props.taskErrors[
                              taskKey(record.container.id)
                            ] ? (
                              <small
                                role="alert"
                                className={__classes("vps-card-error")}
                                data-v-31185faa=""
                              >
                                {display(
                                  __props.taskErrors[
                                    taskKey(record.container.id)
                                  ],
                                )}
                              </small>
                            ) : null}
                            <div
                              aria-label={t("m.9fb9e5641587") as any}
                              className={__classes("vps-card-actions")}
                              data-v-31185faa=""
                            >
                              {record.container.paused ? (
                                <button
                                  type="button"
                                  disabled={!canOperate(record) as any}
                                  onClick={(event: any) => {
                                    emitAction(record, "resume");
                                  }}
                                  data-v-31185faa=""
                                >
                                  {t("m.e0534b8a4e46")}
                                </button>
                              ) : record.container.running ? (
                                <Fragment>
                                  <button
                                    type="button"
                                    disabled={!canOperate(record) as any}
                                    onClick={(event: any) => {
                                      emitAction(record, "stop");
                                    }}
                                    data-v-31185faa=""
                                  >
                                    {t("m.ca4d973c0b00")}
                                  </button>
                                  <button
                                    type="button"
                                    disabled={!canOperate(record) as any}
                                    onClick={(event: any) => {
                                      emitAction(record, "restart");
                                    }}
                                    data-v-31185faa=""
                                  >
                                    {t("m.562822892865")}
                                  </button>
                                </Fragment>
                              ) : (
                                <button
                                  type="button"
                                  disabled={!canOperate(record) as any}
                                  onClick={(event: any) => {
                                    emitAction(record, "start");
                                  }}
                                  data-v-31185faa=""
                                >
                                  {t("m.56410fc65314")}
                                </button>
                              )}
                            </div>
                          </div>
                          <span
                            data-running={record.container.running as any}
                            className={__classes("vps-card-state")}
                            data-v-31185faa=""
                          >
                            {record.container.state || t("m.4d8c1c5b4283")}
                          </span>
                        </article>
                      ))}
                    </Fragment>
                    {__props.dockerStale ? (
                      <p
                        className={__classes("vps-card-stale")}
                        data-v-31185faa=""
                      >
                        {t("m.188cce5ffdcd")}
                      </p>
                    ) : null}
                  </div>
                ) : null}
              </section>
          </UiDialog> : null}
        </>
      );
    },
    input,
    [
      "node",
      "inspected",
      "metrics",
      "inventory",
      "nodePreference",
      "sshConnection",
      "sshLoading",
      "sshUserChoice",
      "sshError",
      "containerPreferences",
      "preferenceIdentities",
      "online",
      "pending",
      "dockerStale",
      "currentTime",
      "tasks",
      "taskSubmitting",
      "taskErrors",
    ],
    [],
    {},
    true,
  );
}
