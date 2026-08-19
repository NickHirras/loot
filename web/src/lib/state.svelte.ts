import { fetchDrops, fetchSources, fetchStats, websocketURL } from './api'
import { sounds } from './sound'
import type { Drop, Rarity, SourceInfo, Stats, WSMessage } from './types'
import { RARITIES } from './types'

/** A drop plus client-only presentation state. */
export type FeedDrop = Drop & { fresh?: boolean }

const MUTE_KEY = 'loot.muted'
/** How many drops to keep in the DOM before old ones are trimmed. */
const MAX_DROPS = 400
/** How long a drop keeps its "just arrived" glow. */
const FRESH_MS = 2200

function readMuted(): boolean {
  try {
    return localStorage.getItem(MUTE_KEY) === '1'
  } catch {
    return false
  }
}

const emptyByRarity = (): Record<Rarity, number> =>
  Object.fromEntries(RARITIES.map((r) => [r, 0])) as Record<Rarity, number>

/**
 * The single source of truth for the dashboard: the drop feed, aggregate stats,
 * source health, the websocket connection and the sound settings.
 */
class LootState {
  drops = $state<FeedDrop[]>([])
  stats = $state<Stats | null>(null)
  sources = $state<SourceInfo[]>([])

  connected = $state(false)
  loading = $state(true)
  loadingMore = $state(false)
  error = $state('')
  nextBefore = $state('')

  muted = $state(readMuted())
  audioReady = $state(false)

  #socket: WebSocket | null = null
  #retry = 0
  #reconnectTimer: ReturnType<typeof setTimeout> | null = null
  #statsTimer: ReturnType<typeof setInterval> | null = null
  #stopped = false

  get byRarity(): Record<Rarity, number> {
    return this.stats?.by_rarity ?? emptyByRarity()
  }

  get devEnabled(): boolean {
    return this.stats?.dev ?? false
  }

  /** Loads the initial page and opens the live stream. */
  async start(): Promise<void> {
    this.#stopped = false
    await this.refresh()
    this.loading = false
    this.connect()

    // The websocket keeps the feed live; this catches anything that arrived
    // while disconnected and keeps source health fresh.
    this.#statsTimer = setInterval(() => void this.refreshMeta(), 30_000)
  }

  stop(): void {
    this.#stopped = true
    if (this.#reconnectTimer) clearTimeout(this.#reconnectTimer)
    if (this.#statsTimer) clearInterval(this.#statsTimer)
    this.#socket?.close()
    this.#socket = null
  }

  async refresh(): Promise<void> {
    try {
      const [page, stats, sources] = await Promise.all([fetchDrops(100), fetchStats(), fetchSources()])
      this.drops = page.drops
      this.nextBefore = page.next_before
      this.stats = stats
      this.sources = sources
      this.error = ''
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err)
    }
  }

  async refreshMeta(): Promise<void> {
    try {
      const [stats, sources] = await Promise.all([fetchStats(), fetchSources()])
      this.stats = stats
      this.sources = sources
    } catch {
      // A failed background refresh is not worth surfacing; the websocket
      // status already tells the user whether the server is reachable.
    }
  }

  /** Fetches the next page of older drops for infinite scroll. */
  async loadMore(): Promise<void> {
    if (!this.nextBefore || this.loadingMore) return
    this.loadingMore = true
    try {
      const page = await fetchDrops(100, this.nextBefore)
      this.drops = [...this.drops, ...page.drops]
      this.nextBefore = page.next_before
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err)
    } finally {
      this.loadingMore = false
    }
  }

  connect(): void {
    if (this.#stopped) return

    const socket = new WebSocket(websocketURL())
    this.#socket = socket

    socket.onopen = () => {
      this.connected = true
      this.#retry = 0
      // Catch up on anything missed while the socket was down.
      void this.refresh()
    }

    socket.onmessage = (event) => {
      let msg: WSMessage
      try {
        msg = JSON.parse(event.data as string) as WSMessage
      } catch {
        return
      }
      if (msg.type === 'chest') {
        // TODO(quest 2, frontend): render the chest badge and the open button.
        // Ignoring it keeps the feed working against a chest-aware server.
        return
      }
      if (msg.type === 'drop' && msg.drop) {
        // The wire splits drop and originating event; the API returns them
        // flattened, so merge here to keep one Drop shape everywhere.
        const ev = msg.event ?? {}
        this.receive({
          source: (ev.source as string) ?? '',
          kind: (ev.kind as string) ?? '',
          app: (ev.app as string) ?? '',
          country: (ev.country as string) ?? '',
          amount: (ev.amount as number) ?? 0,
          currency: (ev.currency as string) ?? '',
          quantity: (ev.quantity as number) ?? 0,
          occurred_at: (ev.occurred_at as string) ?? msg.drop.created_at,
          ...msg.drop,
        })
      }
    }

    socket.onclose = () => {
      this.connected = false
      this.#scheduleReconnect()
    }

    socket.onerror = () => {
      socket.close()
    }
  }

  #scheduleReconnect(): void {
    if (this.#stopped) return
    const delay = Math.min(1000 * 2 ** this.#retry, 15_000)
    this.#retry++
    this.#reconnectTimer = setTimeout(() => this.connect(), delay)
  }

  /** Handles a drop arriving over the websocket. */
  receive(drop: Drop): void {
    // The reconnect refresh can race the socket; never show a drop twice.
    if (this.drops.some((d) => d.id === drop.id)) return

    const fresh: FeedDrop = { ...drop, fresh: true }
    this.drops = [fresh, ...this.drops].slice(0, MAX_DROPS)
    this.#bumpStats(drop)

    if (!this.muted) sounds.play(drop.rarity)

    setTimeout(() => {
      const found = this.drops.find((d) => d.id === drop.id)
      if (found) found.fresh = false
    }, FRESH_MS)
  }

  /** Applies a new drop to the local counters so the header reacts instantly. */
  #bumpStats(drop: Drop): void {
    if (!this.stats) return
    this.stats.total_drops += 1
    this.stats.total_events += 1
    this.stats.total_xp += drop.xp
    this.stats.by_rarity[drop.rarity] = (this.stats.by_rarity[drop.rarity] ?? 0) + 1
    this.stats.by_source[drop.source] = (this.stats.by_source[drop.source] ?? 0) + 1
    if (drop.country && !this.stats.countries.includes(drop.country)) {
      this.stats.countries = [...this.stats.countries, drop.country].sort()
      this.stats.countries_count = this.stats.countries.length
    }
  }

  /** Called from a click handler to satisfy the browser's autoplay policy. */
  async enableAudio(): Promise<void> {
    this.audioReady = await sounds.unlock()
    if (this.audioReady) sounds.setVolume(this.muted ? 0 : 0.9)
  }

  toggleMute(): void {
    this.muted = !this.muted
    try {
      localStorage.setItem(MUTE_KEY, this.muted ? '1' : '0')
    } catch {
      // Private browsing without storage: the setting just will not persist.
    }
    sounds.setVolume(this.muted ? 0 : 0.9)
    if (!this.muted && this.audioReady) sounds.play('uncommon', 0.6)
  }
}

export const loot = new LootState()
