import { t } from "../i18n";
import type { BodySchema } from "../operations";

import { Fragment } from "react";
import { __classes, __each, useController } from "../state";
import "./ContractFields.css";
import UiSelect from "./UiSelect";
type Props = {
  schema?: BodySchema;
  values: Record<string, unknown>;
  omit?: string[];
} & { children?: import("react").ReactNode; [key: string]: any };
export default function ContractFields(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      function update(key: string, schema: BodySchema, event: Event) {
        const element = event.target as HTMLInputElement;
        props.values[key] =
          schema.type === "boolean"
            ? element instanceof HTMLInputElement && element.type === "checkbox"
              ? element.checked
              : element.value === "true"
            : schema.type === "integer"
              ? /^-?\d+$/.test(element.value)
                ? BigInt(element.value)
                : element.value
              : schema.type === "number"
                ? Number(element.value)
                : schema.type === "array"
                  ? element.value
                      .split("\n")
                      .map((x) => x.trim())
                      .filter(Boolean)
                  : element.value;
      }
      function label(key: string) {
        return t(`host.field.${key}`);
      }
      return () => (
        <>
          <div className={__classes("contract-fields")} data-v-d4e6d614="">
            <Fragment>
              {__each(
                Object.entries(__props.schema?.properties ?? {}).filter(
                  ([key]) => !__props.omit?.includes(key),
                ),
                ([key, field]) => (
                  <label
                    key={key as any}
                    data-field-type={field.enum ? "enum" : (field.type as any)}
                    className={__classes({
                      "check-row": field.type === "boolean",
                    })}
                    data-v-d4e6d614=""
                  >
                    <span data-v-d4e6d614="">{label(key)}</span>
                    {field.type === "boolean" ? (
                      <input
                        type="checkbox"
                        checked={!!__props.values[key] as any}
                        onChange={(event: any) => {
                          update(key, field, event);
                        }}
                        data-v-d4e6d614=""
                      ></input>
                    ) : field.enum ? (
                      <UiSelect
                        value={String(__props.values[key] ?? "") as any}
                        onChange={(event: any) => {
                          update(key, field, event);
                        }}
                        data-v-d4e6d614=""
                      >
                        <Fragment>
                          {__each(field.enum, (option) => (
                            <option
                              key={String(option) as any}
                              value={String(option) as any}
                              data-v-d4e6d614=""
                            >
                              {option === "" ? "—" : option}
                            </option>
                          ))}
                        </Fragment>
                      </UiSelect>
                    ) : field.type === "array" ? (
                      <textarea
                        rows={3}
                        value={
                          Array.isArray(__props.values[key])
                            ? (__props.values[key] as string[]).join("\n")
                            : ("" as any)
                        }
                        onInput={(event: any) => {
                          update(key, field, event);
                        }}
                        data-v-d4e6d614=""
                      ></textarea>
                    ) : key === "script" ? (
                      <textarea
                        rows={6}
                        value={String(__props.values[key] ?? "") as any}
                        spellCheck="false"
                        onInput={(event: any) => {
                          update(key, field, event);
                        }}
                        data-v-d4e6d614=""
                      ></textarea>
                    ) : (
                      <input
                        type={
                          field.type === "number" ? "number" : ("text" as any)
                        }
                        inputMode={
                          field.type === "integer"
                            ? "numeric"
                            : (undefined as any)
                        }
                        step={
                          field.type === "number" ? "any" : (undefined as any)
                        }
                        value={__props.values[key] ?? ("" as any)}
                        placeholder={
                          field.format === "date-time"
                            ? "2026-12-01T00:00:00Z"
                            : (undefined as any)
                        }
                        onInput={(event: any) => {
                          update(key, field, event);
                        }}
                        data-v-d4e6d614=""
                      ></input>
                    )}
                  </label>
                ),
              )}
            </Fragment>
          </div>
        </>
      );
    },
    input,
    ["schema", "values", "omit"],
    [],
    {},
    true,
  );
}
