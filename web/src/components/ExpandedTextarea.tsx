import { forwardRef, useId, useLayoutEffect, useRef, useState } from "react";
import { locale, t } from "../i18n";
import { Button } from "./arc/button/button";
import { Textarea, type TextareaProps } from "./arc/textarea/textarea";
import {
  Popover,
  PopoverClose,
  PopoverContent,
  PopoverTrigger,
} from "./arc/popover/popover";
import "./ExpandedTextarea.css";

export const ExpandedTextarea = forwardRef<HTMLTextAreaElement, TextareaProps>(
  function ExpandedTextarea(
    {
      id,
      label,
      description,
      error,
      className,
      disabled,
      value,
      defaultValue,
      onChange,
      onInput,
      name,
      form,
      ...props
    },
    ref,
  ) {
    const generatedId = useId();
    const controlId = id ?? `expanded-textarea-${generatedId}`;
    const trigger = useRef<HTMLButtonElement>(null);
    const [open, setOpen] = useState(false);
    const [inheritedDisabled, setInheritedDisabled] = useState(false);
    const [caption, setCaption] = useState("");
    const [uncontrolled, setUncontrolled] = useState(defaultValue ?? "");
    const current = value ?? uncontrolled;
    const accessibleName = props["aria-label"] ?? label ?? caption;
    useLayoutEffect(() => {
      const button = trigger.current;
      if (!button) return;
      const sync = () => {
        setInheritedDisabled(button.matches(":disabled"));
        const owner = button.labels?.[0]?.cloneNode(true) as
          | HTMLElement
          | undefined;
        owner
          ?.querySelectorAll("[data-expanded-textarea]")
          .forEach((node) => node.remove());
        setCaption(owner?.textContent?.trim() ?? "");
      };
      sync();
      const fieldset = button.closest("fieldset");
      const observer = new MutationObserver(sync);
      if (fieldset)
        observer.observe(fieldset, {
          attributes: true,
          attributeFilter: ["disabled"],
        });
      return () => observer.disconnect();
    }, [disabled, label, locale.value]);
    useLayoutEffect(() => {
      if (disabled || inheritedDisabled) setOpen(false);
    }, [disabled, inheritedDisabled]);
    const blocked = disabled || inheritedDisabled;
    return (
      <div
        className={`product-arc arc-ui registry-controls expanded-textarea ${className ?? ""}`}
        data-expanded-textarea=""
        data-field-name={name}
      >
        {label ? <label htmlFor={controlId}>{label}</label> : null}
        <Popover
          open={open}
          onOpenChange={(next) => {
            if (!blocked) setOpen(next);
          }}
        >
          <PopoverTrigger asChild>
            <Button
              ref={trigger}
              id={controlId}
              type="button"
              variant="secondary"
              disabled={disabled}
              aria-label={accessibleName || undefined}
              aria-invalid={!!error || props["aria-invalid"]}
              aria-describedby={props["aria-describedby"]}
              data-form-control=""
              data-expanded-textarea-trigger=""
            >
              {String(current).length
                ? t("expandedTextarea.filled")
                : props.required
                  ? t("expandedTextarea.required")
                  : t("expandedTextarea.empty")}
            </Button>
          </PopoverTrigger>
          <PopoverContent
            className="product-arc arc-ui registry-controls expanded-textarea-panel"
            aria-label={accessibleName || t("expandedTextarea.edit")}
            align="end"
          >
            <div className="expanded-textarea-editor">
              <Textarea
                {...props}
                minLength={undefined}
                maxLength={undefined}
                ref={ref}
                id={`${controlId}-editor`}
                label={accessibleName || undefined}
                description={description}
                error={error}
                aria-label={accessibleName || t("expandedTextarea.edit")}
                disabled={blocked}
                value={current}
                rows={Math.max(props.rows ?? 6, 6)}
                onInput={onInput}
                onChange={(event) => {
                  setUncontrolled(event.target.value);
                  onChange?.(event);
                }}
              />
              <div className="expanded-textarea-actions">
                <PopoverClose asChild>
                  <Button type="button" variant="secondary">
                    {t("expandedTextarea.done")}
                  </Button>
                </PopoverClose>
              </div>
            </div>
          </PopoverContent>
        </Popover>
        <div hidden aria-hidden="true" className="expanded-textarea-source">
          <Textarea
            {...props}
            minLength={undefined}
            maxLength={undefined}
            id={`${controlId}-source`}
            aria-label={undefined}
            aria-labelledby={undefined}
            form={form}
            name={name}
            disabled={blocked}
            value={current}
            onChange={(event) => {
              setUncontrolled(event.target.value);
              onChange?.(event);
            }}
            onInvalid={(event) => {
              event.preventDefault();
              setOpen(true);
            }}
          />
        </div>
      </div>
    );
  },
);
