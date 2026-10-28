import { activeDialog, cancelDialogs, finishDialog } from "../dialogs";
import { t } from "../i18n";
import {
  afterRender,
  beforeDispose,
  cell,
  watchState,
} from "../state";
import { installScrollEdges } from "../scroll-edge";

import { Fragment, useEffect, useId, useRef } from "react";
import { __classes, __each, __input, useController } from "../state";
import "./UiDialog.css";
type Props = {} & { children?: import("react").ReactNode; [key: string]: any };
export default function UiDialog(input: Props) {
  return typeof input.open === "boolean" ? <FormDialog {...input} /> : <SystemDialog {...input} />;
}

function FormDialog(input: Props) {
  const element = useRef<HTMLDialogElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const id = `form-dialog-${useId().replace(/:/g, "")}`;
  useEffect(() => {
    const dialog = element.current;
    if (!dialog) return;
    // A closed dialog owns no focus. Its cleanup must not move focus away
    // from the trigger that is about to open this or another dialog.
    if (!input.open) {
      if (dialog.open) dialog.close();
      return;
    }
    const returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (!dialog.open) dialog.showModal();
    return () => {
      if (dialog.open) dialog.close();
      if (returnFocus?.isConnected) returnFocus.focus({ preventScroll: true });
    };
  }, [input.open]);
  useEffect(() => {
    const edges = installScrollEdges(body.current ?? undefined);
    return () => edges?.dispose();
  }, []);
  return <dialog ref={element} id={id} aria-labelledby={`${id}-title`} aria-busy={input.busy || undefined}
    className={`ui-dialog ${input.className ?? ""}`} data-v-b0bbf977=""
    onCancel={event => { event.preventDefault(); if (!input.busy) input.onClose?.(); }}>
    <div className="ui-dialog-head" data-v-b0bbf977="">
      <h2 id={`${id}-title`} data-v-b0bbf977="">{input.title}</h2>
      <button type="button" className="ui-dialog-close" aria-label={t("ui.close")}
        onClick={event => { if (!input.busy) input.onClose?.(); }} data-v-b0bbf977="">×</button>
    </div>
    <div className="ui-dialog-body scroll-fade" ref={body} data-v-b0bbf977="">{input.children}</div>
  </dialog>;
}

function SystemDialog(input: Props) {
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
          } else if (request.kind === "choice") {
            const radios = element.value?.querySelectorAll<HTMLInputElement>(
              ".ui-dialog-choice input[type=radio]",
            );
            const checked = Array.from(radios ?? []).find(
              (radio) => radio.checked,
            );
            (checked ?? radios?.[0])?.focus();
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
            ? activeDialog.value?.kind === "confirm"
              ? ""
              : value.value
            : null,
        );
      }
      beforeDispose(() => {
        cancelDialogs();
      });
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
                <div className="ui-dialog-head" data-v-b0bbf977="">
                  <h2 id="ui-dialog-title" data-v-b0bbf977="">
                    {t("dialog.title")}
                  </h2>
                  <button
                    type="button"
                    className={__classes("ui-dialog-close")}
                    aria-label={t("m.16707b59cf5e") as any}
                    onClick={(event: any) => {
                      finish(false);
                    }}
                    data-v-b0bbf977=""
                  >
                    {"×"}
                  </button>
                </div>
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
                ) : activeDialog.value.kind === "choice" ? (
                  <div
                    role="radiogroup"
                    aria-label={activeDialog.value.message as any}
                    className={__classes("ui-dialog-choices")}
                    data-v-b0bbf977=""
                  >
                    <Fragment>
                      {__each(activeDialog.value.choices ?? [], (choice) => (
                        <label
                          key={choice.value as any}
                          className={__classes("ui-dialog-choice")}
                          data-v-b0bbf977=""
                        >
                          <input
                            type="radio"
                            name="ui-dialog-choice"
                            checked={(value.value === choice.value) as any}
                            onChange={(__event: any) => {
                              value.value = choice.value;
                            }}
                            data-v-b0bbf977=""
                          ></input>
                          <span data-v-b0bbf977="">
                            <span
                              className={__classes("ui-dialog-choice-label")}
                              data-v-b0bbf977=""
                            >
                              {choice.label}
                            </span>
                            {choice.detail ? (
                              <small
                                className={__classes(
                                  "ui-dialog-choice-detail",
                                )}
                                data-v-b0bbf977=""
                              >
                                {choice.detail}
                              </small>
                            ) : null}
                          </span>
                        </label>
                      ))}
                    </Fragment>
                  </div>
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
                    {activeDialog.value.confirmLabel ?? t("dialog.confirm")}
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
