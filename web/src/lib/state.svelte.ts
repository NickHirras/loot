import { fetchChests, fetchDrops, fetchSources, fetchStats, openChest, websocketURL } from './api'
import { codexState } from './codex.svelte'
import { questsState } from './quests.svelte'
import { prefersReducedMotion } from './route.svelte'
import { sounds } from './sound'
import type { ChestSummary, Drop, Rarity, SourceInfo, Stats, WSMessage } from './types'
import { RARITIES, RARITY_RANK } from './types'
import { vault } from './vault.svelte'

/** A drop plus client-only presentation state. */
export type FeedDrop = Drop & { fresh?: boolean }

const MUTE_KEY = 'loot.muted'
/** How many drops to keep in the DOM before old ones are trimmed. */
const MAX_DROPS = 400
/** How long a drop keeps its "just arrived" glow. */
const FRESH_MS = 2200

/** How long the lid animation runs before the first drop is shown. */
const LID_MS = 900
const LID_MS_REDUCED = 120
/** The floor between two reveals, so a queued burst still plays as a cascade. */
const PACE_MS = 520
/**
 * How long the cascade waits on the websocket before pulling the next drop out
 * of the POST response instead. The server spaces drops 600 ms apart, so this
 * only fires when a message was genuinely lost (or the socket is down).
 */
const STALL_MS = 1600
/** How often the cascade engine looks at its queue. */
const TICK_MS = 120

/** Where an open chest is in its reveal. */
export type ChestPhase = 'idle' | 'opening' | 'cascade' | 'done'

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
 * source health, the daily chest, the websocket connection and the sound
 * settings.
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

  // ------------------------------------------------------------------ chest

  /** The chests still waiting, oldest first. */
  chests = $state<ChestSummary[]>([])
  /** Whether the chest overlay is on screen. */
  chestOverlay = $state(false)
  chestPhase = $state<ChestPhase>('idle')
  /** The day being opened right now. */
  chestDate = $state('')
  /** Everything the opened chest holds, in cascade order. */
  chestExpected = $state<Drop[]>([])
  /** What has been revealed so far, in arrival order. */
  chestRevealed = $state<Drop[]>([])
  /** The drop currently centre stage. */
  chestCurrent = $state<Drop | null>(null)
  chestError = $state('')

  #chestsLoaded = false
  /**
   * True once infinite scroll has fetched at least one older page. From then
   * on the feed is no longer just "the newest N drops": trimming its tail
   * would open a gap between the oldest card on screen and `nextBefore`, and
   * the next page would load into the middle of a hole. Anyone who has not
   * scrolled back — which is everyone watching the live feed — still gets the
   * MAX_DROPS cap.
   */
  #paged = false
  #queue: Drop[] = []
  #shown = new Set<string>()
  #queued = new Set<string>()
  #lastShownAt = 0
  #cascadeTimer: ReturnType<typeof setInterval> | null = null

  /**
   * Everyone who wants to hear about a drop the moment it lands. The websocket
   * is opened once, here, so the globe (and anything else that animates an
   * arrival) subscribes rather than opening a second socket of its own.
   */
  #listeners = new Set<(drop: Drop) => void>()

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

  /** True when the server is serving synthetic demo data. */
  get demo(): boolean {
    return this.stats?.demo ?? false
  }

  /**
   * How many mysteries are still unexplained, for the Quests tab badge. The
   * page's own state wins once it has loaded, so solving one updates the badge
   * without waiting for the next stats poll.
   */
  get openMysteries(): number {
    return questsState.casebook.open.length || (this.stats?.open_mysteries ?? 0)
  }

  /** How many drops are waiting in unopened chests. Drives the header badge. */
  get chestCount(): number {
    if (this.#chestsLoaded) return this.chests.reduce((sum, c) => sum + c.count, 0)
    return this.stats?.unrevealed_count ?? 0
  }

  /** True while a chest is mid-reveal, which several buttons need to know. */
  get chestBusy(): boolean {
    return this.chestPhase === 'opening' || this.chestPhase === 'cascade'
  }

  /** XP gained by the chest that was just opened. */
  get chestHaulXP(): number {
    return this.chestRevealed.reduce((sum, d) => sum + d.xp, 0)
  }

  /** The rarest (then highest XP) drop of the current haul. */
  get chestBest(): Drop | null {
    let best: Drop | null = null
    for (const drop of this.chestRevealed) {
      if (!best) {
        best = drop
        continue
      }
      const rank = RARITY_RANK[drop.rarity] ?? 0
      const bestRank = RARITY_RANK[best.rarity] ?? 0
      // `cursed` sits above the ladder as bad news; it never wins "best drop".
      const better = drop.rarity !== 'cursed' && (best.rarity === 'cursed' || rank > bestRank)
      if (better || (rank === bestRank && drop.rarity !== 'cursed' && drop.xp > best.xp)) best = drop
    }
    return best
  }

  /**
   * Subscribes to drops as they arrive. Returns the unsubscribe function, so a
   * component can hand it straight back from an `$effect`.
   */
  onDrop(listener: (drop: Drop) => void): () => void {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
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
    if (this.#cascadeTimer) clearInterval(this.#cascadeTimer)
    this.#socket?.close()
    this.#socket = null
  }

  async refresh(): Promise<void> {
    try {
      const [page, stats, sources, chests] = await Promise.all([
        fetchDrops(100),
        fetchStats(),
        fetchSources(),
        fetchChests(),
      ])
      this.#mergeHead(page.drops, page.next_before)
      this.stats = stats
      this.sources = sources
      this.#setChests(chests)
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
      return
    }
    // The chest list rides the websocket; only re-read it when a cascade is
    // not in flight, so a late poll cannot resurrect a chest being opened.
    if (this.chestBusy) return
    await this.#refreshChests()
  }

  /**
   * Re-reads the chest list. The busy check is repeated after the fetch as
   * well as before it: a chest can start opening while the request is in the
   * air, and a reply from before the open would put the opened chest back.
   */
  async #refreshChests(): Promise<void> {
    try {
      const chests = await fetchChests()
      if (!this.chestBusy) this.#setChests(chests)
    } catch {
      // The badge keeps its last known value, which beats zeroing it.
    }
  }

  /**
   * Folds a freshly fetched first page into the feed. A reconnect (or any
   * other catch-up refresh) must not throw away pages the user scrolled back
   * to load, so unseen drops are prepended and the existing tail — and the
   * cursor that continues it — are left alone.
   */
  #mergeHead(head: Drop[], nextBefore: string): void {
    if (this.drops.length === 0) {
      this.drops = head
      this.nextBefore = nextBefore
      return
    }
    const known = new Set(this.drops.map((d) => d.id))
    const unseen = head.filter((d) => !known.has(d.id))
    if (unseen.length) this.drops = [...unseen, ...this.drops]
    // `nextBefore` is left as it is on purpose: it already points past
    // everything still loaded, which a fresh head page cannot improve on, and
    // an empty one means the whole hoard is in hand.
  }

  /** Fetches the next page of older drops for infinite scroll. */
  async loadMore(): Promise<void> {
    if (!this.nextBefore || this.loadingMore) return
    this.loadingMore = true
    try {
      const page = await fetchDrops(100, this.nextBefore)
      this.drops = [...this.drops, ...page.drops]
      this.nextBefore = page.next_before
      this.#paged = true
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
      // Catch up on anything missed while the socket was down — unless a chest
      // is mid-cascade, whose drops the reveal engine is still holding back.
      if (!this.chestBusy) void this.refresh()
    }

    socket.onmessage = (event) => {
      let msg: WSMessage
      try {
        msg = JSON.parse(event.data as string) as WSMessage
      } catch {
        return
      }
      if (msg.type === 'chest') {
        this.#setChests(msg.chests ?? [])
        return
      }
      // The quest board, the casebook and the codex are nudges, not payloads:
      // the page refetches, and the header's counts ride along on the next
      // stats read.
      if (msg.type === 'quests' || msg.type === 'mysteries') {
        questsState.markStale()
        void this.refreshMeta()
        return
      }
      if (msg.type === 'codex') {
        codexState.markStale()
        return
      }
      if (msg.type === 'drop' && msg.drop) {
        // The wire splits drop and originating event; the API returns them
        // flattened, so merge here to keep one Drop shape everywhere.
        const ev = msg.event ?? {}
        const drop: Drop = {
          source: (ev.source as string) ?? '',
          kind: (ev.kind as string) ?? '',
          app: (ev.app as string) ?? '',
          country: (ev.country as string) ?? '',
          amount: (ev.amount as number) ?? 0,
          amount_base: (ev.amount_base as number) ?? 0,
          currency: (ev.currency as string) ?? '',
          quantity: (ev.quantity as number) ?? 0,
          day: (ev.day as string) ?? '',
          occurred_at: (ev.occurred_at as string) ?? msg.drop.created_at,
          ...msg.drop,
        }

        // A drop from the chest being opened here is the cascade's clock: it
        // goes into the reveal queue instead of straight into the feed.
        if (msg.chest && this.#wantsArrival(drop)) {
          this.#enqueue(drop)
          return
        }
        this.receive(drop)
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
  receive(drop: Drop, quiet = false): void {
    // The reconnect refresh can race the socket; never show a drop twice.
    if (this.drops.some((d) => d.id === drop.id)) return

    const fresh: FeedDrop = { ...drop, fresh: true }
    const next = [fresh, ...this.drops]
    // Only trim a feed still sitting on its first page; see `#paged`.
    this.drops = this.#paged ? next : next.slice(0, MAX_DROPS)
    this.#bumpStats(drop)
    // Money that landed changes what the vault would answer.
    if (drop.amount_base || drop.amount) vault.markStale()
    // A completion drop is the quest board's own news; refresh it so the card
    // lights up as the sound plays.
    if (drop.kind === 'quest_complete' || drop.kind === 'mystery_solved') questsState.markStale()
    // An achievement drop *is* the unlock, arriving on the normal drop
    // pathway with its own rarity and sound; the wall behind it is stale.
    if (drop.kind === 'achievement') codexState.markStale()

    if (!this.muted && !quiet) sounds.play(drop.rarity)

    for (const listener of this.#listeners) {
      try {
        listener(drop)
      } catch {
        // A subscriber that throws must not stall the feed or the cascade.
      }
    }

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

  // ------------------------------------------------------------ chest reveal

  #setChests(chests: ChestSummary[]): void {
    this.chests = chests
    this.#chestsLoaded = true
    if (this.stats) this.stats.unrevealed_count = chests.reduce((sum, c) => sum + c.count, 0)
  }

  showChest(): void {
    this.chestOverlay = true
  }

  /** Closes the overlay. The cascade, if any, keeps running underneath. */
  hideChest(): void {
    this.chestOverlay = false
    if (this.chestPhase === 'done') this.#resetChest()
  }

  #resetChest(): void {
    this.chestPhase = 'idle'
    this.chestDate = ''
    this.chestExpected = []
    this.chestRevealed = []
    this.chestCurrent = null
    this.#queue = []
    this.#shown.clear()
    this.#queued.clear()
  }

  /**
   * Opens a chest and starts the reveal. The POST answers with the whole haul,
   * which is both the cascade's script and its safety net; the websocket
   * arrivals are what actually pace it.
   */
  async open(date = ''): Promise<void> {
    if (this.chestBusy) return
    this.#resetChest()
    this.chestError = ''
    this.chestPhase = 'opening'
    this.chestDate = date

    let result
    try {
      result = await openChest(date)
    } catch (err) {
      this.chestError = err instanceof Error ? err.message : String(err)
      this.chestPhase = 'idle'
      return
    }

    // A reply without a `chests` field says nothing about what is left, so the
    // badge keeps its value and the truth is fetched instead of assumed.
    if (result.chests) this.#setChests(result.chests)
    if (result.count === 0 || result.drops.length === 0) {
      this.chestError = 'That chest is already open.'
      this.chestPhase = 'idle'
      if (!result.chests) void this.#refreshChests()
      return
    }
    // A cascade is starting, so the refetch waits: `#finish` refreshes once
    // the reveal is over, when a chest list can safely be believed again.

    this.chestDate = result.opened
    this.chestExpected = result.drops
    // Anything the socket delivered during the POST is already queued; the lid
    // animation runs first either way.
    await new Promise((r) => setTimeout(r, prefersReducedMotion() ? LID_MS_REDUCED : LID_MS))

    this.chestPhase = 'cascade'
    this.#lastShownAt = Date.now()
    if (this.#cascadeTimer) clearInterval(this.#cascadeTimer)
    this.#cascadeTimer = setInterval(() => this.#tick(), TICK_MS)
  }

  /** True when a websocket arrival belongs to the reveal happening right now. */
  #wantsArrival(drop: Drop): boolean {
    if (!this.chestBusy) return false
    if (this.#shown.has(drop.id) || this.#queued.has(drop.id)) return false
    // While the POST is still in flight the date is not known yet, so accept
    // any chest drop; afterwards, only the chest actually being opened.
    return !this.chestDate || !drop.chest_date || drop.chest_date === this.chestDate
  }

  #enqueue(drop: Drop): void {
    this.#queued.add(drop.id)
    this.#queue.push(drop)
    // A drop the POST response did not mention still counts towards the haul.
    if (!this.chestExpected.some((d) => d.id === drop.id)) this.chestExpected = [...this.chestExpected, drop]
  }

  #tick(): void {
    if (this.chestPhase !== 'cascade') return
    const now = Date.now()
    if (now - this.#lastShownAt < PACE_MS) return

    const next = this.#queue.shift()
    if (next) {
      this.#show(next)
      return
    }

    const missing = this.chestExpected.filter((d) => !this.#shown.has(d.id) && !this.#queued.has(d.id))
    if (missing.length === 0) {
      this.#finish()
      return
    }
    // The socket owes us a drop and has not delivered: fall back to the POST
    // response so a lost message cannot strand the cascade.
    if (now - this.#lastShownAt >= STALL_MS) this.#show(missing[0])
  }

  #show(drop: Drop, quiet = false): void {
    this.#shown.add(drop.id)
    this.#queued.delete(drop.id)
    this.chestRevealed = [...this.chestRevealed, drop]
    this.chestCurrent = drop
    this.#lastShownAt = Date.now()
    this.receive(drop, quiet)
  }

  /** Reveals whatever is left at once, without a wall of sound. */
  skipCascade(): void {
    if (this.chestPhase !== 'cascade') return
    for (const drop of this.chestExpected) {
      if (!this.#shown.has(drop.id)) this.#show(drop, true)
    }
    this.#queue = []
    this.#finish()
  }

  #finish(): void {
    if (this.#cascadeTimer) clearInterval(this.#cascadeTimer)
    this.#cascadeTimer = null
    this.chestPhase = 'done'
    this.chestCurrent = null
    vault.markStale()
    void this.refreshMeta()
    // Nobody is watching the final screen, so do not save it for later.
    if (!this.chestOverlay) this.#resetChest()
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
