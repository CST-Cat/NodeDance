import {
  Activity,
  Box,
  Cloud,
  Database,
  Folder,
  Globe,
  Server,
  Shield,
  Terminal,
} from "lucide-react";
import { serviceLogos } from "../service-logos";
import { systemLogos } from "../system-logo";
import OsLogo from "./OsLogo";
import "./NodeLogo.css";
const icons = {
  server: Server,
  globe: Globe,
  database: Database,
  shield: Shield,
  terminal: Terminal,
  box: Box,
  cloud: Cloud,
  folder: Folder,
  activity: Activity,
};
/** SVG assets only; the discovered Core enum owns which choices can be saved. */
export default function NodeLogo({
  icon,
  platform,
  os,
}: {
  icon?: string;
  platform?: string | null;
  os?: string | null;
}) {
  const brand = icon
    ? (serviceLogos[icon] ?? systemLogos[icon as keyof typeof systemLogos])
    : undefined;
  if (brand)
    return (
      <svg
        className="product-logo"
        data-node-icon={icon}
        viewBox="0 0 24 24"
        fill="currentColor"
        aria-hidden="true"
      >
        <path d={brand.path} />
      </svg>
    );
  const Icon = icons[icon as keyof typeof icons];
  return Icon ? (
    <Icon className="product-logo" data-node-icon={icon} aria-hidden="true" />
  ) : (
    <OsLogo platform={platform} os={os} />
  );
}
