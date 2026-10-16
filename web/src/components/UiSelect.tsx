import { t } from "../i18n";
import {
  afterMount,
  afterRender,
  attributes,
  beforeDispose,
  cell,
  derived,
  uniqueId,
  watchState,
} from "../state";

import { AnimatePresence, motion } from "framer-motion";
import { SPRING_SNAPPY } from "../motion";
import { Children, Fragment, isValidElement, type ReactNode } from "react";
import {
  __call,
  __classes,
  __each,
  __portal,
  __style,
  useController,
} from "../state";
import "./UiSelect.css";
type Props = {
  modelValue?: unknown;
  value?: unknown;
  menuClass?: string;
  modelModifiers?: {
    number?: boolean;
  };
} & { children?: import("react").ReactNode; [key: string]: any };
export default function UiSelect(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const emit = __emit;
      const attrs = attributes(),
        id = "select-" + uniqueId(),
        source = cell<HTMLSelectElement>(),
        button = cell<HTMLButtonElement>(),
        list = cell<HTMLUListElement>();
      const options = cell<
          {
            value: string;
            label: string;
            disabled: boolean;
            selected: boolean;
          }[]
        >([]),
        open = cell(false),
        active = cell(-1),
        label = cell(""),
        position = cell<Record<string, string>>({}),
        invalid = cell(false);
      const inheritedDisabled = cell(false);
      const inline = derived(() => Number(attrs.size) > 1),
        value = derived(() =>
          props.modelValue !== undefined ? props.modelValue : props.value,
        );
      const disabled = derived(
        () =>
          Boolean(attrs.disabled) ||
          inheritedDisabled.value ||
          !options.value.some((option) => !option.disabled),
      );
      const caption = derived(
        () =>
          options.value.find((option) => option.selected)?.label ||
          t("select.placeholder"),
      );
      let observer: MutationObserver | undefined,
        typed = "",
        typingTimer: ReturnType<typeof setTimeout> | undefined,
        frame = 0,
        disposed = false;
      function originalOptions(children: ReactNode): unknown[] {
        const values: unknown[] = [];
        Children.forEach(children, (child) => {
          if (!isValidElement<{ value?: unknown; children?: ReactNode }>(child))
            return;
          if (child.type === "option") values.push(child.props.value);
          else values.push(...originalOptions(child.props.children));
        });
        return values;
      }
      function sync() {
        if (!source.value) return;
        inheritedDisabled.value = source.value.matches(":disabled");
        options.value = Array.from(source.value.options).map((option) => ({
          value: option.value,
          label: option.label,
          disabled:
            option.disabled ||
            !!(
              option.parentElement instanceof HTMLOptGroupElement &&
              option.parentElement.disabled
            ),
          selected: option.selected,
        }));
        if (
          (open.value || inline.value) &&
          (active.value < 0 || active.value >= options.value.length)
        )
          active.value = options.value.findIndex(
            (option) => option.selected && !option.disabled,
          );
        if ((open.value || inline.value) && active.value < 0)
          active.value = options.value.findIndex((option) => !option.disabled);
        const parent = source.value.closest("label")?.cloneNode(true) as
          | HTMLElement
          | undefined;
        parent
          ?.querySelectorAll(".ui-select")
          .forEach((element) => element.remove());
        label.value = String(
          attrs["aria-label"] || parent?.textContent?.trim() || caption.value,
        );
        if (disabled.value) close(false);
      }
      function place() {
        if (!button.value) return;
        const rect = button.value.getBoundingClientRect(),
          below = innerHeight - rect.bottom - 16,
          above = rect.top - 16,
          up = below < 180 && above > below,
          height = Math.max(46, Math.min(320, up ? above : below));
        position.value = {
          position: "fixed",
          left:
            Math.max(
              8,
              Math.min(
                rect.left,
                innerWidth - Math.min(rect.width, innerWidth - 16) - 8,
              ),
            ) + "px",
          width: Math.min(rect.width, innerWidth - 16) + "px",
          maxHeight: height + "px",
          ...(up
            ? {
                bottom: innerHeight - rect.top + 8 + "px",
                transformOrigin: "50% 100%",
              }
            : {
                top: rect.bottom + 8 + "px",
                transformOrigin: "50% 0",
              }),
        };
      }
      function scrollActive() {
        void afterRender(() =>
          list.value
            ?.querySelector<HTMLElement>(`[data-index="${active.value}"]`)
            ?.scrollIntoView({
              block: "nearest",
            }),
        );
      }
      async function show() {
        if (disabled.value) return;
        sync();
        active.value = options.value.findIndex(
          (option) => option.selected && !option.disabled,
        );
        if (active.value < 0)
          active.value = options.value.findIndex((option) => !option.disabled);
        place();
        open.value = true;
        await afterRender();
        if (!open.value || disposed) return;
        list.value?.focus({
          preventScroll: true,
        });
        scrollActive();
      }
      function close(focus = true) {
        open.value = false;
        if (focus && !inline.value)
          button.value?.focus({
            preventScroll: true,
          });
      }
      function changed(event: Event) {
        const original = originalOptions(props.children)[
          source.value?.selectedIndex ?? -1
        ];
        let selected = original === undefined ? source.value?.value : original;
        if (
          props.modelModifiers?.number &&
          typeof selected === "string" &&
          selected !== "" &&
          !Number.isNaN(Number(selected))
        )
          selected = Number(selected);
        invalid.value = false;
        emit("update:modelValue", selected);
        emit("change", event);
        sync();
      }
      function choose(index: number) {
        if (
          disabled.value ||
          !options.value[index] ||
          options.value[index].disabled ||
          !source.value ||
          source.value.matches(":disabled")
        ) {
          close(false);
          return;
        }
        source.value.selectedIndex = index;
        source.value.dispatchEvent(
          new Event("change", {
            bubbles: true,
          }),
        );
        close();
        if (inline.value)
          list.value?.focus({
            preventScroll: true,
          });
      }
      function move(direction: number) {
        let next = active.value + direction;
        while (next >= 0 && next < options.value.length) {
          if (!options.value[next].disabled) {
            active.value = next;
            scrollActive();
            return;
          }
          next += direction;
        }
      }
      function keys(event: KeyboardEvent) {
        if (!inline.value && !open.value) {
          if (["ArrowDown", "ArrowUp", "Enter", " "].includes(event.key)) {
            event.preventDefault();
            void show();
          }
          return;
        }
        if (
          [
            "ArrowDown",
            "ArrowUp",
            "Home",
            "End",
            "Enter",
            " ",
            "Escape",
          ].includes(event.key)
        )
          event.preventDefault();
        if (event.key === "ArrowDown") move(1);
        else if (event.key === "ArrowUp") move(-1);
        else if (event.key === "Home") {
          active.value = -1;
          move(1);
        } else if (event.key === "End") {
          active.value = options.value.length;
          move(-1);
        } else if (event.key === "Enter" || event.key === " ")
          choose(active.value);
        else if (event.key === "Escape") {
          event.stopPropagation();
          close();
        } else if (event.key === "Tab") close(false);
        else if (event.key.length === 1 && !event.ctrlKey && !event.metaKey) {
          typed += event.key.toLocaleLowerCase();
          clearTimeout(typingTimer);
          typingTimer = setTimeout(() => {
            typed = "";
          }, 600);
          const hit = options.value.findIndex(
            (option) =>
              !option.disabled &&
              option.label.toLocaleLowerCase().startsWith(typed),
          );
          if (hit >= 0) {
            active.value = hit;
            scrollActive();
          }
        }
      }
      function outside(event: PointerEvent) {
        if (
          open.value &&
          !button.value?.contains(event.target as Node) &&
          !list.value?.contains(event.target as Node)
        )
          close(false);
      }
      function viewport(event: Event) {
        if (
          open.value &&
          !(event.target instanceof Node && list.value?.contains(event.target))
        )
          close(false);
      }
      watchState(value, () => void afterRender(sync), {
        flush: "post",
      });
      afterMount(() => {
        sync();
        active.value = options.value.findIndex((option) => option.selected);
        observer = new MutationObserver(() => {
          cancelAnimationFrame(frame);
          frame = requestAnimationFrame(() => {
            if (!disposed) sync();
          });
        });
        if (source.value) {
          observer.observe(source.value, {
            subtree: true,
            childList: true,
            characterData: true,
            attributes: true,
          });
          const fieldset = source.value.closest("fieldset");
          if (fieldset)
            observer.observe(fieldset, {
              attributes: true,
              attributeFilter: ["disabled"],
            });
        }
        document.addEventListener("pointerdown", outside);
        window.addEventListener("resize", viewport);
        window.addEventListener("scroll", viewport, true);
      });
      beforeDispose(() => {
        disposed = true;

        observer?.disconnect();
        clearTimeout(typingTimer);
        cancelAnimationFrame(frame);
        document.removeEventListener("pointerdown", outside);
        window.removeEventListener("resize", viewport);
        window.removeEventListener("scroll", viewport, true);
      });
      return () => (
        <>
          <span
            className={__classes("ux ui-select u031", {
              "is-open": open.value,
              "is-inline": inline.value,
            })}
            style={__style(attrs.style as any)}
          >
            {!inline.value ? (
              <button
                ref={__dom(button)}
                type="button"
                role="combobox"
                aria-label={label.value as any}
                aria-haspopup="listbox"
                aria-expanded={open.value as any}
                aria-controls={id as any}
                aria-invalid={invalid.value || (undefined as any)}
                aria-describedby={attrs["aria-describedby"] as string as any}
                disabled={disabled.value as any}
                className={__classes("u031-btn")}
                onClick={(event: any) => {
                  open.value ? close() : show();
                }}
                onKeyDown={(event: any) => {
                  __call(keys, event);
                }}
              >
                <span className={__classes("u031-v")}>{caption.value}</span>
                <span aria-hidden="true" className="u031-car" />
              </button>
            ) : null}
            <select
              ref={__dom(source)}
              {...attrs}
              value={value.value as any}
              tabIndex={-1}
              aria-hidden="true"
              className={__classes("ui-select-source", attrs.className)}
              onChange={(event: any) => {
                __call(changed, event);
              }}
              onInvalid={(event: any) => {
                event.preventDefault();
                invalid.value = true;
                button.value?.focus();
              }}
            >
              {__props.children}
            </select>
            {__portal(
              <>
                <AnimatePresence mode="wait">
                  {open.value || inline.value ? (
                    <motion.ul
                      id={id as any}
                      ref={__dom(list)}
                      role="listbox"
                      tabIndex={0}
                      aria-label={label.value as any}
                      aria-activedescendant={
                        active.value >= 0
                          ? id + "-" + active.value
                          : (undefined as any)
                      }
                      className={__classes("ux u031-list", [
                        {
                          "is-inline": inline.value,
                        },
                        __props.menuClass,
                      ])}
                      style={__style(inline.value ? undefined : position.value)}
                      onKeyDown={(event: any) => {
                        __call(keys, event);
                      }}
                      onFocus={(event: any) => {
                        inline.value && sync();
                      }}
                      onBlur={(event: any) => {
                        !list.value?.contains(event.relatedTarget as Node) &&
                          close(false);
                      }}
                      initial={{ opacity: 0, y: 4, scale: 0.98 }}
                      animate={{ opacity: 1, y: 0, scale: 1 }}
                      exit={{ opacity: 0, y: 2, scale: 0.98 }}
                      transition={SPRING_SNAPPY}
                    >
                      <Fragment>
                        {__each(options.value, (option, index) => (
                          <li
                            id={(id + "-" + index) as any}
                            key={index as any}
                            data-index={index as any}
                            role="option"
                            aria-selected={option.selected as any}
                            aria-disabled={
                              option.disabled || (undefined as any)
                            }
                            className={__classes("u031-o", {
                              "is-active": active.value === index,
                            })}
                            onPointerMove={(event: any) => {
                              !option.disabled && (active.value = index);
                            }}
                            onMouseDown={(event: any) => {
                              event.preventDefault();
                              undefined;
                            }}
                            onClick={(event: any) => {
                              event.stopPropagation();
                              choose(index);
                            }}
                          >
                            <span>{option.label}</span>
                            <svg
                              viewBox="0 0 18 18"
                              aria-hidden="true"
                              className={__classes("u031-ck")}
                            >
                              <path d="M4 9.5l3.5 3.5L14 6"></path>
                            </svg>
                          </li>
                        ))}
                      </Fragment>
                    </motion.ul>
                  ) : null}
                </AnimatePresence>
              </>,
              "body",
              inline.value,
            )}
          </span>
        </>
      );
    },
    input,
    ["modelValue", "value", "menuClass", "modelModifiers"],
    [],
    {},
    false,
  );
}
