export type ComparisonValue = boolean | "partial" | string | { value: boolean | "partial" | string; note?: string };

export type ComparisonColumn = {
  id: string;
  name: string;
  /** A short line under the name, such as a price. */
  caption?: string;
  /** Your product. It gets the tinted band and stays visible on phones. */
  highlight?: boolean;
};

export type ComparisonRow = {
  id: string;
  feature: string;
  /** Secondary text under the feature name. */
  hint?: string;
  values: Record<string, ComparisonValue>;
};

export type ComparisonSection = { id: string; title: string; rows: ComparisonRow[] };

export const comparisonColumns: ComparisonColumn[] = [
  { id: "relay", name: "Relay", caption: "$10 per seat", highlight: true },
  { id: "suite", name: "Legacy suite", caption: "$24 per seat" },
  { id: "sheets", name: "Spreadsheets", caption: "Free" },
  { id: "point", name: "Point tools", caption: "$16 per seat" },
];

export const comparisonSections: ComparisonSection[] = [
  {
    id: "planning", title: "Planning", rows: [
      { id: "projects", feature: "Projects", values: { relay: "Unlimited", suite: "Up to 50", sheets: "Unlimited", point: "Up to 10" } },
      { id: "roadmaps", feature: "Timelines and roadmaps", values: { relay: true, suite: true, sheets: false, point: { value: "partial", note: "Timeline only" } } },
      { id: "dependencies", feature: "Dependencies", hint: "Blocked work moves when its blocker does", values: { relay: true, suite: true, sheets: false, point: false } },
      { id: "templates", feature: "Templates", values: { relay: true, suite: true, sheets: { value: "partial", note: "Manual copies" }, point: true } },
    ],
  },
  {
    id: "collaboration", title: "Collaboration", rows: [
      { id: "realtime", feature: "Real time editing", values: { relay: true, suite: false, sheets: true, point: true } },
      { id: "guests", feature: "Free guest access", values: { relay: true, suite: false, sheets: true, point: { value: "partial", note: "Five guests" } } },
      { id: "comments", feature: "Comments and mentions", values: { relay: true, suite: true, sheets: true, point: true } },
      { id: "offline", feature: "Offline mode", hint: "Edits sync when you reconnect", values: { relay: true, suite: false, sheets: { value: "partial", note: "Desktop app only" }, point: false } },
    ],
  },
  {
    id: "automation", title: "Automation", rows: [
      { id: "rules", feature: "Rules and triggers", values: { relay: true, suite: true, sheets: false, point: { value: "partial", note: "Paid add on" } } },
      { id: "ai", feature: "AI summaries", values: { relay: true, suite: false, sheets: false, point: false } },
      { id: "api", feature: "Public API", values: { relay: true, suite: true, sheets: true, point: true } },
    ],
  },
  {
    id: "security", title: "Security", rows: [
      { id: "sso", feature: "SAML single sign on", values: { relay: true, suite: true, sheets: { value: "partial", note: "Enterprise plan" }, point: false } },
      { id: "audit", feature: "Audit log", values: { relay: true, suite: true, sheets: false, point: false } },
      { id: "residency", feature: "EU data residency", values: { relay: true, suite: { value: "partial", note: "Enterprise plan" }, sheets: true, point: false } },
    ],
  },
];
