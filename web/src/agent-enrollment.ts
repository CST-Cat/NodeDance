import { api, operationRequest } from "./api";
import { clipboardFeedback } from "./clipboard";
import { t } from "./i18n";
import { resolveOperation, type BodySchema } from "./operations";
import {
  afterMount,
  beforeDispose,
  cell,
  derived,
  observable,
  watchState,
} from "./state";
export interface AgentSetupDraft {
  displayName: string;
  coreUrl: string;
  deploymentMethod: "native" | "docker";
}
export function createAgentSetupDraft(): AgentSetupDraft {
  return observable({
    displayName: "",
    coreUrl: window.location.origin,
    deploymentMethod: "native",
  });
}
export function createEnrollmentController(
  props: { nodeId?: string },
  draft = createAgentSetupDraft(),
  requestScope: () => string = () => "manual",
) {
  let disposed = false;
  const copied = clipboardFeedback();
  const asset = cell<Record<string, unknown>>({});
  const assetSchema = cell<BodySchema>();
  const assetReady = cell(false);
  const displayName = derived({
    get: () => draft.displayName,
    set: (value) => {
      draft.displayName = value;
    },
  });
  const creating = cell(false);
  const error = cell("");
  const agentServer = derived({
    get: () => draft.coreUrl,
    set: (value) => {
      draft.coreUrl = value;
    },
  });
  const installCommand = cell("");
  const dockerInstallCommand = cell("");
  const deploymentMethod = derived<"native" | "docker">({
    get: () => draft.deploymentMethod,
    set: (value) => {
      draft.deploymentMethod = value;
    },
  });
  afterMount(async () => {
    try {
      const { operation } = await resolveOperation("agents.enrollment");
      if (disposed) return;
      const value = operation.bodySchema?.properties?.deploymentMethod?.default;
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
      error.value = reason instanceof Error ? reason.message : String(reason);
    }
  });
  const selectedInstallCommand = derived(() =>
    deploymentMethod.value === "docker"
      ? dockerInstallCommand.value
      : installCommand.value,
  );
  watchState(deploymentMethod, () => copied.reset());
  let requestEpoch = 0;
  beforeDispose(() => {
    disposed = true;
    requestEpoch++;
  });
  watchState(
    [displayName, agentServer, asset, derived(requestScope)],
    () => {
      requestEpoch++;
      copied.reset();
      installCommand.value = "";
      dockerInstallCommand.value = "";
    },
    {
      deep: true,
    },
  );
  async function generateEnrollment() {
    if (disposed || creating.value) return;
    error.value = "";
    installCommand.value = "";
    dockerInstallCommand.value = "";
    const name = displayName.value.trim();
    const current = requestEpoch;
    creating.value = true;
    try {
      const result = props.nodeId
        ? await api.renewAgentEnrollment(
            props.nodeId,
            agentServer.value.trim(),
            deploymentMethod.value,
            asset.value,
          )
        : await api.createAgentEnrollment(
            name,
            agentServer.value.trim(),
            deploymentMethod.value,
            asset.value,
          );
      if (current !== requestEpoch) return;
      installCommand.value = result.nativeInstallCommand;
      dockerInstallCommand.value = result.dockerInstallCommand;
      await copied.copy(selectedCommand(result));
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
  return {
    copied,
    asset,
    assetSchema,
    assetReady,
    displayName,
    creating,
    error,
    agentServer,
    installCommand,
    dockerInstallCommand,
    deploymentMethod,
    selectedInstallCommand,
    generateEnrollment,
  };
}
