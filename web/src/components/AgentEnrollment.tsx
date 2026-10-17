import { api, operationRequest } from "../api";
import { requestConfirm } from "../dialogs";
import { display, t } from "../i18n";
import { resolveOperation, type BodySchema } from "../operations";
import { afterMount, cell, derived, watchState } from "../state";
import NodeAssetFields from "./NodeAssetFields";

import { __call, __classes, __input, useController } from "../state";
import "./AgentEnrollment.css";
type Props = {
  compact?: boolean;
  nodeId?: string;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function AgentEnrollment(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const asset = cell<Record<string, unknown>>({});
      const assetSchema = cell<BodySchema>();
      const assetReady = cell(false);
      const props = __props;
      const emit = __emit;
      const displayName = cell("");
      const creating = cell(false);
      const error = cell("");
      const notice = cell("");
      const agentServer = cell(window.location.origin);
      const development = window.location.protocol === "http:";
      const agentOrigin = derived(() => {
        try {
          const parsed = new URL(agentServer.value.trim());
          return (parsed.protocol === "https:" ||
            parsed.protocol === "http:") &&
            !parsed.username &&
            !parsed.password &&
            !parsed.search &&
            !parsed.hash &&
            (parsed.pathname === "/" || parsed.pathname === "")
            ? parsed.origin
            : "";
        } catch {
          return "";
        }
      });
      const agentOriginIsSupported = derived(() => agentOrigin.value !== "");
      const agentOriginIsLoopback = derived(() => {
        if (!agentOrigin.value) return false;
        return isLiteralLoopback(new URL(agentOrigin.value).hostname);
      });
      const agentOriginUsesAllowedTransport = derived(
        () =>
          agentOrigin.value.startsWith("https://") ||
          (development &&
            agentOrigin.value.startsWith("http://") &&
            agentOriginIsLoopback.value),
      );
      const installCommand = cell("");
      const dockerInstallCommand = cell("");
      const deploymentMethod = cell<"native" | "docker">("native");
      afterMount(async () => {
        try {
          const { operation } = await resolveOperation("agents.enrollment");
          const value =
            operation.bodySchema?.properties?.deploymentMethod?.default;
          if (value === "native" || value === "docker")
            deploymentMethod.value = value;
          assetSchema.value = operation.bodySchema?.properties?.asset;
          asset.value = props.nodeId
            ? await operationRequest<Record<string, unknown>>("nodes.asset", {
                params: {
                  nodeId: props.nodeId,
                },
              })
            : Object.fromEntries(
                Object.entries(assetSchema.value?.properties ?? {}).map(
                  ([key, field]) => [
                    key,
                    field.default ??
                      (field.type === "integer" || field.type === "number"
                        ? 0
                        : null),
                  ],
                ),
              );
          if (!props.nodeId) asset.value.billingCycleUnit = "month";
          assetReady.value = true;
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : String(reason);
        }
      });
      const selectedInstallCommand = derived(() =>
        deploymentMethod.value === "docker"
          ? dockerInstallCommand.value
          : installCommand.value,
      );
      let requestEpoch = 0;
      watchState(
        [displayName, agentServer, asset],
        () => {
          requestEpoch++;
          installCommand.value = "";
          dockerInstallCommand.value = "";
          notice.value = "";
        },
        {
          deep: true,
        },
      );
      async function generateEnrollment() {
        error.value = "";
        notice.value = "";
        installCommand.value = "";
        dockerInstallCommand.value = "";
        const name = displayName.value.trim();
        if (
          !agentOriginIsSupported.value ||
          !agentOriginUsesAllowedTransport.value
        ) {
          error.value = t("m.70226817387f");
          return;
        }
        if (!props.nodeId && (!name || /[\r\n\0]/.test(name))) {
          error.value = t("m.bd8975b069f6");
          return;
        }
        const current = requestEpoch;
        creating.value = true;
        try {
          const result = props.nodeId
            ? await api.renewAgentEnrollment(
                props.nodeId,
                agentOrigin.value,
                deploymentMethod.value,
                asset.value,
              )
            : await api.createAgentEnrollment(
                name,
                agentOrigin.value,
                deploymentMethod.value,
                asset.value,
              );
          if (current !== requestEpoch) return;
          installCommand.value = result.nativeInstallCommand;
          dockerInstallCommand.value = result.dockerInstallCommand;
          let copied = false;
          try {
            await navigator.clipboard.writeText(selectedCommand(result));
            copied = true;
          } catch {
            copied = false;
          }
          if (copied) {
            await requestConfirm(t("m.9d2f65ed4a17"));
          } else {
            notice.value = props.nodeId
              ? t("node.enrollmentRenewed")
              : t("m.d04e8d433a67", {
                  "0": result.displayName,
                });
          }
        } catch (reason) {
          if (current === requestEpoch)
            error.value =
              reason instanceof Error && reason.message
                ? reason.message
                : t("m.9ecada108145");
        } finally {
          creating.value = false;
        }
      }
      function selectedCommand(result: {
        nativeInstallCommand: string;
        dockerInstallCommand: string;
      }) {
        return deploymentMethod.value === "docker"
          ? result.dockerInstallCommand
          : result.nativeInstallCommand;
      }
      async function copyText(value: string, label: string) {
        error.value = "";
        notice.value = "";
        try {
          await navigator.clipboard.writeText(value);
          notice.value = t("m.e3852e382b5b", {
            "0": label,
          });
        } catch {
          error.value = t("m.e00feb7956c7");
        }
      }
      function isLiteralLoopback(hostname: string): boolean {
        const host = hostname.replace(/^\[|\]$/g, "");
        if (host === "::1") return true;
        const octets = host.split(".");
        return (
          octets.length === 4 &&
          octets.every(
            (part) => /^\d{1,3}$/.test(part) && Number(part) <= 255,
          ) &&
          Number(octets[0]) === 127
        );
      }
      return () => (
        <>
          <main
            data-testid="agent-enrollment"
            className={__classes(
              __props.compact
                ? "agent-enrollment-compact-page"
                : "agent-enrollment-page workspace-page",
            )}
            data-v-2709b2e0=""
          >
            <section
              className={__classes([
                "agent-enrollment-card",
                {
                  "agent-enrollment-card-compact": __props.compact,
                },
              ])}
              data-v-2709b2e0=""
            >
              <div
                className={__classes("agent-enrollment-form")}
                data-v-2709b2e0=""
              >
                <div
                  className={__classes("agent-enrollment-section-heading")}
                  data-v-2709b2e0=""
                >
                  <div data-v-2709b2e0="">
                    <h2 data-v-2709b2e0="">
                      {__props.compact
                        ? t("m.11e4978edcfe")
                        : t("m.8e80901344c8")}
                    </h2>
                    <p
                      className={__classes("agent-enrollment-usage-hint")}
                      data-v-2709b2e0=""
                    >
                      {t("m.9d6f2b41c5a8")}
                    </p>
                  </div>
                  {!__props.compact ? (
                    <button
                      type="button"
                      className={__classes("quiet-button")}
                      onClick={(event: any) => {
                        emit("back");
                      }}
                      data-v-2709b2e0=""
                    >
                      {t("m.e0e6dc51f626")}
                    </button>
                  ) : null}
                </div>
                <div
                  className={__classes("enrollment-connection")}
                  data-v-2709b2e0=""
                >
                  <div
                    className={__classes("enrollment-identity")}
                    data-v-2709b2e0=""
                  >
                    <label
                      className={__classes("field enrollment-server")}
                      data-v-2709b2e0=""
                    >
                      <span data-v-2709b2e0="">{t("m.b66f55e3dc3d")}</span>
                      <input
                        autoComplete="url"
                        placeholder="https://core.example:8180"
                        aria-label={t("m.b66f55e3dc3d") as any}
                        {...__input(
                          agentServer.value,
                          (__nextValue: any) => {
                            agentServer.value = __nextValue;
                          },
                          "",
                          undefined,
                          [],
                        )}
                        data-v-2709b2e0=""
                      ></input>
                    </label>
                    {!__props.nodeId ? (
                      <label
                        className={__classes("field enrollment-name")}
                        data-v-2709b2e0=""
                      >
                        <span data-v-2709b2e0="">{t("m.a09f47188571")}</span>
                        <input
                          maxLength={80}
                          autoComplete="off"
                          placeholder={t("m.1b086a50d7b3") as any}
                          {...__input(
                            displayName.value,
                            (__nextValue: any) => {
                              displayName.value = __nextValue;
                            },
                            "",
                            undefined,
                            [],
                          )}
                          data-v-2709b2e0=""
                        ></input>
                      </label>
                    ) : null}
                  </div>
                  <fieldset
                    data-testid="agent-deployment-method"
                    className={__classes("deployment-choices")}
                    data-v-2709b2e0=""
                  >
                    <legend data-v-2709b2e0="">
                      {t("agent.deploymentMethod")}
                    </legend>
                    <div data-v-2709b2e0="">
                      <button
                        type="button"
                        aria-pressed={
                          deploymentMethod.value === ("native" as any)
                        }
                        onClick={(event: any) => {
                          deploymentMethod.value = "native";
                        }}
                        data-v-2709b2e0=""
                      >
                        {t("agent.nativeDeployment")}
                      </button>
                      <button
                        type="button"
                        aria-pressed={
                          deploymentMethod.value === ("docker" as any)
                        }
                        onClick={(event: any) => {
                          deploymentMethod.value = "docker";
                        }}
                        data-v-2709b2e0=""
                      >
                        {t("agent.dockerDeployment")}
                      </button>
                    </div>
                    {deploymentMethod.value === "docker" ? (
                      <p className={__classes("field-hint")} data-v-2709b2e0="">
                        {t("agent.dockerRequirements")}
                      </p>
                    ) : null}
                  </fieldset>
                  {__props.nodeId ? (
                    <p className={__classes("field-hint")} data-v-2709b2e0="">
                      {t("node.enrollmentRenewHint")}
                    </p>
                  ) : null}
                </div>
                {assetReady.value ? (
                  <NodeAssetFields
                    showTitle={false as any}
                    values={asset.value as any}
                    schema={assetSchema.value as any}
                    disabled={creating.value as any}
                    className={__classes("enrollment-assets")}
                    data-v-2709b2e0=""
                  ></NodeAssetFields>
                ) : null}
                {!agentOriginIsSupported.value ||
                !agentOriginUsesAllowedTransport.value ? (
                  <p
                    role="alert"
                    className={__classes("alert alert-error")}
                    data-v-2709b2e0=""
                  >
                    {t("m.1d5964dbb8d8")}
                  </p>
                ) : null}
                {error.value ? (
                  <p
                    role="alert"
                    className={__classes("alert alert-error")}
                    data-v-2709b2e0=""
                  >
                    {display(error.value)}
                  </p>
                ) : null}
                {notice.value ? (
                  <p
                    role="status"
                    className={__classes("alert alert-success")}
                    data-v-2709b2e0=""
                  >
                    {display(notice.value)}
                  </p>
                ) : null}
                <div
                  className={__classes("agent-enrollment-actions")}
                  data-v-2709b2e0=""
                >
                  {selectedInstallCommand.value ? (
                    <button
                      type="button"
                      className={__classes("secondary-button")}
                      onClick={(event: any) => {
                        copyText(
                          selectedInstallCommand.value,
                          t("m.bdf7d59b3dc2"),
                        );
                      }}
                      data-v-2709b2e0=""
                    >
                      {t("m.fe95745d1ef4")}
                    </button>
                  ) : null}
                  <button
                    type="button"
                    disabled={
                      !assetReady.value ||
                      creating.value ||
                      (!__props.nodeId && !displayName.value.trim()) ||
                      !agentOriginIsSupported.value ||
                      (!agentOriginUsesAllowedTransport.value as any)
                    }
                    className={__classes("primary-button")}
                    onClick={(event: any) => {
                      __call(generateEnrollment, event);
                    }}
                    data-v-2709b2e0=""
                  >
                    {creating.value
                      ? t("m.a8b8730c9611")
                      : __props.nodeId
                        ? t("node.generateInstall")
                        : t("m.92840286f8cd")}
                  </button>
                </div>
                {selectedInstallCommand.value ? (
                  <p
                    role="status"
                    className={__classes("alert alert-success")}
                    data-v-2709b2e0=""
                  >
                    {t("m.9d2f65ed4a17")}
                  </p>
                ) : null}
              </div>
            </section>
          </main>
        </>
      );
    },
    input,
    ["compact", "nodeId"],
    [],
    {
      compact: false,
    },
    true,
  );
}
