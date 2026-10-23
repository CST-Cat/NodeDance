import { createEnrollmentController } from "../agent-enrollment";
import { ManagementPage, Button, Input } from "./ManagementUI";
import UiCopyFeedback from "./UiCopyFeedback";
import { display, t } from "../i18n";
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
      const { copied, asset, assetSchema, assetReady, displayName, creating, error, agentServer, installCommand, dockerInstallCommand, deploymentMethod, selectedInstallCommand, generateEnrollment } = createEnrollmentController(__props);
      const emit = __emit;
      return () => (
        <>
          <ManagementPage
            embedded
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
                    <Button
                      type="button"
                      variant="ghost"
                      className={__classes("quiet-button")}
                      onClick={(event: any) => {
                        emit("back");
                      }}
                      data-v-2709b2e0=""
                    >
                      {t("m.e0e6dc51f626")}
                    </Button>
                  ) : null}
                </div>
                <div
                  className={__classes("enrollment-connection")}
                  data-v-2709b2e0=""
                >
                  <h3>{t("agent.connectionSection")}</h3>
                  <div
                    className={__classes("enrollment-identity")}
                    data-v-2709b2e0=""
                  >
                    <label
                      className={__classes("field enrollment-server")}
                      data-v-2709b2e0=""
                    >
                      <span data-v-2709b2e0="">{t("m.b66f55e3dc3d")}</span>
                      <Input
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
                      ></Input>
                    </label>
                    {!__props.nodeId ? (
                      <label
                        className={__classes("field enrollment-name")}
                        data-v-2709b2e0=""
                      >
                        <span data-v-2709b2e0="">{t("m.a09f47188571")}</span>
                        <Input
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
                        ></Input>
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
                      <Button
                        variant="secondary"
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
                      </Button>
                      <Button
                        variant="secondary"
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
                      </Button>
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
                <section className="enrollment-asset-section">
                  <h3>{t("agent.assetsSection")}</h3>
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
                </section>
                {error.value ? (
                  <p
                    role="alert"
                    className={__classes("alert alert-error")}
                    data-v-2709b2e0=""
                  >
                    {display(error.value)}
                  </p>
                ) : null}

                <div
                  className={__classes("agent-enrollment-actions")}
                  data-v-2709b2e0=""
                >
                  {selectedInstallCommand.value ? (
                    <Button
                      type="button"
                      variant="secondary"
                      className={__classes("secondary-button")}
                      aria-label={t("m.fe95745d1ef4")}
                      onClick={(event: any) => {
                        void copied.copy(selectedInstallCommand.value);
                      }}
                      data-v-2709b2e0=""
                    >
                      <UiCopyFeedback state={copied.state.value}>{t("m.fe95745d1ef4")}</UiCopyFeedback>
                    </Button>
                  ) : null}
                  <Button
                    type="button"
                    disabled={
                      !assetReady.value ||
                      creating.value
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
                  </Button>
                </div>

              </div>
            </section>
          </ManagementPage>
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
