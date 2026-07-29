// Device-local preferences (language, time format). Stored in localStorage,
// not synced: they describe this device, not the account.

export const LOCALES = ["en", "ru", "es", "de", "fr", "zh"] as const;
export type Locale = (typeof LOCALES)[number];
export type LocaleSetting = "auto" | Locale;
export type TimeFormatSetting = "auto" | "12" | "24";

const STORAGE_KEY = "worktime-prefs";

interface Prefs {
  locale: LocaleSetting;
  timeFormat: TimeFormatSetting;
}

function loadPrefs(): Prefs {
  const prefs: Prefs = { locale: "auto", timeFormat: "auto" };
  try {
    const raw = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "{}") as Partial<Prefs>;
    if (raw.locale === "auto" || (LOCALES as readonly string[]).includes(raw.locale ?? "")) {
      prefs.locale = raw.locale as LocaleSetting;
    }
    if (raw.timeFormat === "auto" || raw.timeFormat === "12" || raw.timeFormat === "24") {
      prefs.timeFormat = raw.timeFormat;
    }
  } catch {
    // Corrupt storage falls back to defaults.
  }
  return prefs;
}

export const prefs = $state(loadPrefs());

export function updatePrefs(patch: Partial<Prefs>): void {
  Object.assign(prefs, patch);
  localStorage.setItem(STORAGE_KEY, JSON.stringify({ locale: prefs.locale, timeFormat: prefs.timeFormat }));
}

// The UI language: explicit choice, or the closest browser language, or English.
export function effectiveLocale(): Locale {
  if (prefs.locale !== "auto") return prefs.locale;
  const browser = (navigator.language ?? "en").toLowerCase();
  for (const locale of LOCALES) {
    if (browser.startsWith(locale)) return locale;
  }
  return "en";
}

// BCP 47 tag for date/time formatting; undefined lets the browser decide (auto).
export function formattingLocale(): string | undefined {
  if (prefs.locale === "auto") return undefined;
  return prefs.locale === "zh" ? "zh-CN" : prefs.locale;
}

// hourCycle option for toLocaleTimeString; undefined follows the locale default.
// h23 (not hour12: false) avoids the "24:00" midnight quirk in some locales.
export function hourCycle(): "h12" | "h23" | undefined {
  if (prefs.timeFormat === "auto") return undefined;
  return prefs.timeFormat === "12" ? "h12" : "h23";
}
