import { systemLogo, systemLogos } from "../system-logo";
import { t } from "../i18n";
export default function OsLogo({ platform, os }: { platform?: string | null; os?: string | null }) {
  const key = systemLogo(platform, os), logo = key ? systemLogos[key] : null;
  const label = logo?.name ?? t("os.unknown");
  return <svg data-os-logo={key ?? "unknown"} viewBox="0 0 24 24" role="img" aria-label={label}
    style={{color: `var(--${logo?.tone ?? "muted"})`}} fill="currentColor">
    <title>{label}</title>
    <path d={logo?.path ?? "M3 3h18v14H3V3zm2 2v10h14V5H5zm5 14h4v2h4v1H6v-1h4v-2z"}/>
  </svg>;
}
