import type { ContainerTask } from "../api";
import { composeApi } from "../compose-api";
import { requestConfirm } from "../dialogs";
import { display, t } from "../i18n";
import {
  __classes,
  __input,
  afterRender,
  beforeDispose,
  cell,
  lazyView,
  useController,
} from "../state";
import "./ComposeCreateForm.css";

const NodeFiles = lazyView(() => import("./NodeFiles"));
type Props = {
  nodeId: string;
  disabled: boolean;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function ComposeCreateForm(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const emit = __emit;
      const name = cell("");
      const workingDirectory = cell("");
      const content = cell("");
      const directoryPickerOpen = cell(false);
      const selectedDirectory = cell("");
      const directoryButton = cell<HTMLButtonElement>();
      async function closeDirectoryPicker(useDirectory = false) {
        if (useDirectory && selectedDirectory.value)
          workingDirectory.value = selectedDirectory.value;
        directoryPickerOpen.value = false;
        selectedDirectory.value = "";
        await afterRender();
        directoryButton.value?.focus();
      }
      const submitting = cell(false);
      const error = cell("");
      const message = cell("");
      const task = cell<ContainerTask>();
      let pollTimer: ReturnType<typeof setTimeout> | undefined;
      let disposed = false;
      function validInput() {
        return Boolean(
          name.value.trim() &&
            workingDirectory.value.trim() &&
            content.value.trim(),
        );
      }
      function taskMessage(value: ContainerTask) {
        if (value.status === "succeeded") return t("m.cffa1e3e2c3b");
        if (value.status === "failed")
          return t("m.8fd2915e75c4", {
            "0": value.result.code || t("m.f50ce21ae910"),
          });
        if (value.status === "unknown" || value.status === "timed_out")
          return t("m.0c4dcb0a3484");
        if (value.status === "canceled") return t("m.12632265c570");
        return t("m.282f0fac39e8", {
          "0": value.status,
        });
      }
      function poll(taskId: string) {
        pollTimer = setTimeout(async () => {
          try {
            const latest = await composeApi.task(props.nodeId, taskId);
            if (disposed) return;
            task.value = latest;
            message.value = taskMessage(latest);
            if (latest.status === "queued" || latest.status === "running") {
              poll(taskId);
              return;
            }
            submitting.value = false;
            if (latest.status === "succeeded") emit("created", latest);
          } catch (reason) {
            if (disposed) return;
            submitting.value = false;
            error.value =
              reason instanceof Error ? reason.message : t("m.bbf9a9b3593c");
          }
        }, 750);
      }
      async function createProject() {
        if (props.disabled || submitting.value || !validInput()) return;
        if (
          !(await requestConfirm(
            t("m.0934bc7652a3", {
              "0": props.nodeId,
              "1": name.value,
            }),
          ))
        )
          return;
        submitting.value = true;
        error.value = "";
        message.value = "";
        task.value = undefined;
        try {
          const accepted = await composeApi.createProject(props.nodeId, {
            name: name.value,
            workingDirectory: workingDirectory.value,
            content: content.value,
          });
          const current = await composeApi.task(props.nodeId, accepted.taskId);
          task.value = current;
          message.value = taskMessage(current);
          if (current.status === "queued" || current.status === "running")
            poll(accepted.taskId);
          else {
            submitting.value = false;
            if (current.status === "succeeded") emit("created", current);
          }
        } catch (reason) {
          submitting.value = false;
          error.value =
            reason instanceof Error ? reason.message : t("m.748066b2afa5");
        }
      }
      beforeDispose(() => {
        disposed = true;
        if (pollTimer) clearTimeout(pollTimer);
      });
      return () => (
        <>
          <section
            aria-label={t("m.2798c23115cf") as any}
            data-testid="compose-create-form"
            className={__classes("compose-create-form")}
            data-v-833010c3=""
          >
            <header data-v-833010c3="">
              <div data-v-833010c3="">
                <span className={__classes("eyebrow")} data-v-833010c3="">
                  {t("label.7212dcc44ffd")}
                </span>
                <h3 data-v-833010c3="">{t("m.2798c23115cf")}</h3>
              </div>
              <span data-v-833010c3="">{t("m.0da9f2878ec4")}</span>
            </header>
            <p className={__classes("compose-create-note")} data-v-833010c3="">
              {t("m.872a816958dc")}
            </p>
            <div
              className={__classes("compose-create-fields")}
              data-v-833010c3=""
            >
              <label data-v-833010c3="">
                {t("m.867698fb7b0c")}
                <input
                  autoComplete="off"
                  maxLength={63}
                  placeholder={t("m.d1bcd8876f63") as any}
                  disabled={submitting.value as any}
                  {...__input(
                    name.value,
                    (__nextValue: any) => {
                      name.value = __nextValue;
                    },
                    "",
                    undefined,
                    ["trim"],
                  )}
                  data-v-833010c3=""
                ></input>
              </label>
              <div
                className={__classes("compose-directory-field")}
                data-v-833010c3=""
              >
                <label data-v-833010c3="">
                  {t("m.e9abe5532d97")}
                  <input
                    data-testid="compose-workdir"
                    autoComplete="off"
                    placeholder="/srv/web-stack"
                    disabled={submitting.value as any}
                    {...__input(
                      workingDirectory.value,
                      (__nextValue: any) => {
                        workingDirectory.value = __nextValue;
                      },
                      "",
                      undefined,
                      ["trim"],
                    )}
                    data-v-833010c3=""
                  ></input>
                </label>
                <button
                  ref={__dom(directoryButton)}
                  type="button"
                  disabled={__props.disabled || (submitting.value as any)}
                  aria-expanded={directoryPickerOpen.value as any}
                  aria-controls="compose-directory-picker"
                  data-testid="compose-directory-open"
                  className={__classes("container-action")}
                  onClick={(event: any) => {
                    selectedDirectory.value = "";
                    directoryPickerOpen.value = true;
                  }}
                  data-v-833010c3=""
                >
                  {t("compose.browseDirectory")}
                </button>
              </div>
            </div>
            {directoryPickerOpen.value ? (
              <section
                id="compose-directory-picker"
                aria-label={t("compose.chooseDirectory") as any}
                data-testid="compose-directory-picker"
                className={__classes("compose-directory-picker")}
                onKeyDown={(event: any) => {
                  event.stopPropagation();
                  void closeDirectoryPicker();
                }}
                data-v-833010c3=""
              >
                <header data-v-833010c3="">
                  <h3 data-v-833010c3="">{t("compose.chooseDirectory")}</h3>
                  <button
                    type="button"
                    className={__classes("container-action")}
                    onClick={(event: any) => {
                      void closeDirectoryPicker();
                    }}
                    data-v-833010c3=""
                  >
                    {t("dialog.cancel")}
                  </button>
                </header>
                <p data-v-833010c3="">{t("compose.directoryHelp")}</p>
                <NodeFiles
                  nodeId={__props.nodeId as any}
                  nodeName={t("compose.chooseDirectory") as any}
                  directoryPicker={true}
                  onDirectoryReady={(event: any) => {
                    selectedDirectory.value = event;
                  }}
                  data-v-833010c3=""
                ></NodeFiles>
                <footer data-v-833010c3="">
                  <span data-v-833010c3="">
                    {selectedDirectory.value || t("guest.loading")}
                  </span>
                  <button
                    type="button"
                    disabled={!selectedDirectory.value as any}
                    data-testid="compose-directory-use"
                    className={__classes("container-action")}
                    onClick={(event: any) => {
                      void closeDirectoryPicker(true);
                    }}
                    data-v-833010c3=""
                  >
                    {t("compose.useDirectory")}
                  </button>
                </footer>
              </section>
            ) : null}
            <label
              className={__classes("compose-create-yaml")}
              data-v-833010c3=""
            >
              {"Compose YAML"}
              <textarea
                spellCheck="false"
                placeholder={
                  "services:\n  app:\n    image: nginx:stable" as any
                }
                disabled={submitting.value as any}
                {...__input(
                  content.value,
                  (__nextValue: any) => {
                    content.value = __nextValue;
                  },
                  "",
                  undefined,
                  [],
                )}
                data-v-833010c3=""
              ></textarea>
            </label>
            <div
              className={__classes("compose-create-actions")}
              data-v-833010c3=""
            >
              <button
                type="button"
                disabled={
                  __props.disabled || submitting.value || (!validInput() as any)
                }
                className={__classes(
                  "container-action container-action-danger",
                )}
                onClick={(event: any) => {
                  void createProject();
                }}
                data-v-833010c3=""
              >
                {submitting.value ? t("m.92199e4ba37e") : t("m.6db055dc4ece")}
              </button>
              {message.value ? (
                <span
                  role="status"
                  data-status={task.value?.status as any}
                  data-v-833010c3=""
                >
                  {display(message.value)}
                </span>
              ) : null}
              {error.value ? (
                <span role="alert" data-v-833010c3="">
                  {display(error.value)}
                </span>
              ) : null}
            </div>
          </section>
        </>
      );
    },
    input,
    ["nodeId", "disabled"],
    [],
    {},
    true,
  );
}
