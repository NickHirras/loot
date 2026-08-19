import { fetchHearth } from './api'
import { loot } from './state.svelte'
import type { Drop, Hearth, HearthCountry, HearthDrop } from './types'

/** How often the Hearth re-reads the aggregate while its page is open. */
const REFRESH_MS = 60_000
/** A burst of drops (a chest cascade) should cause one refetch, not thirty. */
const STALE_DEBOUNCE_MS = 2_000
/** How many arrivals the ticker keeps. Matches what the API returns. */
const MAX_RECENT = 30

/**
 * The Hearth's data.
 *
 * The aggregate walks the whole events table, so it is fetched once a minute
 * rather than on every drop. In between, live drops are merged in here: a new
 * country appears on the globe the instant its first customer arrives, the
 * ticker updates, and the era bar advances — the refetch only ever confirms
 * what the page already showed.
 */
class HearthState {
  data = $state<Hearth | null>(null)
  loading = $state(false)
  error = $state('')

  #active = false
  #again = false
  #timer: ReturnType<typeof setInterval> | null = null
  #staleTimer: ReturnType<typeof setTimeout> | null = null
  #unsubscribe: (() => void) | null = null

  /**
   * Total XP, live. The feed's stats are bumped by every arriving drop, so the
   * era card counts up between refetches instead of sitting still for a minute.
   */
  get totalXP(): number {
    const live = loot.stats?.total_xp
    const known = this.data?.total_xp ?? 0
    return live !== undefined && live > known ? live : known
  }

  /**
   * How far through the current era, recomputed from live XP. The era's own
   * bounds come from the server, so no threshold table is duplicated here; an
   * era that is actually complete triggers a refetch, which is what renames it.
   */
  get eraProgress(): number {
    const era = this.data?.era
    if (!era) return 0
    if (!era.next_xp) return 1
    const span = era.next_xp - era.min_xp
    if (span <= 0) return 1
    return Math.max(0, Math.min(1, (this.totalXP - era.min_xp) / span))
  }

  /** XP still owed to the next era, live. 0 at the top of the ladder. */
  get toNextEra(): number {
    const era = this.data?.era
    if (!era?.next_xp) return 0
    return Math.max(0, era.next_xp - this.totalXP)
  }

  /** Called by the page while it is mounted: loads now, then polls. */
  activate(): () => void {
    this.#active = true
    void this.load()
    this.#timer = setInterval(() => void this.load(), REFRESH_MS)
    this.#unsubscribe = loot.onDrop((drop) => this.#applyDrop(drop))

    return () => {
      this.#active = false
      if (this.#timer) clearInterval(this.#timer)
      if (this.#staleTimer) clearTimeout(this.#staleTimer)
      this.#unsubscribe?.()
      this.#timer = this.#staleTimer = null
      this.#unsubscribe = null
    }
  }

  async load(): Promise<void> {
    if (this.loading) {
      this.#again = true
      return
    }
    this.loading = true
    try {
      this.data = await fetchHearth()
      this.error = ''
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err)
    } finally {
      this.loading = false
      if (this.#again) {
        this.#again = false
        void this.load()
      }
    }
  }

  /** Refetch soon, but only while the page is actually on screen. */
  markStale(): void {
    if (!this.#active) return
    if (this.#staleTimer) clearTimeout(this.#staleTimer)
    this.#staleTimer = setTimeout(() => void this.load(), STALE_DEBOUNCE_MS)
  }

  /**
   * Folds a freshly arrived drop into the aggregate so the globe does not wait
   * a minute to react. Populations are left to the server — only it knows what
   * counts as a citizen — but a brand new country is added immediately, because
   * founding a settlement is the whole point of watching.
   */
  #applyDrop(drop: Drop): void {
    const data = this.data
    if (!data || !drop.country) return

    const arrival: HearthDrop = {
      id: drop.id,
      rarity: drop.rarity,
      country: drop.country,
      title: drop.title,
      subtitle: drop.subtitle,
      kind: drop.kind,
      created_at: drop.created_at,
    }
    if (!data.recent.some((d) => d.id === drop.id)) {
      data.recent = [arrival, ...data.recent].slice(0, MAX_RECENT)
    }

    const existing = data.countries.find((c) => c.country === drop.country)
    if (existing) {
      existing.drops += 1
      existing.last_seen = drop.day || existing.last_seen
    } else {
      const founded: HearthCountry = {
        country: drop.country,
        population: 1,
        revenue_base: 0,
        first_seen: drop.day || drop.created_at.slice(0, 10),
        last_seen: drop.day || drop.created_at.slice(0, 10),
        drops: 1,
        // An outpost until the server says otherwise, which is a minute away
        // at most — and is what a country with one customer really is.
        tier: data.tiers[0] ?? { name: 'outpost', index: 0, min_share: 0 },
        share: 0,
      }
      data.countries = [...data.countries, founded]
      // No capital yet means this is the first country Loot has ever seen: it
      // is the capital until a bigger one comes along.
      if (!data.capital) data.capital = drop.country
    }

    // The era bar reads live XP already; a crossed threshold needs the server
    // to rename the era, and money needs it to move any settlement's tier.
    this.markStale()
  }
}

export const hearth = new HearthState()
