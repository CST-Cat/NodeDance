import { ExpandedTextarea } from "./ExpandedTextarea";
import { useId } from "react";
import { SettingsGroup } from "./SettingsGroup";
import { t } from "../i18n";
import type { BodySchema } from "../operations";
import { useController } from "../state";
import { Input, Checkbox, Select } from "./ManagementUI";
import "./ContractFields.css";
type Props = {
  schema?: BodySchema;
  values: Record<string, unknown>;
  omit?: string[];
  disabled?: boolean;
  [key: string]: any;
};
export default function ContractFields(input: Props) {
  const prefix = useId();
  return useController(
    (props: Props) => {
      function update(key: string, field: BodySchema, value: string) {
        props.values[key] =
          field.type === "integer"
            ? /^-?\d+$/.test(value)
              ? BigInt(value)
              : value
            : field.type === "number"
              ? Number(value)
              : field.type === "array"
                ? value
                    .split("\n")
                    .map((x) => x.trim())
                    .filter(Boolean)
                : value;
      }
      return () => (
        <SettingsGroup className="contract-fields">
          {Object.entries(props.schema?.properties ?? {})
            .filter(([key]) => !props.omit?.includes(key))
            .map(([key, field]) => {
              const translated = t(`host.field.${key}`),
                label =
                  translated === `host.field.${key}`
                    ? field.description || key
                    : translated,
                id = `${prefix}-${key}`,
                value = props.values[key];
              return (
                <div
                  key={key}
                  className="parameter-row contract-field"
                  data-field-type={field.enum ? "enum" : field.type}
                >
                  <label htmlFor={id}>{label}</label>
                  <div
                    className={
                      field.type === "boolean"
                        ? "contract-control contract-toggle"
                        : "contract-control"
                    }
                  >
                    {field.type === "boolean" ? (
                      <Checkbox
                        id={id}
                        aria-label={label}
                        disabled={props.disabled}
                        checked={!!value}
                        onCheckedChange={(checked) => {
                          props.values[key] = checked === true;
                        }}
                      />
                    ) : field.enum ? (
                      <Select
                        id={id}
                        aria-label={label}
                        disabled={props.disabled}
                        value={String(value ?? "")}
                        options={field.enum.map((option) => ({
                          value: String(option),
                          label: option === "" ? "—" : String(option),
                        }))}
                        onValueChange={(value) => update(key, field, value)}
                      />
                    ) : field.type === "array" || key === "script" ? (
                      <ExpandedTextarea
                        id={id}
                        aria-label={label}
                        disabled={props.disabled}
                        rows={key === "script" ? 6 : 3}
                        value={
                          field.type === "array"
                            ? Array.isArray(value)
                              ? value.join("\n")
                              : ""
                            : String(value ?? "")
                        }
                        spellCheck={false}
                        onChange={(event) =>
                          update(key, field, event.target.value)
                        }
                      />
                    ) : (
                      <Input
                        id={id}
                        aria-label={label}
                        disabled={props.disabled}
                        type={field.type === "number" ? "number" : "text"}
                        inputMode={
                          field.type === "integer" ? "numeric" : undefined
                        }
                        step={field.type === "number" ? "any" : undefined}
                        value={String(value ?? "")}
                        placeholder={
                          field.format === "date-time"
                            ? "2026-12-01T00:00:00Z"
                            : undefined
                        }
                        onChange={(event) =>
                          update(key, field, event.target.value)
                        }
                      />
                    )}
                  </div>
                </div>
              );
            })}
        </SettingsGroup>
      );
    },
    input,
    ["schema", "values", "omit", "disabled"],
    [],
    {},
    true,
  );
}
