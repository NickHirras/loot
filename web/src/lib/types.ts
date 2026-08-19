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
