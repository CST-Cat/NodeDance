import type { ReactNode } from "react";
import ResourceGauge from "./ResourceGauge";
type Props = { label: string; value: number | null; detail: ReactNode; resource?: string; highAt?: number; valueTestId?: string; labelContent?: ReactNode; children?: ReactNode };
export default function ResourceReading({label, value, detail, resource, highAt, valueTestId, labelContent, children}: Props) {
  return <div className="resource-reading" data-resource={resource}>
    <ResourceGauge label={label} value={value} highAt={highAt} valueTestId={valueTestId} />
    {labelContent ?? <span>{label}</span>}
    <small>{detail}</small>
    {children}
  </div>;
}
