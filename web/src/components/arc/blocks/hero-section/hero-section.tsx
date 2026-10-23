"use client";

import { useState } from "react";
import type { ReactNode } from "react";
import SegmentedControl from "../../segmented-control/segmented-control";
import { HeroLumen } from "./hero-lumen";
import { HeroRelay } from "./hero-relay";
import { HeroCadence } from "./hero-cadence";
import { HeroMesh } from "./hero-mesh";
import type { HeroMeshPoint } from "./hero-mesh";
import { HeroContent } from "./hero-content";
import type { HeroAction, HeroInstallCommand } from "./hero-content";
import styles from "./hero-section.module.css";

export { HeroCadence, HeroContent, HeroLumen, HeroMesh, HeroRelay };
export type { HeroAction, HeroInstallCommand, HeroMeshPoint };

export type HeroSectionVariant = "centered" | "split" | "minimal";

export interface HeroSectionProps {
  /**
   * `centered` is a headline over a drifting mesh with a live dashboard rising from the bottom edge, `split` sets the copy
   * beside a live workflow graph that runs sample events node by node, and `minimal` is large type over a mesh gradient.
   * Each design fills one full screen.
   */
  variant?: HeroSectionVariant;
  /** Plays the entrance once on mount. Defaults to true. */
  animateIn?: boolean;
  /** The main call to action. With `doneLabel` and no `href`, the button confirms in place. */
  primaryAction?: HeroAction | null;
  /** The second call to action. */
  secondaryAction?: HeroAction | null;
  /**
   * Your own headline. When set, the hero renders your content in the chosen layout (see `HeroContent`) instead of the
   * designed sample, together with the props below.
   */
  title?: string;
  description?: string;
  announcement?: HeroAction | null;
  /** Install commands for your own content; the first one is shown with a copy button. */
  install?: HeroInstallCommand[] | null;
  /** A visual beside your own content in the split layout. */
  media?: ReactNode;
  /** Small facts under your own content, such as the version and license. */
  meta?: string[];
  /** Kept for compatibility; highlighted phrases render as plain text. */
  highlight?: string;
  className?: string;
}

/**
 * A full screen landing page hero in three designs: a product screenshot in perspective over a drifting mesh, a live workflow
 * graph that routes sample events, and editorial type over a mesh gradient.
 */
export function HeroSection({ variant = "centered", animateIn = true, primaryAction, secondaryAction, title, description, announcement, install, media, meta, className }: HeroSectionProps) {
  if (title) return <HeroContent layout={variant} title={title} description={description} announcement={announcement} primaryAction={primaryAction} secondaryAction={secondaryAction} install={install} media={media} meta={meta} animateIn={animateIn} className={className} />;
  const actions = { primaryAction: primaryAction ?? undefined, secondaryAction: secondaryAction ?? undefined };
  if (variant === "split") return <HeroRelay animateIn={animateIn} className={className} {...actions} />;
  if (variant === "minimal") return <HeroCadence animateIn={animateIn} className={className} {...actions} />;
  return <HeroLumen animateIn={animateIn} className={className} {...actions} />;
}

const variantOptions = [
  { value: "centered", label: "Screenshot" },
  { value: "split", label: "Workflow" },
  { value: "minimal", label: "Mesh" },
];

/**
 * Preview: the hero, full screen, with a small glass switch floating over its top edge. The switch takes no space of its own,
 * so every design fills the screen exactly. Switching replays the entrance.
 */
export function HeroSectionBlock({ variant: initial = "centered" }: { variant?: HeroSectionVariant }) {
  const [variant, setVariant] = useState<HeroSectionVariant>(initial);
  return <div className={styles.preview}>
    <HeroSection key={variant} variant={variant} />
    <div className={styles.switcher}>
      <SegmentedControl label="Hero design" options={variantOptions} value={variant} onValueChange={value => setVariant(value as HeroSectionVariant)} className={styles.switch} />
    </div>
  </div>;
}

export default HeroSectionBlock;
