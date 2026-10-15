import { activeDialog, cancelDialogs, finishDialog } from "../dialogs";
import { t } from "../i18n";
import { afterRender, beforeDispose, cell, watchState } from "../state";

import { __classes, __input, useController } from "../state";
import "./UiDialog.css";
type Props = {} & { children?: import("react").ReactNode; [key: string]: any };
export default function UiDialog(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const element = cell<HTMLDialogElement>();
      const input = cell<HTMLInputElement>();
      const value = cell("");
      watchState(
        activeDialog,
        async (request) => {
          element.value?.close();
          if (!request) return;
          value.value = request.value;
          await afterRender();
          if (activeDialog.value !== request) return;
          element.value?.showModal();
          if (request.kind === "prompt") {
            input.value?.focus();
            input.value?.select();
          }
        },
        {
          flush: "post",
        },
      );
      function finish(accepted: boolean) {
        element.value?.close();
        finishDialog(
          accepted
            ? activeDialog.value?.kind === "prompt"
              ? value.value
              : ""
            : null,
        );
      }
      beforeDispose(cancelDialogs);
      return () => (
        <>
          <dialog
            ref={__dom(element)}
            aria-labelledby="ui-dialog-title"
            aria-describedby="ui-dialog-message"
            className={__classes("ui-dialog")}
            onCancel={(event: any) => {
              event.preventDefault();
              finish(false);
            }}
            data-v-b0bbf977=""
          >
            {activeDialog.value ? (
              <form
                onSubmit={(event: any) => {
                  event.preventDefault();
                  finish(true);
                }}
                data-v-b0bbf977=""
              >
                <h2 id="ui-dialog-title" data-v-b0bbf977="">
                  {t("dialog.title")}
                </h2>
                <p id="ui-dialog-message" data-v-b0bbf977="">
                  {activeDialog.value.message}
                </p>
                {activeDialog.value.kind === "prompt" ? (
                  <input
                    ref={__dom(input)}
                    aria-label={activeDialog.value.message as any}
                    autoComplete="off"
                    {...__input(
                      value.value,
                      (__nextValue: any) => {
                        value.value = __nextValue;
                      },
                      "",
                      undefined,
                      [],
                    )}
                    data-v-b0bbf977=""
                  ></input>
                ) : null}
                <footer data-v-b0bbf977="">
                  <button
                    type="button"
                    autoFocus={true}
                    className={__classes("secondary-button")}
                    onClick={(event: any) => {
                      finish(false);
                    }}
                    data-v-b0bbf977=""
                  >
                    {t("dialog.cancel")}
                  </button>
                  <button
                    type="submit"
                    className={__classes("primary-button")}
                    data-v-b0bbf977=""
                  >
                    {t("dialog.confirm")}
                  </button>
                </footer>
              </form>
            ) : null}
          </dialog>
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
