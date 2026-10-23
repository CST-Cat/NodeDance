"use client";

import { MotionConfig, motion } from "motion/react";
import { ActionLink } from "./hero-content";
import type { HeroAction } from "./hero-content";
import { HeroMesh } from "./hero-mesh";
import type { HeroMeshPoint } from "./hero-mesh";
import { heroGroup, heroRise } from "./hero-motion";
import shell from "./hero-section.module.css";
import styles from "./hero-cadence.module.css";

export interface HeroCadenceProps {
  primaryAction?: HeroAction;
  secondaryAction?: HeroAction;
  animateIn?: boolean;
  className?: string;
}

/** Six points, authored the way the Gradient mesh editor saves them. Colors live in CSS, one palette per theme. */
const MESH: HeroMeshPoint[] = [
  { x: .1, y: .88, spread: .72 },
  { x: .86, y: .12, spread: .66 },
  { x: .16, y: .14, spread: .56 },
  { x: .62, y: .62, spread: .5 },
  { x: .94, y: .92, spread: .48 },
  { x: .44, y: .3, spread: .34 },
];

/** Sample customers. The marks are the brands' own icons, drawn in the text color. */
const BRANDS = [
  { name: "Linear", logo: "/block-logos/linear.svg" },
  { name: "Vercel", logo: "/block-logos/vercel.svg" },
  { name: "Raycast", logo: "/block-logos/raycast.svg" },
  { name: "Notion", logo: "/block-logos/notion.svg" },
  { name: "Figma", logo: "/block-logos/figma.svg" },
  { name: "Stripe", logo: "/block-logos/stripe.svg" },
  { name: "Loom", logo: "/block-logos/loom.svg" },
];

/** Minimal hero, one full screen: large type over a slowly drifting mesh gradient with grain, and the teams that use the product along the bottom edge. */
export function HeroCadence({ primaryAction = { label: "Download for Mac", doneLabel: "Download started" }, secondaryAction = { label: "Try it on the web", doneLabel: "Opening Cadence" }, animateIn = true, className }: HeroCadenceProps) {
  const item = heroRise;
  // Reduced motion: Motion skips every transform (rise, zoom, tilt entrance); opacity still fades briefly.
  return <MotionConfig reducedMotion="user"><section className={[shell.hero, shell.screen, styles.hero, className].filter(Boolean).join(" ")}>
    <HeroMesh className={styles.mesh} points={MESH} grain={.55} speed={.45} />
    <motion.div className={styles.inner} variants={heroGroup} initial={animateIn ? "hidden" : false} animate="shown">
      <motion.h1 variants={item} className={[shell.title, styles.title].join(" ")}>Your week, planned before Monday</motion.h1>
      <div className={styles.row}>
        <motion.p variants={item} className={[shell.description, styles.description].join(" ")}>Cadence reads your tasks, meetings and focus goals, then books time for the work that matters. When plans change, it moves everything for you.</motion.p>
        <motion.div variants={item} className={styles.cta}>
          <div className={styles.actions}>
            <ActionLink action={primaryAction} kind="primary" />
            <ActionLink action={secondaryAction} kind="secondary" />
          </div>
          <p className={styles.fine}>Free for personal use. Teams from $8 a seat.</p>
        </motion.div>
      </div>
      <motion.div variants={item} className={styles.brands}>
        <p>Teams at these companies plan with Cadence</p>
        <ul aria-label="Sample customers">
          {BRANDS.map(brand => <li key={brand.name}><i style={{ maskImage: `url(${brand.logo})`, WebkitMaskImage: `url(${brand.logo})` }} aria-hidden="true" />{brand.name}</li>)}
        </ul>
      </motion.div>
    </motion.div>
  </section></MotionConfig>;
}

export default HeroCadence;
