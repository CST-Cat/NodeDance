import type { ReactNode } from "react";
import "./MobileOverviewBar.css";

export default function MobileOverviewBar({ children }: { children: ReactNode }) {
  return <div className="overview-toolbar-slot">
    <div className="overview-toolbar-panel">{children}</div>
  </div>;
}
