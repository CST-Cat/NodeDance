import { t } from "../i18n";
import {
  Children,
  Fragment,
  createContext,
  isValidElement,
  useContext,
  useState,
  type ComponentProps,
  type ReactNode,
} from "react";
import {
  Select as ArcSelect,
  type SelectProps,
} from "@/components/arc/select/select";
export { Button } from "@/components/arc/button/button";
export { Input } from "@/components/arc/input/input";
export { ExpandedTextarea as Textarea } from "./ExpandedTextarea";
export { Checkbox } from "@/components/arc/checkbox/checkbox";
export { Card } from "@/components/arc/card/card";
export { Badge } from "@/components/arc/badge/badge";
export { Alert } from "@/components/arc/alert/alert";
export { Tabs, TabsList, TabsTrigger } from "@/components/arc/tabs/tabs";
import "./ManagementUI.css";

const PortalContainer = createContext<HTMLElement | null>(null);
/** A product theme/portal boundary around the installed shadcn registry components. */
export function ManagementPage({
  className,
  children,
  embedded = false,
  registryStyles = false,
  ...props
}: ComponentProps<"section"> & {
  embedded?: boolean;
  registryStyles?: boolean;
}) {
  const [container, setContainer] = useState<HTMLElement | null>(null);
  return (
    <section
      {...props}
      ref={setContainer}
      className={`product-arc arc-ui ${registryStyles ? "registry-controls" : "management-ui"} ${embedded ? "" : "workspace-page"} ${className ?? ""}`}
    >
      <PortalContainer.Provider value={container}>
        {children}
      </PortalContainer.Provider>
    </section>
  );
}
export function Select(props: SelectProps) {
  return (
    <ArcSelect
      {...props}
      placeholder={props.placeholder ?? t("management.selectPlaceholder")}
      portalContainer={useContext(PortalContainer)}
    />
  );
}

/** Preserve the current boolean/array selection model without synthesizing DOM events. */
export function checkboxProps(
  value: any,
  set: (value: any) => void,
  choice?: any,
) {
  return {
    checked: Array.isArray(value) ? value.includes(choice) : !!value,
    onCheckedChange: (checked: boolean | "indeterminate") =>
      set(
        Array.isArray(value)
          ? checked === true
            ? [...value, choice]
            : value.filter((item) => item !== choice)
          : checked === true,
      ),
  };
}
function plainText(value: ReactNode): string {
  return Children.toArray(value)
    .map((item) =>
      isValidElement<{ children?: ReactNode }>(item)
        ? plainText(item.props.children)
        : String(item),
    )
    .join("");
}
/** Render-only option composition; request validation remains in Core. */
export function selectOptions(children: ReactNode): SelectProps["options"] {
  return Children.toArray(children).flatMap((child) => {
    if (
      !isValidElement<{
        value?: string | number;
        disabled?: boolean;
        children?: ReactNode;
      }>(child)
    )
      return [];
    if (child.type === Fragment) return selectOptions(child.props.children);
    return child.type === "option"
      ? [
          {
            value: String(child.props.value ?? plainText(child.props.children)),
            label: plainText(child.props.children),
            disabled: child.props.disabled,
          },
        ]
      : [];
  });
}
