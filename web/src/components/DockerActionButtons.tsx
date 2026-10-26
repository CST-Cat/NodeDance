import { Button } from "./ManagementUI";
import { t } from "../i18n";
import { derived } from "../state";
export type DockerPrimaryAction =
  | "start"
  | "stop"
  | "restart"
  | "rename"
  | "delete";

import { Fragment } from "react";
import { __classes, __each, useController } from "../state";
import "./DockerActionButtons.css";
type Props = {
  can: (action: DockerPrimaryAction) => boolean;
  renameLabel?: string;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function DockerActionButtons(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const emit = __emit;
      const actions = derived(() => [
        {
          action: "start" as const,
          label: t("m.56410fc65314"),
        },
        {
          action: "stop" as const,
          label: t("m.ca4d973c0b00"),
        },
        {
          action: "restart" as const,
          label: t("m.562822892865"),
        },
        {
          action: "rename" as const,
          label: props.renameLabel || t("m.0d0cbac2eee5"),
        },
        {
          action: "delete" as const,
          label: t("m.2f9daa828907"),
        },
      ]);
      return () => (
        <>
          <div
            className={__classes("docker-primary-actions")}
            data-v-f0dea680=""
          >
            <Fragment>
              {__each(actions.value, ({ action, label }) => (
                <Button variant={action === "delete" ? "danger" : "secondary"}
                  key={action as any}
                  type="button"
                  data-action={action as any}
                  disabled={!__props.can(action) as any}
                  title={
                    action === "rename"
                      ? t("docker.singleOnly")
                      : (t("docker.allApplicable") as any)
                  }

                  onClick={(event: any) => {
                    emit("action", action);
                  }}
                  data-v-f0dea680=""
                >
                  {label}
                </Button>
              ))}
            </Fragment>
          </div>
        </>
      );
    },
    input,
    ["can", "renameLabel"],
    [],
    {},
    true,
  );
}
