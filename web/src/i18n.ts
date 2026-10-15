import { signal, effect } from "@preact/signals-react";
import en from "./locales/en.json";
import zhCN from "./locales/zh-CN.json";

export type Locale = "zh-CN" | "en";
const storageKey = "nodedance.language";
export function normalizeLocale(value: string | null | undefined): Locale {
  return value?.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
}
function initialLocale(): Locale {
  try {
    const saved = localStorage.getItem(storageKey);
    if (saved === "zh-CN" || saved === "en") return saved;
  } catch {
    /* Language selection also works when storage is disabled. */
  }
  return normalizeLocale(
    typeof navigator === "undefined" ? "en" : navigator.language,
  );
}
export const locale = signal<Locale>(initialLocale());
export function setLocale(value: Locale) {
  locale.value = value;
}
effect(() => {
  const value = locale.value;
  if (typeof document !== "undefined")
    document.documentElement.lang = value === "en" ? "en" : "zh-CN";
  try {
    localStorage.setItem(storageKey, value);
  } catch {
    /* Private browsing. */
  }
});

const catalogs: Record<Locale, Record<string, string>> = { en, "zh-CN": zhCN };
export function t(key: string, values: Record<string, unknown> = {}): string {
  const message = catalogs[locale.value][key] ?? key;
  // Numeric slots cannot consume webhook template fields such as {{event}}.
  return message.replace(/(?<!\{)\{(\d+)\}(?!\})/g, (token, name: string) =>
    Object.hasOwn(values, name) ? String(values[name]) : token,
  );
}

const diagnosticKeys = new Map(
  Object.entries(en)
    .filter(([key]) => key.startsWith("api."))
    .map(([key, value]) => [value, key]),
);
// Public Core diagnostics are translated at presentation time. Unknown host,
// Docker, or third-party diagnostics remain intact for troubleshooting.
export function diagnostic(message: string): string {
  const key = diagnosticKeys.get(message);
  return key ? t(key) : message;
}

const presentationKeys = new Map<string, string>();
for (const catalog of Object.values(catalogs)) {
  for (const [key, message] of Object.entries(catalog)) {
    if (!/\{\d+\}/.test(message)) presentationKeys.set(message, key);
  }
}
const presentationTemplates = Object.values(catalogs).flatMap((catalog) =>
  Object.entries(catalog).flatMap(([key, message]) => {
    const slots = [...message.matchAll(/(?<!\{)\{(\d+)\}(?!\})/g)];
    if (!slots.length) return [];
    const escape = (value: string) =>
      value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    let pattern = "^";
    let at = 0;
    for (const slot of slots) {
      pattern += escape(message.slice(at, slot.index)) + "([\\s\\S]*?)";
      at = slot.index + slot[0].length;
    }
    pattern += escape(message.slice(at)) + "$";
    return [
      {
        key,
        slots: slots.map((slot) => slot[1]!),
        pattern: new RegExp(pattern),
      },
    ];
  }),
);
// Localized notices may have been created before a language switch. Translate
// only presentation messages here; never apply this to names or terminal/file data.
export function display(message: string | null | undefined): string {
  if (!message) return "";
  const key = presentationKeys.get(message);
  if (key) return t(key);
  for (const template of presentationTemplates) {
    const match = template.pattern.exec(message);
    if (match)
      return t(
        template.key,
        Object.fromEntries(
          template.slots.map((slot, i) => [slot, match[i + 1]]),
        ),
      );
  }
  return message;
}
