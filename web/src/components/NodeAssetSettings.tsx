import { Button } from "./ManagementUI";
import { operationRequest } from "../api";
import { display, t } from "../i18n";
import { exactJSON, resolveOperation, type BodySchema } from "../operations";
import { afterRender, cell, watchState } from "../state";
import NodeAssetFields from "./NodeAssetFields";

import { __call, __classes, useController } from "../state";
import "./NodeAssetSettings.css";
import { SettingsGroup } from "./SettingsGroup";
type Props = {
  nodeId: string;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function NodeAssetSettings(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const asset = cell<Record<string, unknown>>({}),
        schema = cell<BodySchema>(),
        loading = cell(true),
        saving = cell(false),
        error = cell(""),
        saved = cell(false);
      let epoch = 0;
      watchState(
        () => props.nodeId,
        async (nodeId) => {
          const current = ++epoch;
          loading.value = true;
          error.value = "";
          saved.value = false;
          try {
            const [contract, value] = await Promise.all([
              resolveOperation("nodes.save-asset", {
                params: {
                  nodeId,
                },
              }),
              operationRequest<Record<string, unknown>>("nodes.asset", {
                params: {
                  nodeId,
                },
              }),
            ]);
            if (current !== epoch) return;
            schema.value = contract.operation.bodySchema;
            asset.value = value;
          } catch (reason) {
            if (current === epoch)
              error.value = String(
                reason instanceof Error ? reason.message : reason,
              );
          } finally {
            if (current === epoch) loading.value = false;
          }
        },
        {
          immediate: true,
        },
      );
      async function save() {
        const current = epoch,
          nodeId = props.nodeId;
        saving.value = true;
        error.value = "";
        saved.value = false;
        try {
          const value = await operationRequest<Record<string, unknown>>(
            "nodes.save-asset",
            {
              params: {
                nodeId,
              },
            },
            {
              body: exactJSON(asset.value),
            },
          );
          if (current === epoch) {
            asset.value = value;
            await afterRender();
            if (current === epoch) saved.value = true;
          }
        } catch (reason) {
          if (current === epoch)
            error.value = String(
              reason instanceof Error ? reason.message : reason,
            );
        } finally {
          saving.value = false;
        }
      }
      watchState(
        asset,
        () => {
          saved.value = false;
        },
        {
          deep: true,
        },
      );
      return () => (
        <>
          <form
            data-testid="node-asset-settings"
            className={__classes("asset-settings")}
            onSubmit={(event: any) => {
              event.preventDefault();
              __call(save, event);
            }}
            data-v-c1f6db81=""
          >
            {!loading.value && schema.value ? (
              <SettingsGroup><NodeAssetFields
                grouped
                showHints={false}
                values={asset.value as any}
                schema={schema.value as any}
                disabled={saving.value as any}
                data-v-c1f6db81=""
              ></NodeAssetFields></SettingsGroup>
            ) : null}
            {loading.value ? (
              <p data-v-c1f6db81="">{t("asset.loading")}</p>
            ) : null}
            {error.value ? (
              <p
                role="alert"
                className={__classes("alert alert-error")}
                data-v-c1f6db81=""
              >
                {display(error.value)}
              </p>
            ) : null}
            {saved.value ? (
              <p
                role="status"
                className={__classes("alert alert-success")}
                data-v-c1f6db81=""
              >
                {t("asset.saved")}
              </p>
            ) : null}
            {!loading.value && schema.value ? (
              <Button variant="primary"
                type="submit"
                disabled={saving.value as any}
                data-testid="asset-save"

                data-v-c1f6db81=""
              >
                {t(saving.value ? "asset.saving" : "asset.save")}
              </Button>
            ) : null}
          </form>
        </>
      );
    },
    input,
    ["nodeId"],
    [],
    {},
    true,
  );
}
