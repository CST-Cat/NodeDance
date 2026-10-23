import { Globe } from "lucide-react";
import { countryFlags } from "../country-flags";
import { locale, t } from "../i18n";
import type { AgentNode } from "../api";

export default function CountryLogo({
  country,
}: {
  country?: AgentNode["country"];
}) {
  const code =
    country?.status === "known" ? (country.code?.toLowerCase() ?? "") : "";
  if (!countryFlags.has(code))
    return (
      <Globe
        className="product-logo"
        data-country-logo="unknown"
        role="img"
        aria-label={t("country.unknown")}
      />
    );
  const name =
    new Intl.DisplayNames([locale.value], { type: "region" }).of(
      code.toUpperCase(),
    ) ??
    country?.name ??
    code.toUpperCase();
  return (
    <img
      className="product-logo"
      data-country-logo={code}
      src={`/flags/${code}.svg?v=twemoji-b6b55fe`}
      alt={name}
      title={name}
      width={28}
      height={28}
    />
  );
}
