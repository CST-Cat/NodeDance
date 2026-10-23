import {
  Children,
  Fragment,
  isValidElement,
  type ComponentProps,
  type ReactNode,
} from "react";
import { Card, CardContent } from "./ui/card";
import { Separator } from "./ui/separator";

function rows(children: ReactNode): ReactNode[] {
  return Children.toArray(children).flatMap((child) =>
    isValidElement<{ children?: ReactNode }>(child) && child.type === Fragment
      ? rows(child.props.children)
      : [child],
  );
}

export function SettingsRows({ children }: { children: ReactNode }) {
  return rows(children).map((child, index) => (
    <Fragment
      key={isValidElement(child) && child.key != null ? child.key : index}
    >
      {index ? <Separator className="bg-(--workspace-divider)" /> : null}
      <div className="workspace-group-row">{child}</div>
    </Fragment>
  ));
}

export function SettingsGroup({
  children,
  className = "",
  ...props
}: ComponentProps<"div">) {
  return (
    <Card className="workspace-group bg-transparent border-(--workspace-divider) text-(--text)">
      <CardContent>
        <div {...props} className={`workspace-group-rows ${className}`}>
          <SettingsRows>{children}</SettingsRows>
        </div>
      </CardContent>
    </Card>
  );
}
