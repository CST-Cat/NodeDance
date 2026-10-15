import { api, type TailscaleDeploymentTask, type TailscalePeer } from "../api";
import { display, t } from "../i18n";
import { afterMount, beforeDispose, cell, derived, observable } from "../state";
import AgentEnrollment from "./AgentEnrollment";

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import "./TailscaleDiscovery.css";
import UiSelect from "./UiSelect";
type Props = {} & { children?: import("react").ReactNode; [key: string]: any };
export default function TailscaleDiscovery(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const emit = __emit;
      const peers = cell<TailscalePeer[]>([]);
      const loading = cell(false);
      const discovering = cell(false);
      const error = cell("");
      const notice = cell("");
      const selected = cell<TailscalePeer | null>(null);
      const fingerprint = cell("");
      const fingerprintLoading = cell(false);
      const task = cell<TailscaleDeploymentTask | null>(null);
      const unresolvedDeployments = cell<TailscaleDeploymentTask[]>([]);
      const activeDeploymentKey = cell("");
      const activeDeploymentSignature = cell("");
      let disposed = false;
      const resolutionOutcome = cell<"succeeded" | "failed">("succeeded");
      const resolutionObservedState = cell("connected");
      const resolutionConfirmed = cell(false);
      const credentials = observable({
        user: "",
        password: "",
        privateKey: "",
        passphrase: "",
      });
      const form = observable({
        displayName: "",
        fileRoot: "",
        disableFileRoot: false,
        coreUrl:
          window.location.protocol === "https:" ? window.location.origin : "",
        fallbackUrl: "",
        allowFallback: false,
        confirmHostKey: false,
        confirmChangedHostKey: false,
        usePrivateKey: false,
      });
      const candidates = derived(() =>
        peers.value.filter((peer) => !peer.managed && peer.class === "linux"),
      );
      const unsupported = derived(() =>
        peers.value.filter((peer) => !peer.managed && peer.class !== "linux"),
      );
      const managed = derived(() => peers.value.filter((peer) => peer.managed));
      const taskFinished = derived(
        () =>
          !!task.value &&
          ["succeeded", "failed", "timed_out", "canceled", "unknown"].includes(
            task.value.status,
          ),
      );
      const rescueCommand = derived(() => {
        if (!selected.value?.ips.length) return "";
        const address = selected.value.ips[0];
        const host = address.includes(":") ? `[${address}]` : address;
        return t("m.9dee018adddc", {
          "0": shellQuote(
            t("m.6a82133ca245", {
              "0": credentials.user || t("m.a0b366e8b611"),
              "1": host,
            }),
          ),
        });
      });
      function peerLabel(peer: TailscalePeer): string {
        return peer.name || peer.dnsName || peer.identity;
      }
      function stateLabel(peer: TailscalePeer): string {
        return peer.online ? t("m.b9086662b1df") : t("m.be1b4f3c6c1c");
      }
      function clearNotice() {
        error.value = "";
        notice.value = "";
      }
      function changeResolutionOutcome(event: Event) {
        const value = (event.target as HTMLSelectElement).value;
        if (value !== "succeeded" && value !== "failed") return;
        resolutionConfirmed.value = false;
        resolutionOutcome.value = value;
        resolutionObservedState.value =
          value === "succeeded" ? "connected" : "installation_failed";
      }
      function changeResolutionObservedState(event: Event) {
        const value = (event.target as HTMLSelectElement).value;
        if (!["connected", "unavailable", "absent"].includes(value)) return;
        resolutionConfirmed.value = false;
        resolutionObservedState.value = value;
      }
      async function discover() {
        if (disposed) return;
        clearNotice();
        discovering.value = true;
        try {
          const result = await api.tailscalePeers();
          if (disposed) return;
          peers.value = result.peers;
          if (selected.value)
            selected.value =
              peers.value.find(
                (peer) => peer.identity === selected.value?.identity,
              ) ?? null;
          if (!peers.value.length) notice.value = t("m.8bcf5c4107b5");
        } catch (reason) {
          peers.value = [];
          error.value = message(reason);
        } finally {
          discovering.value = false;
        }
      }
      async function refreshDeploymentTasks() {
        if (disposed) return;
        try {
          const result = await api.tailscaleDeployments();
          if (disposed) return;
          unresolvedDeployments.value = result.tasks;
          if (task.value) {
            const current = result.tasks.find(
              (candidate) => candidate.taskId === task.value?.taskId,
            );
            if (current) task.value = current;
          } else {
            task.value = result.tasks[0] ?? null;
          }
        } catch (reason) {
          error.value = t("m.5b47ff2c5947", {
            "0": message(reason),
          });
        }
      }
      function inspectDeploymentTask(deployment: TailscaleDeploymentTask) {
        task.value = deployment;
        resolutionConfirmed.value = false;
      }
      function choose(peer: TailscalePeer) {
        clearNotice();
        selected.value = peer;
        fingerprint.value = "";
        task.value = null;
        activeDeploymentKey.value = "";
        activeDeploymentSignature.value = "";
        resolutionConfirmed.value = false;
        credentials.password = "";
        credentials.privateKey = "";
        credentials.passphrase = "";
        form.displayName = peerLabel(peer);
        form.fileRoot = "";
        form.disableFileRoot = false;
        form.confirmHostKey = false;
        form.confirmChangedHostKey = false;
      }
      async function probeHostKey() {
        if (!selected.value) return;
        const peerIdentity = selected.value.identity;
        clearNotice();
        fingerprintLoading.value = true;
        try {
          const result = await api.probeTailscaleSSH(peerIdentity);
          if (disposed || selected.value?.identity !== peerIdentity) return;
          fingerprint.value = result.fingerprint;
        } catch (reason) {
          if (!disposed && selected.value?.identity === peerIdentity)
            error.value = message(reason);
        } finally {
          fingerprintLoading.value = false;
        }
      }
      async function startDeployment() {
        if (disposed || loading.value || !selected.value || !fingerprint.value)
          return;
        clearNotice();
        loading.value = true;
        const auth = form.usePrivateKey
          ? {
              user: credentials.user,
              privateKey: credentials.privateKey,
              passphrase: credentials.passphrase || undefined,
            }
          : {
              user: credentials.user,
              password: credentials.password,
            };
        const payload = {
          peerIdentity: selected.value.identity,
          displayName: form.displayName,
          fileRoot: form.disableFileRoot
            ? undefined
            : form.fileRoot.trim() || undefined,
          disableFileRoot: form.disableFileRoot,
          coreUrl: form.coreUrl,
          fallbackUrl: form.fallbackUrl || undefined,
          allowFallback: form.allowFallback,
          hostFingerprint: fingerprint.value,
          confirmHostKey: form.confirmHostKey,
          confirmChangedHostKey: form.confirmChangedHostKey,
          credentials: auth,
        };
        const signature = JSON.stringify({
          peerIdentity: payload.peerIdentity,
          displayName: payload.displayName,
          fileRoot: payload.fileRoot,
          disableFileRoot: payload.disableFileRoot,
          coreUrl: payload.coreUrl,
          fallbackUrl: payload.fallbackUrl,
          allowFallback: payload.allowFallback,
          hostFingerprint: payload.hostFingerprint,
          confirmChangedHostKey: payload.confirmChangedHostKey,
          sshUser: auth.user,
          authentication: form.usePrivateKey ? "private_key" : "password",
        });
        if (
          !activeDeploymentKey.value ||
          activeDeploymentSignature.value !== signature
        ) {
          activeDeploymentKey.value = crypto.randomUUID();
          activeDeploymentSignature.value = signature;
        }
        try {
          const accepted = await api.startTailscaleDeployment(
            payload,
            activeDeploymentKey.value,
          );
          if (disposed) return;
          task.value = accepted;
          unresolvedDeployments.value = [
            accepted,
            ...unresolvedDeployments.value.filter(
              (item) => item.taskId !== accepted.taskId,
            ),
          ];
          activeDeploymentKey.value = "";
          activeDeploymentSignature.value = "";
          credentials.password = "";
          credentials.privateKey = "";
          credentials.passphrase = "";
          await pollTask(accepted.taskId);
        } catch (reason) {
          error.value = message(reason);
        } finally {
          loading.value = false;
        }
      }
      async function resolveUnknownDeployment() {
        if (
          !task.value ||
          task.value.status !== "unknown" ||
          !resolutionConfirmed.value
        )
          return;
        clearNotice();
        try {
          task.value = await api.resolveTailscaleDeployment(task.value.taskId, {
            outcome: resolutionOutcome.value,
            observedState: resolutionObservedState.value,
          });
          resolutionConfirmed.value = false;
          await refreshDeploymentTasks();
          if (task.value.status === "succeeded") await discover();
        } catch (reason) {
          error.value = message(reason);
        }
      }
      async function pollTask(id: string) {
        for (let attempt = 0; attempt < 360; attempt += 1) {
          await new Promise((resolve) => window.setTimeout(resolve, 1500));
          if (disposed || task.value?.taskId !== id) return;
          try {
            const current = await api.tailscaleDeployment(id);
            if (disposed || task.value?.taskId !== id) return;
            task.value = current;
          } catch (reason) {
            error.value = t("m.2eddbfa49d23", {
              "0": message(reason),
            });
            return;
          }
          if (taskFinished.value) {
            await refreshDeploymentTasks();
            if (task.value?.status === "succeeded") {
              const successMessage = task.value.message || t("m.37d815f92ccd");
              await discover();
              if (!error.value) notice.value = successMessage;
            } else if (task.value?.status === "unknown") {
              error.value = task.value.message || t("m.5ef75da06b52");
            } else {
              error.value = task.value?.message || t("m.b6b1127c181b");
            }
            return;
          }
        }
        error.value = t("m.e2de9b4ce02d");
      }
      function message(reason: unknown): string {
        return reason instanceof Error && reason.message
          ? reason.message
          : t("m.a8953ce415a5");
      }
      function shellQuote(value: string): string {
        return `'${value.replaceAll("'", "'\\''")}'`;
      }
      afterMount(() => {
        void discover();
        void refreshDeploymentTasks();
      });
      beforeDispose(() => {
        disposed = true;
        credentials.password =
          credentials.privateKey =
          credentials.passphrase =
            "";
      });
      return () => (
        <>
          <main
            data-testid="tailscale-discovery"
            className={__classes("tailscale-page workspace-page")}
            data-v-20c80a5a=""
          >
            <header
              className={__classes("discovery-heading")}
              data-v-20c80a5a=""
            >
              <div data-v-20c80a5a="">
                <span className={__classes("eyebrow")} data-v-20c80a5a="">
                  {t("label.7aaef7e44874")}
                </span>
                <h1 data-v-20c80a5a="">{t("m.b5af65d9acc5")}</h1>
                <p data-v-20c80a5a="">{t("m.76702efc45e4")}</p>
              </div>
              <button
                type="button"
                disabled={discovering.value as any}
                className={__classes("secondary-button")}
                onClick={(event: any) => {
                  __call(discover, event);
                }}
                data-v-20c80a5a=""
              >
                {discovering.value ? t("m.86b6d0d63062") : t("m.778b04988d4f")}
              </button>
            </header>
            {error.value ? (
              <div
                role="alert"
                className={__classes("alert alert-error")}
                data-v-20c80a5a=""
              >
                {display(error.value)}
              </div>
            ) : null}
            {notice.value ? (
              <div
                role="status"
                className={__classes("alert alert-success")}
                data-v-20c80a5a=""
              >
                {display(notice.value)}
              </div>
            ) : null}
            {unresolvedDeployments.value.length ? (
              <section
                aria-labelledby="unresolved-deployments-title"
                data-testid="unresolved-deployments"
                className={__classes("discovery-card")}
                data-v-20c80a5a=""
              >
                <div
                  className={__classes("discovery-section-heading")}
                  data-v-20c80a5a=""
                >
                  <div data-v-20c80a5a="">
                    <h2 id="unresolved-deployments-title" data-v-20c80a5a="">
                      {t("m.756060e81582")}
                    </h2>
                    <p data-v-20c80a5a="">{t("m.cf0831bd17a7")}</p>
                  </div>
                  <button
                    type="button"
                    className={__classes("quiet-button")}
                    onClick={(event: any) => {
                      __call(refreshDeploymentTasks, event);
                    }}
                    data-v-20c80a5a=""
                  >
                    {t("m.e4c9549fe2f4")}
                  </button>
                </div>
                <div className={__classes("managed-list")} data-v-20c80a5a="">
                  <Fragment>
                    {__each(unresolvedDeployments.value, (deployment) => (
                      <article
                        key={deployment.taskId as any}
                        data-v-20c80a5a=""
                      >
                        <strong data-v-20c80a5a="">
                          {deployment.peerName || deployment.peerIdentity}
                        </strong>
                        <span data-v-20c80a5a="">
                          {deployment.phase}
                          {" · "}
                          {deployment.status}
                        </span>
                        <code data-v-20c80a5a="">{deployment.taskId}</code>
                        <button
                          type="button"
                          className={__classes("secondary-button")}
                          onClick={(event: any) => {
                            inspectDeploymentTask(deployment);
                          }}
                          data-v-20c80a5a=""
                        >
                          {deployment.status === "unknown"
                            ? t("m.196d981f3b71")
                            : t("m.36550b9c5748")}
                        </button>
                      </article>
                    ))}
                  </Fragment>
                </div>
              </section>
            ) : null}
            <section
              aria-labelledby="candidates-title"
              className={__classes("discovery-card")}
              data-v-20c80a5a=""
            >
              <div
                className={__classes("discovery-section-heading")}
                data-v-20c80a5a=""
              >
                <div data-v-20c80a5a="">
                  <h2 id="candidates-title" data-v-20c80a5a="">
                    {t("m.4bd7d4a845a6")}
                  </h2>
                  <p data-v-20c80a5a="">{t("m.8cb6ae80b062")}</p>
                </div>
                <span className={__classes("count-pill")} data-v-20c80a5a="">
                  {candidates.value.length}
                </span>
              </div>
              {discovering.value ? (
                <div
                  role="status"
                  className={__classes("empty-state")}
                  data-v-20c80a5a=""
                >
                  {t("m.41ac7e25e811")}
                </div>
              ) : candidates.value.length ? (
                <div className={__classes("peer-grid")} data-v-20c80a5a="">
                  <Fragment>
                    {__each(candidates.value, (peer) => (
                      <article
                        key={peer.identity as any}
                        data-identity={peer.identity as any}
                        className={__classes("peer-card")}
                        data-v-20c80a5a=""
                      >
                        <div
                          className={__classes("peer-main")}
                          data-v-20c80a5a=""
                        >
                          <span
                            aria-hidden="true"
                            className={__classes("peer-icon")}
                            data-v-20c80a5a=""
                          >
                            {"⌘"}
                          </span>
                          <div data-v-20c80a5a="">
                            <strong data-v-20c80a5a="">
                              {peerLabel(peer)}
                            </strong>
                            <span data-v-20c80a5a="">
                              {peer.os}
                              {" · "}
                              {peer.ips.join(" · ")}
                            </span>
                          </div>
                          <span
                            data-online={peer.online as any}
                            className={__classes("peer-state")}
                            data-v-20c80a5a=""
                          >
                            {stateLabel(peer)}
                          </span>
                        </div>
                        <small
                          className={__classes("peer-id")}
                          data-v-20c80a5a=""
                        >
                          {t("m.314c00531b6b")}
                          {peer.identity}
                        </small>
                        <button
                          type="button"
                          disabled={!peer.online as any}
                          className={__classes("secondary-button")}
                          onClick={(event: any) => {
                            choose(peer);
                          }}
                          data-v-20c80a5a=""
                        >
                          {peer.online
                            ? t("m.633ad558df34")
                            : t("m.badb7a4e8552")}
                        </button>
                      </article>
                    ))}
                  </Fragment>
                </div>
              ) : !error.value && !discovering.value ? (
                <div className={__classes("empty-state")} data-v-20c80a5a="">
                  {t("m.91c31647219d")}
                </div>
              ) : null}
            </section>
            {managed.value.length ? (
              <section
                aria-labelledby="managed-title"
                className={__classes("discovery-card")}
                data-v-20c80a5a=""
              >
                <div
                  className={__classes("discovery-section-heading")}
                  data-v-20c80a5a=""
                >
                  <div data-v-20c80a5a="">
                    <h2 id="managed-title" data-v-20c80a5a="">
                      {t("m.032d0a11f835")}
                    </h2>
                    <p data-v-20c80a5a="">{t("m.f06dc19bae6e")}</p>
                  </div>
                  <span className={__classes("count-pill")} data-v-20c80a5a="">
                    {managed.value.length}
                  </span>
                </div>
                <div className={__classes("managed-list")} data-v-20c80a5a="">
                  <Fragment>
                    {__each(managed.value, (peer) => (
                      <article key={peer.identity as any} data-v-20c80a5a="">
                        <strong data-v-20c80a5a="">{peerLabel(peer)}</strong>
                        <span data-v-20c80a5a="">{peer.ips.join(" · ")}</span>
                        <code data-v-20c80a5a="">{peer.nodeId}</code>
                        <span
                          data-online={peer.online as any}
                          className={__classes("peer-state")}
                          data-v-20c80a5a=""
                        >
                          {peer.online
                            ? t("m.af9306d8cc6d")
                            : t("m.b4ba6377da21")}
                        </span>
                      </article>
                    ))}
                  </Fragment>
                </div>
              </section>
            ) : null}
            {unsupported.value.length ? (
              <section
                aria-labelledby="unsupported-title"
                className={__classes("discovery-card muted-card")}
                data-v-20c80a5a=""
              >
                <div
                  className={__classes("discovery-section-heading")}
                  data-v-20c80a5a=""
                >
                  <div data-v-20c80a5a="">
                    <h2 id="unsupported-title" data-v-20c80a5a="">
                      {t("m.a0eefe1a80e8")}
                    </h2>
                    <p data-v-20c80a5a="">{t("m.bf599a08f93f")}</p>
                  </div>
                  <span className={__classes("count-pill")} data-v-20c80a5a="">
                    {unsupported.value.length}
                  </span>
                </div>
                <ul
                  className={__classes("unsupported-list")}
                  data-v-20c80a5a=""
                >
                  <Fragment>
                    {__each(unsupported.value, (peer) => (
                      <li key={peer.identity as any} data-v-20c80a5a="">
                        <strong data-v-20c80a5a="">{peerLabel(peer)}</strong>
                        <span data-v-20c80a5a="">
                          {peer.os || t("m.17cd83b14b04")}
                          {" · "}
                          {peer.ips.join(" · ")}
                        </span>
                      </li>
                    ))}
                  </Fragment>
                </ul>
              </section>
            ) : null}
            {selected.value ? (
              <section
                aria-labelledby="deploy-title"
                className={__classes("discovery-card deploy-card")}
                data-v-20c80a5a=""
              >
                <div
                  className={__classes("discovery-section-heading")}
                  data-v-20c80a5a=""
                >
                  <div data-v-20c80a5a="">
                    <span className={__classes("eyebrow")} data-v-20c80a5a="">
                      {selected.value.name || selected.value.identity}
                    </span>
                    <h2 id="deploy-title" data-v-20c80a5a="">
                      {t("m.3421f410ce27")}
                    </h2>
                    <p data-v-20c80a5a="">{t("m.b5833cce47f3")}</p>
                  </div>
                  <button
                    type="button"
                    className={__classes("quiet-button")}
                    onClick={(event: any) => {
                      selected.value = null;
                    }}
                    data-v-20c80a5a=""
                  >
                    {t("m.3fd47edce45b")}
                  </button>
                </div>
                <div className={__classes("deploy-columns")} data-v-20c80a5a="">
                  <div className={__classes("deploy-form")} data-v-20c80a5a="">
                    <label className={__classes("field")} data-v-20c80a5a="">
                      <span data-v-20c80a5a="">{t("m.e1290ec94f8d")}</span>
                      <input
                        maxLength={80}
                        autoComplete="off"
                        {...__input(
                          form.displayName,
                          (__nextValue: any) => {
                            form.displayName = __nextValue;
                          },
                          "",
                          undefined,
                          [],
                        )}
                        data-v-20c80a5a=""
                      ></input>
                    </label>
                    <label className={__classes("field")} data-v-20c80a5a="">
                      <span data-v-20c80a5a="">{t("m.d62df2018d54")}</span>
                      <input
                        disabled={form.disableFileRoot as any}
                        autoComplete="off"
                        placeholder="/srv/nodedance-files"
                        {...__input(
                          form.fileRoot,
                          (__nextValue: any) => {
                            form.fileRoot = __nextValue;
                          },
                          "",
                          undefined,
                          [],
                        )}
                        data-v-20c80a5a=""
                      ></input>
                      <small
                        className={__classes("field-hint")}
                        data-v-20c80a5a=""
                      >
                        {t("m.bb7758c4aae9")}
                      </small>
                    </label>
                    <label
                      className={__classes("check-row")}
                      data-v-20c80a5a=""
                    >
                      <input
                        type="checkbox"
                        {...__input(
                          form.disableFileRoot,
                          (__nextValue: any) => {
                            form.disableFileRoot = __nextValue;
                          },
                          "checkbox",
                          undefined,
                          [],
                        )}
                        data-v-20c80a5a=""
                      ></input>
                      <span data-v-20c80a5a="">{t("m.63805b68bc8a")}</span>
                    </label>
                    <label className={__classes("field")} data-v-20c80a5a="">
                      <span data-v-20c80a5a="">{t("m.8f7fd0e28ab3")}</span>
                      <input
                        inputMode="url"
                        placeholder="https://panel.example.ts.net"
                        {...__input(
                          form.coreUrl,
                          (__nextValue: any) => {
                            form.coreUrl = __nextValue;
                          },
                          "",
                          undefined,
                          [],
                        )}
                        data-v-20c80a5a=""
                      ></input>
                      <small
                        className={__classes("field-hint")}
                        data-v-20c80a5a=""
                      >
                        {t("m.6b6c846170b3")}
                      </small>
                    </label>
                    <label className={__classes("field")} data-v-20c80a5a="">
                      <span data-v-20c80a5a="">{t("m.9ca13b13c082")}</span>
                      <input
                        inputMode="url"
                        placeholder="https://backup.example.net"
                        {...__input(
                          form.fallbackUrl,
                          (__nextValue: any) => {
                            form.fallbackUrl = __nextValue;
                          },
                          "",
                          undefined,
                          [],
                        )}
                        data-v-20c80a5a=""
                      ></input>
                    </label>
                    <label
                      className={__classes("check-row")}
                      data-v-20c80a5a=""
                    >
                      <input
                        type="checkbox"
                        {...__input(
                          form.allowFallback,
                          (__nextValue: any) => {
                            form.allowFallback = __nextValue;
                          },
                          "checkbox",
                          undefined,
                          [],
                        )}
                        data-v-20c80a5a=""
                      ></input>
                      <span data-v-20c80a5a="">{t("m.78c672bad4da")}</span>
                    </label>
                    <p className={__classes("field-hint")} data-v-20c80a5a="">
                      {t("m.29319276203d")}
                    </p>
                    <label className={__classes("field")} data-v-20c80a5a="">
                      <span data-v-20c80a5a="">{t("m.4249329ab914")}</span>
                      <input
                        autoComplete="username"
                        {...__input(
                          credentials.user,
                          (__nextValue: any) => {
                            credentials.user = __nextValue;
                          },
                          "",
                          undefined,
                          [],
                        )}
                        data-v-20c80a5a=""
                      ></input>
                    </label>
                    <div
                      role="group"
                      aria-label={t("m.18d16b1edffd") as any}
                      className={__classes("auth-mode")}
                      data-v-20c80a5a=""
                    >
                      <button
                        type="button"
                        aria-pressed={!form.usePrivateKey as any}
                        onClick={(event: any) => {
                          form.usePrivateKey = false;
                        }}
                        data-v-20c80a5a=""
                      >
                        {t("m.c9d0cf7bf3ee")}
                      </button>
                      <button
                        type="button"
                        aria-pressed={form.usePrivateKey as any}
                        onClick={(event: any) => {
                          form.usePrivateKey = true;
                        }}
                        data-v-20c80a5a=""
                      >
                        {t("m.3ffe935f9d0f")}
                      </button>
                    </div>
                    {!form.usePrivateKey ? (
                      <label className={__classes("field")} data-v-20c80a5a="">
                        <span data-v-20c80a5a="">{t("m.6f0890a1b0b0")}</span>
                        <input
                          type="password"
                          autoComplete="new-password"
                          {...__input(
                            credentials.password,
                            (__nextValue: any) => {
                              credentials.password = __nextValue;
                            },
                            "password",
                            undefined,
                            [],
                          )}
                          data-v-20c80a5a=""
                        ></input>
                        <small
                          className={__classes("field-hint")}
                          data-v-20c80a5a=""
                        >
                          {t("m.fe4b4ce6b8aa")}
                        </small>
                      </label>
                    ) : (
                      <Fragment>
                        <label
                          className={__classes("field")}
                          data-v-20c80a5a=""
                        >
                          <span data-v-20c80a5a="">{t("m.2637e8166d92")}</span>
                          <textarea
                            rows={6}
                            spellCheck="false"
                            autoComplete="off"
                            {...__input(
                              credentials.privateKey,
                              (__nextValue: any) => {
                                credentials.privateKey = __nextValue;
                              },
                              "",
                              undefined,
                              [],
                            )}
                            data-v-20c80a5a=""
                          ></textarea>
                        </label>
                        <label
                          className={__classes("field")}
                          data-v-20c80a5a=""
                        >
                          <span data-v-20c80a5a="">{t("m.d2f245e82fd3")}</span>
                          <input
                            type="password"
                            autoComplete="new-password"
                            {...__input(
                              credentials.passphrase,
                              (__nextValue: any) => {
                                credentials.passphrase = __nextValue;
                              },
                              "password",
                              undefined,
                              [],
                            )}
                            data-v-20c80a5a=""
                          ></input>
                        </label>
                      </Fragment>
                    )}
                  </div>
                  <div
                    className={__classes("hostkey-panel")}
                    data-v-20c80a5a=""
                  >
                    <h3 data-v-20c80a5a="">{t("m.00487aceb17d")}</h3>
                    <p data-v-20c80a5a="">{t("m.1f9b1895427a")}</p>
                    <button
                      type="button"
                      disabled={fingerprintLoading.value as any}
                      className={__classes("secondary-button")}
                      onClick={(event: any) => {
                        __call(probeHostKey, event);
                      }}
                      data-v-20c80a5a=""
                    >
                      {fingerprintLoading.value
                        ? t("m.db728a839413")
                        : t("m.c2df7b3b46e0")}
                    </button>
                    {fingerprint.value ? (
                      <code
                        data-testid="ssh-fingerprint"
                        className={__classes("fingerprint")}
                        data-v-20c80a5a=""
                      >
                        {fingerprint.value}
                      </code>
                    ) : null}
                    {fingerprint.value ? (
                      <label
                        className={__classes("check-row")}
                        data-v-20c80a5a=""
                      >
                        <input
                          type="checkbox"
                          {...__input(
                            form.confirmHostKey,
                            (__nextValue: any) => {
                              form.confirmHostKey = __nextValue;
                            },
                            "checkbox",
                            undefined,
                            [],
                          )}
                          data-v-20c80a5a=""
                        ></input>
                        <span data-v-20c80a5a="">{t("m.053515f793f4")}</span>
                      </label>
                    ) : null}
                    {fingerprint.value ? (
                      <label
                        className={__classes("check-row")}
                        data-v-20c80a5a=""
                      >
                        <input
                          type="checkbox"
                          {...__input(
                            form.confirmChangedHostKey,
                            (__nextValue: any) => {
                              form.confirmChangedHostKey = __nextValue;
                            },
                            "checkbox",
                            undefined,
                            [],
                          )}
                          data-v-20c80a5a=""
                        ></input>
                        <span data-v-20c80a5a="">{t("m.d643c97eee50")}</span>
                      </label>
                    ) : null}
                    <button
                      type="button"
                      disabled={
                        loading.value ||
                        !fingerprint.value ||
                        !form.confirmHostKey ||
                        !credentials.user ||
                        (!form.displayName as any)
                      }
                      className={__classes("primary-button full-button")}
                      onClick={(event: any) => {
                        __call(startDeployment, event);
                      }}
                      data-v-20c80a5a=""
                    >
                      {loading.value
                        ? t("m.96461d04f477")
                        : t("m.3795cc8670b9")}
                    </button>
                  </div>
                </div>
                <aside
                  aria-label={t("m.7fe4082935b3") as any}
                  className={__classes("ssh-rescue")}
                  data-v-20c80a5a=""
                >
                  <div data-v-20c80a5a="">
                    <h3 data-v-20c80a5a="">{t("m.6e9f9850c332")}</h3>
                    <p data-v-20c80a5a="">{t("m.4771d6286323")}</p>
                  </div>
                  <div
                    className={__classes("rescue-addresses")}
                    data-v-20c80a5a=""
                  >
                    <span data-v-20c80a5a="">{t("m.25e4ddf9ec12")}</span>
                    <code data-v-20c80a5a="">
                      {selected.value.ips.join(" · ") || t("m.423dec214508")}
                    </code>
                  </div>
                  {selected.value.ips.length ? (
                    <pre data-v-20c80a5a="">
                      <code data-v-20c80a5a="">{rescueCommand.value}</code>
                    </pre>
                  ) : null}
                  <p className={__classes("field-hint")} data-v-20c80a5a="">
                    {t("m.6392f921944a")}
                    <code data-v-20c80a5a="">
                      {"systemctl status nodedance-agent"}
                    </code>
                    {t("m.5c05a073a592")}
                  </p>
                </aside>
              </section>
            ) : null}
            {task.value ? (
              <section
                aria-labelledby="deployment-task-title"
                data-testid="deployment-task-section"
                className={__classes("discovery-card")}
                data-v-20c80a5a=""
              >
                <div
                  className={__classes("discovery-section-heading")}
                  data-v-20c80a5a=""
                >
                  <div data-v-20c80a5a="">
                    <h2 id="deployment-task-title" data-v-20c80a5a="">
                      {t("m.9343a9c6612f")}
                    </h2>
                    <p data-v-20c80a5a="">{t("m.6094de4eb887")}</p>
                  </div>
                  <button
                    type="button"
                    className={__classes("quiet-button")}
                    onClick={(event: any) => {
                      task.value = null;
                    }}
                    data-v-20c80a5a=""
                  >
                    {t("m.3fd47edce45b")}
                  </button>
                </div>
                <div
                  data-testid="deployment-task"
                  data-status={task.value.status as any}
                  role="status"
                  className={__classes("task-status")}
                  data-v-20c80a5a=""
                >
                  <strong data-v-20c80a5a="">
                    {task.value.phase}
                    {" · "}
                    {task.value.status}
                  </strong>
                  <span data-v-20c80a5a="">{display(task.value.message)}</span>
                  {task.value.nodeId ? (
                    <small data-v-20c80a5a="">
                      {"Node ID："}
                      {task.value.nodeId}
                    </small>
                  ) : null}
                </div>
                {task.value.status === "unknown" ? (
                  <div
                    data-testid="deployment-review"
                    className={__classes("task-status task-review")}
                    data-v-20c80a5a=""
                  >
                    <strong data-v-20c80a5a="">{t("m.278a2d42ba92")}</strong>
                    <p data-v-20c80a5a="">{t("m.e972e33c6e32")}</p>
                    <label className={__classes("field")} data-v-20c80a5a="">
                      <span data-v-20c80a5a="">{t("m.6e3bdd16a1ec")}</span>
                      <UiSelect
                        value={resolutionOutcome.value as any}
                        onChange={(event: any) => {
                          __call(changeResolutionOutcome, event);
                        }}
                        data-v-20c80a5a=""
                      >
                        <option value="succeeded" data-v-20c80a5a="">
                          {t("m.18786701de8f")}
                        </option>
                        <option value="failed" data-v-20c80a5a="">
                          {t("m.981ea9952f00")}
                        </option>
                      </UiSelect>
                    </label>
                    {resolutionOutcome.value === "succeeded" ? (
                      <label className={__classes("field")} data-v-20c80a5a="">
                        <span data-v-20c80a5a="">{t("m.e0c379ab2eb0")}</span>
                        <UiSelect
                          value={resolutionObservedState.value as any}
                          onChange={(event: any) => {
                            __call(changeResolutionObservedState, event);
                          }}
                          data-v-20c80a5a=""
                        >
                          <option value="connected" data-v-20c80a5a="">
                            {t("m.83af47139246")}
                          </option>
                          <option value="unavailable" data-v-20c80a5a="">
                            {t("m.d3e431f9d10b")}
                          </option>
                          <option value="absent" data-v-20c80a5a="">
                            {t("m.8c4ecd0b9c16")}
                          </option>
                        </UiSelect>
                      </label>
                    ) : null}
                    <label
                      className={__classes("check-row")}
                      data-v-20c80a5a=""
                    >
                      <input
                        type="checkbox"
                        {...__input(
                          resolutionConfirmed.value,
                          (__nextValue: any) => {
                            resolutionConfirmed.value = __nextValue;
                          },
                          "checkbox",
                          undefined,
                          [],
                        )}
                        data-v-20c80a5a=""
                      ></input>
                      <span data-v-20c80a5a="">{t("m.64aa1e37e12e")}</span>
                    </label>
                    <button
                      type="button"
                      disabled={!resolutionConfirmed.value as any}
                      className={__classes("secondary-button")}
                      onClick={(event: any) => {
                        __call(resolveUnknownDeployment, event);
                      }}
                      data-v-20c80a5a=""
                    >
                      {t("m.b8dead662e24")}
                    </button>
                  </div>
                ) : null}
              </section>
            ) : null}
            <AgentEnrollment
              compact={true}
              data-v-20c80a5a=""
            ></AgentEnrollment>
            <footer
              className={__classes("discovery-footer")}
              data-v-20c80a5a=""
            >
              <span data-v-20c80a5a="">{t("m.b4e9ecd50535")}</span>
              <button
                type="button"
                className={__classes("quiet-button")}
                onClick={(event: any) => {
                  emit("back");
                }}
                data-v-20c80a5a=""
              >
                {t("m.e0e6dc51f626")}
              </button>
            </footer>
          </main>
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
