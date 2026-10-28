import { signal, computed, effect } from "@preact/signals-react";

export type Locale = "zh-CN" | "en";
export function normalizeLocale(value: string | null | undefined): Locale {
  return value?.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
}
function browserLocale(): Locale {
  if (typeof navigator === "undefined") return "en";
  const languages = navigator.languages?.length
    ? navigator.languages
    : [navigator.language];
  const supported = languages.find(language => /^(zh|en)(-|$)/i.test(language));
  return normalizeLocale(supported);
}
export type LanguagePreference = "system" | Locale;
const preferenceKey = "nodedance.locale";
function savedLanguagePreference(): LanguagePreference {
  try {
    const saved = localStorage.getItem(preferenceKey);
    if (saved === "en" || saved === "zh-CN") return saved;
  } catch { /* Browser preferences still work without local storage. */ }
  return "system";
}
const browserLanguage = signal<Locale>(browserLocale());
export const languagePreference = signal<LanguagePreference>(savedLanguagePreference());
export const locale = computed<Locale>(() =>
  languagePreference.value === "system" ? browserLanguage.value : languagePreference.value,
);

const catalogs: Partial<Record<Locale, Record<string, string>>> = {};
const catalogLoads = new Map<Locale, Promise<void>>();
const catalogEpoch = signal(0);
const loaders: Record<Locale, () => Promise<{ default: Record<string, string> }>> = {
  en: () => import("./locales/en.json"),
  "zh-CN": () => import("./locales/zh-CN.json"),
};

const diagnosticKeys = new Map<string, string>();
const presentationKeys = new Map<string, string>();
const presentationTemplates: { key: string; slots: string[]; pattern: RegExp; literal: number }[] = [];
function absorbCatalog(owner: Locale, catalog: Record<string, string>) {
  if (owner === "en") {
    for (const [key, value] of Object.entries(catalog)) {
      if (key.startsWith("api.")) diagnosticKeys.set(value, key);
    }
  }
  for (const [key, message] of Object.entries(catalog)) {
    if (!/\{\d+\}/.test(message)) presentationKeys.set(message, key);
    const slots = [...message.matchAll(/(?<!\{)\{(\d+)\}(?!\})/g)];
    if (!slots.length) continue;
    const escape = (value: string) =>
      value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    let pattern = "^";
    let at = 0;
    for (const slot of slots) {
      pattern += escape(message.slice(at, slot.index)) + "([\\s\\S]*?)";
      at = slot.index + slot[0].length;
    }
    pattern += escape(message.slice(at)) + "$";
    presentationTemplates.push({
      key,
      slots: slots.map((slot) => slot[1]!),
      pattern: new RegExp(pattern),
      literal: message.length - slots.reduce((total, slot) => total + slot[0].length, 0),
    });
  }
  presentationTemplates.sort((a, b) => b.literal - a.literal);
}

export function loadCatalog(target: Locale): Promise<void> {
  const existing = catalogLoads.get(target);
  if (existing) return existing;
  const load = loaders[target]().then((loaded) => {
    const catalog = (loaded as { default?: Record<string, string> }).default
      ?? (loaded as unknown as Record<string, string>);
    catalogs[target] = catalog;
    absorbCatalog(target, catalog);
    catalogEpoch.value++;
  }).catch(() => {
    catalogLoads.delete(target);
  });
  catalogLoads.set(target, load);
  return load;
}

export function initI18n(): Promise<void> {
  return loadCatalog(locale.value);
}

export function setLanguagePreference(value: LanguagePreference) {
  languagePreference.value = value;
  try { localStorage.setItem(preferenceKey, value); } catch { /* In-memory preference. */ }
  void loadCatalog(locale.value);
}
if (typeof window !== "undefined") {
  window.addEventListener("languagechange", () => {
    browserLanguage.value = browserLocale();
    void loadCatalog(locale.value);
  });
}

effect(() => {
  if (typeof document !== "undefined")
    document.documentElement.lang = locale.value;
});

export function t(key: string, values: Record<string, unknown> = {}): string {
  void catalogEpoch.value;
  const message = catalogs[locale.value]?.[key] ?? catalogs.en?.[key] ?? catalogs["zh-CN"]?.[key] ?? key;
  // Numeric slots cannot consume webhook template fields such as {{event}}.
  return message.replace(/(?<!\{)\{(\d+)\}(?!\})/g, (token, name: string) =>
    Object.hasOwn(values, name) ? String(values[name]) : token,
  );
}

// Public Core diagnostics are translated at presentation time. Unknown host,
// Docker, or third-party diagnostics remain intact for troubleshooting.
export function diagnostic(message: string): string {
  void catalogEpoch.value;
  const key = diagnosticKeys.get(message);
  return key ? t(key) : message;
}

// Localized notices may have been created before a language switch. Translate
// only presentation messages here; never apply this to names or terminal/file data.
export function display(message: string | null | undefined): string {
  if (!message) return "";
  void catalogEpoch.value;
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

void loadCatalog(locale.value);
