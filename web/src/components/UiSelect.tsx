import { Children, isValidElement, type ReactNode } from "react";
import { Select } from "./arc/select/select";
import { selectOptions } from "./ManagementUI";
import "./UiSelect.css";

type Props = { modelValue?: unknown; value?: unknown; menuClass?: string; modelModifiers?: { number?: boolean }; children?: ReactNode; [key: string]: any };
export default function UiSelect({ modelValue, value, menuClass, modelModifiers, children, onUpdateModelValue, ...attrs }: Props) {
  const values: unknown[] = [];
  const collect = (nodes: ReactNode) => Children.forEach(nodes, child => {
    if (!isValidElement<{ value?: unknown; children?: ReactNode }>(child)) return;
    if (child.type === "option") values.push(child.props.value);
    else collect(child.props.children);
  });
  collect(children);
  const change = (next: string) => {
    const original = values.find(value => String(value) === next);
    const result = modelModifiers?.number ? Number(next) : original ?? next;
    onUpdateModelValue?.(result);
    attrs.onChange?.({ target: { value: next }, currentTarget: { value: next } });
  };
  return <span className="ui-select product-arc arc-ui" style={attrs.style}>
    <Select value={String(modelValue ?? value ?? "")} options={selectOptions(children)}
      disabled={attrs.disabled} name={attrs.name}
      aria-label={attrs["aria-label"]} className="u031-btn" onValueChange={change} />
  </span>;
}
