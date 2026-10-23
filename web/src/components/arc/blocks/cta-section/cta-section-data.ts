import { avatar, type PersonId } from "../../lib/media";

export interface CtaAction {
  label: string;
  /** Renders a link. */
  href?: string;
  onClick?: () => void;
  /** With no href or onClick, the button confirms in place with this label, which suits previews. */
  confirmedLabel?: string;
}

export const ctaCopy = {
  centered: {
    title: "Start building in minutes",
    description: "Set up a workspace, invite your team, and ship the first project this week. Free for 14 days.",
    primary: { label: "Start free trial", confirmedLabel: "Trial started" },
    secondary: { label: "Talk to sales", confirmedLabel: "We'll email you" },
    note: "No credit card required",
  },
  split: {
    title: "Your team, set up before lunch",
    description: "Import your projects, bring everyone in, and keep working the way you already do.",
    primary: { label: "Create a workspace", confirmedLabel: "Workspace created" },
    secondary: { label: "Book a demo", confirmedLabel: "Demo requested" },
    points: ["Import from any tracker in one step", "Single sign-on and audit logs included", "Cancel anytime from settings"],
  },
  banner: {
    title: "Workflows are here",
    description: "Automate handoffs between teams with no code.",
    primary: { label: "See how it works", confirmedLabel: "Opened" },
  },
} satisfies Record<string, { title: string; description: string; primary: CtaAction; secondary?: CtaAction; note?: string; points?: string[] }>;

/** Faces for the social proof line in the centered variant. */
export const ctaFaces = (["sofia-ramirez", "nathan-cole", "chloe-nguyen", "tyler-hayes"] as PersonId[]).map(id => avatar(id));

/** Sample content for the split variant's setup card. */
export const ctaSetup = {
  workspace: "Northwind",
  steps: ["Create workspace", "Import 214 issues", "Invite 4 teammates"],
  faces: (["emma-collins", "marcus-johnson", "jasmine-brooks", "daniel-kim"] as PersonId[]).map(id => avatar(id)),
};
