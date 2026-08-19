/** Rarity ladder, mirroring internal/core. */
export const RARITIES = ['common', 'uncommon', 'rare', 'epic', 'legendary', 'cursed'] as const

export type Rarity = (typeof RARITIES)[number]

/** How noteworthy a rarity is; `cursed` sits above the ladder as "bad news". */
export const RARITY_RANK: Record<Rarity, number> = {
  common: 0,
  uncommon: 1,
  rare: 2,
  epic: 3,
  legendary: 4,
  cursed: 5,
}

/** Anything at or above `rare` gets a glow, particles and a terminal bell. */
export function isFlashy(rarity: Rarity): boolean {
  return rarity === 'cursed' || RARITY_RANK[rarity] >= RARITY_RANK.rare
}

/** A drop joined with the event fields the feed renders. */
export interface Drop {
  id: string
  event_id: string
  rarity: Rarity
  title: string
  subtitle: string
  xp: number
  created_at: string
  /** The day whose chest held this drop, or absent for an immediate drop. */
  chest_date?: string
  /** When the chest holding this drop was opened; absent while it waits. */
  revealed_at?: string | null

  source: string
  kind: string
  app: string
  country: string
  amount: number
  /** `amount` converted into the dashboard's display currency at ingest. */
  amount_base?: number
  currency: string
  quantity: number
  /** Business day (YYYY-MM-DD) the event belongs to. */
  day?: string
  occurred_at: string
}

/** One unopened daily chest, as returned by GET /api/chest. */
export interface ChestSummary {
  date: string
  count: number
  xp: number
  by_rarity: Record<string, number>
}

export interface Stats {
  total_drops: number
  total_events: number
  total_xp: number
  by_rarity: Record<Rarity, number>
  by_source: Record<string, number>
  countries: string[]
  countries_count: number
  /** How many drops are waiting inside unopened chests. */
  unrevealed_count: number
  /** The days those chests are for, oldest first. */
  chest_dates: string[]
  display_currency: string
  dev: boolean
  /** True when this Loot is running on synthetic demo data. */
  demo: boolean
  listeners: number
}

export interface SourceInfo {
  name: string
  mode: 'poll' | 'webhook'
  poll_interval?: string
  last_poll_at: string | null
  last_error: string
  events: number
}

/**
 * Websocket envelope from GET /ws.
 *
 *   {"type":"hello"}                      on connect
 *   {"type":"drop","drop":…,"event":…}    a drop landed
 *   {"type":"drop","chest":true,…}        a drop revealed by opening a chest
 *   {"type":"chest","chests":[…]}         the set of unopened chests changed
 */
export interface WSMessage {
  type: 'hello' | 'drop' | 'chest'
  drop?: Pick<
    Drop,
    'id' | 'event_id' | 'rarity' | 'title' | 'subtitle' | 'xp' | 'created_at' | 'chest_date' | 'revealed_at'
  >
  event?: Record<string, unknown>
  /** True when this drop arrived because a chest was opened. */
  chest?: boolean
  /** Present on `chest` messages: the chests still waiting. */
  chests?: ChestSummary[]
}

/** Turns an ISO 3166-1 alpha-2 code into its flag emoji. */
export function flagEmoji(iso2: string): string {
  if (!/^[A-Za-z]{2}$/.test(iso2)) return ''
  return String.fromCodePoint(
    ...iso2
      .toUpperCase()
      .split('')
      .map((c) => 0x1f1e6 + c.charCodeAt(0) - 65),
  )
}

/** Compact relative time, e.g. "just now", "4m", "3h", "2d". */
export function timeAgo(iso: string): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return ''

  const seconds = Math.floor((Date.now() - then) / 1000)
  if (seconds < 10) return 'just now'
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

/** Formats an amount with its currency, or "" when there is no money involved. */
export function money(amount: number, currency: string): string {
  if (!amount) return ''
  try {
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: currency || 'USD',
      maximumFractionDigits: 2,
    }).format(amount)
  } catch {
    return `${amount.toFixed(2)} ${currency}`.trim()
  }
}

// --------------------------------------------------------------- vault types

/** The ranges GET /api/vault/summary accepts, and the labels the picker shows. */
export const VAULT_RANGES = ['7d', '30d', '90d', '365d'] as const

export type VaultRange = (typeof VAULT_RANGES)[number]

export interface VaultWindow {
  from: string
  to: string
  days: number
}

export interface VaultTotals {
  revenue_base: number
  units: number
  refunds: number
  drops: number
  events: number
  countries: number
}

/** One day of the (zero-filled) series; `by_source` maps source to revenue. */
export interface VaultPoint {
  day: string
  revenue_base: number
  units: number
  by_source: Record<string, number>
}

/** The shared body of every breakdown row. `share` is a fraction of revenue. */
export interface VaultSlice {
  revenue_base: number
  units: number
  share: number
}

export type SourceSlice = VaultSlice & { source: string }
export type AppSlice = VaultSlice & { app: string }
export type CountrySlice = VaultSlice & { country: string }

export interface VaultSubscriptions {
  active: number | null
  as_of: string | null
}

/** Sighted-but-not-banked numbers: RevenueCat's realtime view of today. */
export interface VaultRealtime {
  revenuecat_purchases_today: number
  revenuecat_amount_base_today: number
}

/** The whole of GET /api/vault/summary. */
export interface VaultSummary {
  display_currency: string
  range: VaultWindow
  totals: VaultTotals
  prev_totals: VaultTotals
  series: VaultPoint[]
  by_source: SourceSlice[]
  by_app: AppSlice[]
  by_country: CountrySlice[]
  subscriptions: VaultSubscriptions
  realtime: VaultRealtime
}

// ----------------------------------------------------------------- formatting

/** Currency formatter, cached per currency: Intl objects are not cheap. */
const currencyFormatters = new Map<string, Intl.NumberFormat>()

function currencyFormatter(currency: string, fractionDigits: number): Intl.NumberFormat {
  const key = `${currency}/${fractionDigits}`
  let fmt = currencyFormatters.get(key)
  if (!fmt) {
    try {
      fmt = new Intl.NumberFormat(undefined, {
        style: 'currency',
        currency: currency || 'USD',
        minimumFractionDigits: fractionDigits,
        maximumFractionDigits: fractionDigits,
      })
    } catch {
      fmt = new Intl.NumberFormat(undefined, {
        minimumFractionDigits: fractionDigits,
        maximumFractionDigits: fractionDigits,
      })
    }
    currencyFormatters.set(key, fmt)
  }
  return fmt
}

/** Formats an amount in the display currency, always (unlike `money`). */
export function currency(amount: number, code: string, fractionDigits = 2): string {
  return currencyFormatter(code, fractionDigits).format(amount ?? 0)
}

/** Whole-unit currency, for axis ticks and stat tiles that would look noisy. */
export function currencyCompact(amount: number, code: string): string {
  return currency(amount, code, Math.abs(amount) >= 1000 ? 0 : 2)
}

const integerFormat = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 })

export function integer(value: number): string {
  return integerFormat.format(value ?? 0)
}

const percentFormat = new Intl.NumberFormat(undefined, {
  style: 'percent',
  maximumFractionDigits: 1,
})

/** Formats a 0..1 share as a percentage. */
export function percent(share: number): string {
  return percentFormat.format(share ?? 0)
}

/** The direction of a period-over-period change; `flat` covers 0 and "no basis". */
export type Direction = 'up' | 'down' | 'flat'

export interface Delta {
  direction: Direction
  /** Formatted percentage, or "" when the previous period was empty. */
  label: string
}

/**
 * Percentage change against the previous period. With nothing to compare
 * against, the delta is deliberately neutral rather than "+100%".
 */
export function delta(current: number, previous: number): Delta {
  if (!previous) {
    if (!current) return { direction: 'flat', label: '' }
    return { direction: 'flat', label: 'new' }
  }
  const change = (current - previous) / Math.abs(previous)
  if (Math.abs(change) < 0.0005) return { direction: 'flat', label: '0%' }
  return {
    direction: change > 0 ? 'up' : 'down',
    label: `${change > 0 ? '+' : ''}${percentFormat.format(change)}`,
  }
}

/** "Aug 18" style day label for chart axes and tooltips. */
export function dayLabel(day: string, withYear = false): string {
  const parsed = new Date(`${day}T00:00:00Z`)
  if (Number.isNaN(parsed.getTime())) return day
  return parsed.toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
    year: withYear ? 'numeric' : undefined,
    timeZone: 'UTC',
  })
}

/** One row of a vault breakdown panel (by source, app or country). */
export interface BreakdownRow {
  /** Stable identity, used as the key and for the bar color. */
  key: string
  label: string
  /** Rendered before the label; the country panel puts a flag here. */
  prefix?: string
  revenue_base: number
  units: number
  share: number
}

// -------------------------------------------------------------- hearth types

/** One rung of the settlement ladder, mirroring internal/core/hearth.go. */
export interface HearthTier {
  name: string
  index: number
  /** Inclusive lower bound on population / largest country's population. */
  min_share: number
}

/** Where the account stands on the era ladder. */
export interface HearthEra {
  name: string
  index: number
  /** Total XP at which this era began. */
  min_xp: number
  /** XP earned inside this era. */
  xp: number
  /** The era after this one, or "" at the top of the ladder. */
  next_name: string
  /** Total XP the next era starts at; 0 at the top. */
  next_xp: number
  /** XP still owed to the next era; 0 at the top. */
  to_next: number
  /** 0..1 through the current era. */
  progress: number
}

/** One settlement: a country, its people and its money. */
export interface HearthCountry {
  country: string
  population: number
  revenue_base: number
  /** Business days: the first and latest event from this country. */
  first_seen: string
  last_seen: string
  drops: number
  tier: HearthTier
  /** Population as a fraction of the largest country's. */
  share: number
}

/** Events with no country at all — Flathub reports installs but not where. */
export interface HearthUnknown {
  population: number
  revenue_base: number
}

/** One row of the arrivals ticker. */
export interface HearthDrop {
  id: string
  rarity: Rarity
  country: string
  title: string
  subtitle: string
  kind: string
  created_at: string
}

/** The whole of GET /api/hearth. */
export interface Hearth {
  /** ISO2 of the capital: home_country, or the biggest settlement. */
  capital: string
  display_currency: string
  era: HearthEra
  total_xp: number
  /** Totals across every country; the unknown bucket is counted apart. */
  population: number
  revenue_base: number
  unknown: HearthUnknown
  countries: HearthCountry[]
  tiers: HearthTier[]
  recent: HearthDrop[]
}
