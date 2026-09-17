/**
 * Which language the dashboard speaks, and how it decided.
 *
 * Paraglide is compiled with the `globalVariable` strategy, which means it
 * never guesses: nothing is read from a cookie, a URL path or the browser
 * until this module says so. `initLocale()` is the one place that decides, and
 * it runs once in main.ts before the app mounts, so every message function and
 * every `Intl` formatter sees the same answer from the first frame.
 *
 * The negotiation itself is a pure function — `resolveLocale` takes every
 * input as an argument and touches no global — because "does pt-PT fall back
 * to pt-BR or to English?" is a question worth being able to test.
 */
import { assertIsLocale, baseLocale, getLocale, locales, setLocale } from '../paraglide/runtime'

/** Where a chosen language is remembered between visits. */
export const STORAGE_KEY = 'loot.lang'

/** The `?lang=` override, which also persists what it selects. */
export const QUERY_KEY = 'lang'

/** Everything `resolveLocale` is allowed to look at. */
export interface LocaleInput {
  /** `?lang=xx` from the URL, or null. */
  query?: string | null
  /** What localStorage remembers, or null. */
  stored?: string | null
  /** What the server injected into `<html data-loot-lang>`, or null. */
  injected?: string | null
  /** `navigator.languages`, most preferred first. */
  navigatorLanguages?: readonly string[]
  /** The locales this build was compiled with. */
  known?: readonly string[]
}

/**
 * The known locale a single BCP-47 tag should be served as, or undefined.
 *
 * Three tries, narrowing: the tag itself ("pt-BR" → "pt-BR"), then the bare
 * language if we ship it ("en-GB" → "en"), then any locale in the same
 * language ("pt-PT" → "pt-BR", "zh-TW" → "zh-Hans"). The middle step is what
 * keeps a regional English on plain `en` rather than on the first `en-*`
 * variant that happens to be in the list.
 */
function matchTag(tag: string, known: readonly string[]): string | undefined {
  const wanted = tag.trim().toLowerCase()
  if (!wanted) return undefined

  const exact = known.find((k) => k.toLowerCase() === wanted)
  if (exact) return exact

  const primary = wanted.split('-')[0]
  const bare = known.find((k) => k.toLowerCase() === primary)
  if (bare) return bare

  return known.find((k) => k.toLowerCase().split('-')[0] === primary)
}

/** True when `tag` is one of the locales this build was compiled with. */
function known(tag: string | null | undefined, list: readonly string[]): string | undefined {
  if (!tag) return undefined
  const lower = tag.trim().toLowerCase()
  return list.find((k) => k.toLowerCase() === lower)
}

/**
 * Which language to speak, given everything we know.
 *
 * In order: an explicit `?lang=`, then a remembered choice, then whatever the
 * server was configured with, then the browser's own preference list, then
 * English. Only the last two are negotiated; the first three are either a
 * locale we ship or ignored outright, because a request for a language this
 * build does not have is better answered in English than in a near miss.
 */
export function resolveLocale(input: LocaleInput): string {
  const list = input.known ?? locales

  const fromQuery = known(input.query, list)
  if (fromQuery) return fromQuery

  const fromStorage = known(input.stored, list)
  if (fromStorage) return fromStorage

  const injected = (input.injected ?? '').trim()
  if (injected && injected.toLowerCase() !== 'auto') {
    const fromServer = known(injected, list)
    if (fromServer) return fromServer
  }

  for (const tag of input.navigatorLanguages ?? []) {
    const negotiated = matchTag(tag, list)
    if (negotiated) return negotiated
  }

  return baseLocale
}

/** Reads the remembered language, or null when storage is unavailable. */
function readStored(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

/** Remembers a language, best effort: private browsing may refuse. */
function writeStored(locale: string): void {
  try {
    localStorage.setItem(STORAGE_KEY, locale)
  } catch {
    // Nothing to do — the choice simply will not survive the tab.
  }
}

/**
 * Decides the language and installs it. Call once, before mounting.
 *
 * `setLocale(_, { reload: false })` is the right call here and nowhere else:
 * Loot is a fully client-rendered app with no localized URLs, so there is no
 * document to navigate to — and reloading would throw away the socket, the
 * feed and any chest mid-cascade.
 */
export function initLocale(): string {
  const params = typeof location === 'undefined' ? null : new URLSearchParams(location.search)
  const query = params?.get(QUERY_KEY) ?? null

  const locale = resolveLocale({
    query,
    stored: readStored(),
    injected: document.documentElement.dataset.lootLang ?? null,
    navigatorLanguages: navigator.languages ?? (navigator.language ? [navigator.language] : []),
  })

  // Only an explicit `?lang=` is worth remembering. Persisting a negotiated
  // guess would freeze it: change your browser's language and Loot would keep
  // speaking the one it picked on your first ever visit.
  if (query && known(query, locales)) writeStored(locale)

  // assertIsLocale only narrows the type: resolveLocale already answered with
  // one of `locales` or with the base locale, so it cannot throw here.
  setLocale(assertIsLocale(locale), { reload: false })
  document.documentElement.lang = locale
  return locale
}

/** The language everything is currently rendered in. */
export function currentLocale(): string {
  return getLocale()
}
