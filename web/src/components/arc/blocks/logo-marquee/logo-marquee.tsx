"use client";

import { useId, useState } from "react";
import { Pause, Play } from "lucide-react";
import { Button } from "../../button/button";
import styles from "./logo-marquee.module.css";

export interface LogoMarqueeBrand {
  name: string;
  /** Monochrome SVG, drawn as a mask in the text color. */
  icon: string;
  /** Single brand color for the mark in color tone. Leave out for brands whose mark is black or white. */
  color?: string;
  /** Full color SVG for multicolor marks, used in color tone instead of `icon`. */
  colorIcon?: string;
}

export type LogoMarqueeTone = "color" | "mono";

export interface LogoMarqueeProps {
  title?: string;
  description?: string;
  brands?: LogoMarqueeBrand[];
  /** `color` shows each mark in its official colors; `mono` draws every mark in the text color. */
  tone?: LogoMarqueeTone;
}

const exampleBrands: LogoMarqueeBrand[] = [
  { name: "Figma", icon: "/block-logos/figma.svg", colorIcon: "/block-logos/figma-color.svg" },
  { name: "Linear", icon: "/block-logos/linear.svg", color: "#5E6AD2" },
  { name: "Notion", icon: "/block-logos/notion.svg" },
  { name: "Slack", icon: "/block-logos/slack.svg", colorIcon: "/block-logos/slack-color.svg" },
  { name: "GitHub", icon: "/block-logos/github.svg" },
  { name: "Stripe", icon: "/block-logos/stripe.svg", color: "#635BFF" },
  { name: "Vercel", icon: "/block-logos/vercel.svg" },
];

function BrandMark({ brand, tone }: { brand: LogoMarqueeBrand; tone: LogoMarqueeTone }) {
  if (tone === "color" && brand.colorIcon) {
    // eslint-disable-next-line @next/next/no-img-element
    return <img className={styles.brandMark} src={brand.colorIcon} alt="" width={22} height={22} draggable={false} />;
  }
  return (
    <span
      className={styles.brandMark}
      style={{ maskImage: `url("${brand.icon}")`, WebkitMaskImage: `url("${brand.icon}")`, color: tone === "color" ? brand.color : undefined }}
      aria-hidden="true"
    />
  );
}

function BrandList({ brands, tone, duplicate = false }: { brands: LogoMarqueeBrand[]; tone: LogoMarqueeTone; duplicate?: boolean }) {
  return (
    <ul className={styles.brandList} aria-label={duplicate ? undefined : "Illustrative tool logos"} aria-hidden={duplicate || undefined}>
      {brands.map((brand, index) => (
        <li className={styles.brand} key={`${brand.name}-${index}`}>
          <BrandMark brand={brand} tone={tone} />
          <span className={styles.brandName}>{brand.name}</span>
        </li>
      ))}
    </ul>
  );
}

export function LogoMarquee({
  title = "Good work moves between tools",
  description = "From the first idea to the final handoff, a familiar set of tools stays close to the work.",
  brands = exampleBrands,
  tone = "color",
}: LogoMarqueeProps) {
  const titleId = useId();
  const [paused, setPaused] = useState(false);

  return (
    <section className={styles.section} data-tone={tone} aria-labelledby={titleId}>
      <div className={styles.intro}>
        <h2 id={titleId}>{title}</h2>
        <p>{description}</p>
      </div>

      <div className={styles.marquee} role="region" aria-label="Illustrative tool logos">
        <div className={`${styles.track}${paused ? ` ${styles.paused}` : ""}`}>
          <BrandList brands={brands} tone={tone} />
          <BrandList brands={brands} tone={tone} duplicate />
        </div>
      </div>

      <div className={styles.footer}>
        <Button
          variant="ghost"
          size="sm"
          className={styles.motionButton}
          aria-label={paused ? "Play logo motion" : "Pause logo motion"}
          onClick={() => setPaused((value) => !value)}
        >
          {paused ? <><Play size={16} strokeWidth={1.75} aria-hidden="true" />Play</> : <><Pause size={16} strokeWidth={1.75} aria-hidden="true" />Pause</>}
        </Button>
        <span className={styles.motionStatus} aria-live="polite">
          {paused ? "Motion paused" : ""}
        </span>
      </div>
    </section>
  );
}

export default LogoMarquee;
