import { Button } from "./ManagementUI";
import { ExpandedTextarea } from "./ExpandedTextarea";
import UiTaskProgress from "./UiTaskProgress";
import UiLoadState from "./UiLoadState";
import UiPath from "./UiPath";
import UiDialog from "./UiDialog";
import UiConfigDiff from "./UiConfigDiff";
import type { ContainerTask } from "../api";
import { composeApi, type ComposeProject } from "../compose-api";
import { display, t } from "../i18n";
import {
  afterMount,
  afterRender,
  beforeDispose,
  cell,
  derived,
} from "../state";

import { Fragment } from "react";
import { __call, __classes, __each, __input, useController } from "../state";
import "./ComposeEditor.css";
import UiSelect from "./UiSelect";
type Props = {
  nodeId: string;
  project: ComposeProject;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function ComposeEditor(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const reviewing = cell(false);
      const reviewButton = cell<HTMLButtonElement>();
      function closeReview() {
        reviewing.value = false;
        void afterRender(() =>
          reviewButton.value?.focus({ preventScroll: true }),
        );
      }
      const fileIndex = cell(0);
      const content = cell("");
      const savedContent = cell("");
      const baseSHA256 = cell("");
      const loading = cell(false);
      const saving = cell(false);
      const error = cell("");
      const message = cell("");
      const task = cell<ContainerTask>();
      let pollTimer: ReturnType<typeof setTimeout> | undefined;
      let generation = 0;
      const dirty = derived(() => content.value !== savedContent.value);
      const currentFile = derived(
        () =>
          props.project.ref.configFiles[fileIndex.value] ??
          t("m.8b98267611a6", {
            "0": fileIndex.value + 1,
          }),
      );
      async function loadConfig() {
        reviewing.value = false;
        const requestGeneration = ++generation;
        loading.value = true;
        error.value = "";
        try {
          const result = await composeApi.config(
            props.nodeId,
            props.project.ref.key,
            fileIndex.value,
          );
          if (generation !== requestGeneration) return;
          content.value = result.content;
          savedContent.value = result.content;
          baseSHA256.value = result.sha256;
          message.value = t("m.8d2e18f84c7e");
        } catch (reason) {
          if (generation === requestGeneration)
            error.value =
              reason instanceof Error ? reason.message : t("m.3957ffb3e358");
        } finally {
          if (generation === requestGeneration) loading.value = false;
        }
      }
      function schedulePoll(taskId: string, requestGeneration: number) {
        pollTimer = setTimeout(async () => {
          try {
            const current = await composeApi.task(props.nodeId, taskId);
            if (requestGeneration !== generation) return;
            task.value = current;
            if (current.status === "succeeded") {
              await loadConfig();
              message.value = t("m.334ef965226e");
              saving.value = false;
              return;
            }
            if (
              current.status === "failed" ||
              current.status === "unknown" ||
              current.status === "timed_out" ||
              current.status === "canceled"
            ) {
              error.value =
                current.status === "unknown"
                  ? t("m.e90270efd29a")
                  : t("m.2a044add4de5");
              saving.value = false;
              return;
            }
            schedulePoll(taskId, requestGeneration);
          } catch (reason) {
            if (requestGeneration !== generation) return;
            error.value =
              reason instanceof Error ? reason.message : t("m.f5ab77abe914");
            saving.value = false;
          }
        }, 750);
      }
      async function save() {
        if (loading.value || !dirty.value || saving.value) return;
        saving.value = true;
        error.value = "";
        message.value = "";
        const requestGeneration = ++generation;
        try {
          const accepted = await composeApi.createTask(
            props.nodeId,
            props.project.ref.key,
            {
              action: "config_save",
              fileIndex: fileIndex.value,
              content: content.value,
              baseSha256: baseSHA256.value,
            },
          );
          task.value = await composeApi.task(props.nodeId, accepted.taskId);
          if (task.value.status === "succeeded") {
            await loadConfig();
            message.value = t("m.334ef965226e");
            saving.value = false;
            return;
          }
          if (
            task.value.status === "failed" ||
            task.value.status === "unknown"
          ) {
            error.value =
              task.value.status === "unknown"
                ? t("m.e90270efd29a")
                : t("m.2a044add4de5");
            saving.value = false;
            return;
          }
          schedulePoll(accepted.taskId, requestGeneration);
        } catch (reason) {
          error.value =
            reason instanceof Error ? reason.message : t("m.3d63ac5acf31");
          saving.value = false;
        }
      }
      function restore() {
        content.value = savedContent.value;
        error.value = "";
        message.value = t("m.5440ce29fbc9");
      }
      function onContentChange() {
        error.value = "";
        message.value = "";
      }
      function onFileChange() {
        content.value =
          savedContent.value =
          baseSHA256.value =
          message.value =
            "";
        task.value = undefined;
        void loadConfig();
      }
      afterMount(() => void loadConfig());
      beforeDispose(() => {
        generation++;
        if (pollTimer) clearTimeout(pollTimer);
      });
      return () => (
        <>
          <section
            aria-label={t("m.68caef829d91") as any}
            data-testid="compose-editor"
            className={__classes("compose-editor ui-load-region")}
            data-v-306714f3=""
          >
            <header
              className={__classes(
                "compose-editor-heading ui-responsive-header",
              )}
              data-v-306714f3=""
            >
              <div data-v-306714f3="">

                <h3 data-v-306714f3="">{t("m.a5de4df1d563")}</h3>
              </div>
              <Button variant="secondary"
                type="button"
                disabled={loading.value || (saving.value as any)}

                onClick={(event: any) => {
                  void loadConfig();
                }}
                data-v-306714f3=""
              >
                {t("m.df7392cc96be")}
              </Button>
            </header>
            <label
              className={__classes("compose-file-picker")}
              data-v-306714f3=""
            >
              {t("m.1d27f02ed278")}
              <UiSelect
                disabled={loading.value || (saving.value as any)}
                onChange={(event: any) => {
                  __call(onFileChange, event);
                }}
                modelValue={fileIndex.value}
                onUpdateModelValue={(__nextValue: any) => {
                  fileIndex.value = __nextValue;
                }}
                modelModifiers={{ number: true }}
                data-v-306714f3=""
              >
                <Fragment>
                  {__each(__props.project.ref.configFiles, (path, index) => (
                    <option
                      key={path as any}
                      value={index as any}
                      data-v-306714f3=""
                    >
                      {path.split("/").at(-1) || path}
                    </option>
                  ))}
                </Fragment>
              </UiSelect>
              <small data-v-306714f3="">
                <UiPath value={currentFile.value} />
              </small>
            </label>
            <p className={__classes("compose-editor-risk")} data-v-306714f3="">
              {t("m.cd673d386eed")}
            </p>
            <ExpandedTextarea
              aria-label={t("m.47a8168380df") as any}
              spellCheck={false}
              disabled={loading.value || (saving.value as any)}
              onInput={(event: any) => {
                __call(onContentChange, event);
              }}
              {...__input(
                content.value,
                (__nextValue: any) => {
                  content.value = __nextValue;
                },
                "",
                undefined,
                [],
              )}
              data-v-306714f3=""
            ></ExpandedTextarea>
            <div
              className={__classes("compose-editor-actions")}
              data-v-306714f3=""
            >
              <Button variant="primary"
                type="button"
                disabled={
                  loading.value || !dirty.value || (saving.value as any)
                }

                onClick={(event: any) => {
                  void save();
                }}
                data-v-306714f3=""
              >
                {saving.value ? t("m.ff509c9ba052") : t("m.6e584e3d5ce6")}
              </Button>
              <Button variant="secondary"
                type="button"
                disabled={!dirty.value || (saving.value as any)}

                onClick={(event: any) => {
                  __call(restore, event);
                }}
                data-v-306714f3=""
              >
                {t("m.131be45b460c")}
              </Button>
              <Button variant="secondary"
                type="button"
                ref={__dom(reviewButton)}

                aria-haspopup="dialog"
                disabled={loading.value || !dirty.value || saving.value}
                onClick={() => {
                  reviewing.value = true;
                }}
              >
                {t("diff.review")}
              </Button>
            </div>
            <UiLoadState
              loading={loading.value}
              ready={!!baseSHA256.value}
              failed={!!error.value}
            />
            {task.value ? (
              <UiTaskProgress
                status={task.value.status}
                phase={task.value.progress.phase}
                completed={task.value.progress.completed}
                total={task.value.progress.total}
              />
            ) : null}
            {message.value ? (
              <p
                role="status"
                className={__classes("compose-editor-message")}
                data-v-306714f3=""
              >
                {display(message.value)}
              </p>
            ) : null}
            {error.value ? (
              <p
                role="alert"
                className={__classes("compose-editor-error")}
                data-v-306714f3=""
              >
                {display(error.value)}
              </p>
            ) : null}
          </section>
          {reviewing.value ? (
            <UiDialog
              open
              title={`${t("diff.review")} · ${currentFile.value}`}
              className="config-diff-dialog"
              onClose={closeReview}
            >
              <UiConfigDiff before={savedContent.value} after={content.value} />
              <footer data-v-b0bbf977="">
                <Button variant="secondary" type="button" onClick={closeReview}>
                  {t("overview.close")}
                </Button>
              </footer>
            </UiDialog>
          ) : null}
        </>
      );
    },
    input,
    ["nodeId", "project"],
    [],
    {},
    true,
  );
}
