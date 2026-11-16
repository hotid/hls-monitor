// Interface texts, with vue-i18n. A language is a folder of JSON files,
// src/locales/<code>/<namespace>.json; a text is addressed as
// "namespace.key". Adding a folder adds the language — see I18N.md.

import { createI18n } from 'vue-i18n'

type Messages = Record<string, Record<string, string>>

const files = import.meta.glob<Record<string, string>>('./locales/*/*.json', { eager: true, import: 'default' })
const messages: Record<string, Messages> = {}
for (const [path, entries] of Object.entries(files)) {
  const [, , code = '', file = ''] = path.split('/')
  ;(messages[code] ??= {})[file.replace(/\.json$/, '')] = entries
}

/** Languages the interface can be shown in. */
export const locales: string[] = Object.keys(messages).sort()

/** Shown when the chosen language lacks a text, and to browsers that prefer none of ours. */
const fallback = 'en'

function initial(): string {
  try {
    const saved = localStorage.getItem('locale')
    if (saved && locales.includes(saved)) return saved
  } catch {
    /* storage is not available */
  }
  // The browser's languages in order of preference: "de-AT" also matches "de".
  for (const want of navigator.languages ?? [navigator.language]) {
    const code = want.toLowerCase().split('-')[0] ?? ''
    if (locales.includes(code)) return code
  }
  return fallback
}

/**
 * Which of the forms "one | few | many" suits a number. Forms are written
 * in the order Intl.PluralRules lists the categories of the language; a
 * language may leave out the trailing ones (Russian omits "other", which
 * only fractions use).
 */
function pluralRule(code: string) {
  const rules = new Intl.PluralRules(code)
  const categories = rules.resolvedOptions().pluralCategories
  return (choice: number, forms: number) => Math.min(categories.indexOf(rules.select(choice)), forms - 1)
}

export const i18n = createI18n({
  legacy: false,
  locale: initial(),
  fallbackLocale: fallback,
  messages,
  pluralRules: Object.fromEntries(locales.map((code) => [code, pluralRule(code)])),
  missingWarn: import.meta.env.DEV,
  fallbackWarn: false,
})

/**
 * The text for a key: t('stream.address'); with parameters
 * t('table.nOfTotal', { n: 1, total: 3 }); the word form for a number
 * t('common.streams', 3).
 */
export const t = i18n.global.t

/** The current language; assign through setLocale. */
export const locale = i18n.global.locale

document.documentElement.lang = locale.value

export function setLocale(code: string) {
  if (!locales.includes(code)) return
  locale.value = code
  document.documentElement.lang = code
  try {
    localStorage.setItem('locale', code)
  } catch {
    /* storage is not available */
  }
}

/** Locale for dates and times; British English for a 24-hour clock. */
export function dateLocale(): string {
  return locale.value === 'en' ? 'en-GB' : locale.value
}

/**
 * A table of texts kept outside components: built on first use and again
 * after the language changes. Reading it in a template makes the template
 * follow the language.
 */
export function perLocale<T>(build: () => T): () => T {
  let made: { locale: string; value: T } | undefined
  return () => {
    const now = locale.value
    if (made?.locale !== now) made = { locale: now, value: build() }
    return made.value
  }
}
