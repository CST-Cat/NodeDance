import { t } from "../i18n";
import type { BodySchema } from "../operations";
import { useController } from "../state";
import { Input, Textarea, Checkbox, Select } from "./ManagementUI";
import "./ContractFields.css";
type Props = {
  schema?: BodySchema;
  values: Record<string, unknown>;
  omit?: string[];
  [key: string]: any;
};
export default function ContractFields(input: Props) {
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
        <div className="contract-fields">
          {Object.entries(props.schema?.properties ?? {})
            .filter(([key]) => !props.omit?.includes(key))
            .map(([key, field]) => {
              const label = t(`host.field.${key}`),
                value = props.values[key];
              return (
                <div
                  key={key}
                  className="contract-field"
                  data-field-type={field.enum ? "enum" : field.type}
                >
                  {field.type === "boolean" ? (
                    <Checkbox
                      label={label}
                      checked={!!value}
                      onCheckedChange={(checked) => {
                        props.values[key] = checked === true;
                      }}
                    />
                  ) : field.enum ? (
                    <Select
                      label={label}
                      value={String(value ?? "")}
                      options={field.enum.map((option) => ({
                        value: String(option),
                        label: option === "" ? "—" : String(option),
                      }))}
                      onValueChange={(value) => update(key, field, value)}
                    />
                  ) : field.type === "array" || key === "script" ? (
                    <Textarea
                      label={label}
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
                      label={label}
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
              );
            })}
        </div>
      );
    },
    input,
    ["schema", "values", "omit"],
    [],
    {},
    true,
  );
}
