import { untrack } from 'svelte'
import { fetchBosses, slayBoss } from './api'
import type { Boss, BossBoard } from './types'

/** How often the page re-reads the board while it is open. */
const REFRESH_MS = 60_000
/** A poll that ingests four hundred crash rows should cause one refresh. */
const STALE_DEBOUNCE_MS = 1_200
/** How long a freshly slain boss keeps its glow. */
const FLASH_MS = 5_000
/** A kill older than this was not watched happening; no glow. */
const FRESH_KILL_MS = 30_000

/**
 * The Boss fights section's data.
 *
 * It is a separate store from the quest board even though the two share a tab,
 * because they answer different endpoints on different clocks and a crash poll
 * has no business re-reading the quest board. The Quests page activates both.
 */
class BossesState {
  board = $state<BossBoard>({ alive: [], recent: [] })
  loading = $state(true)
  error = $state('')
  /** Ids of bosses that died while the page was watching. */
  flashing = $state<string[]>([])
  /** Ids with a request in flight, so a button can disable itself. */
  busy = $state<string[]>([])
  /** The boss whose "Mark slain" button is waiting for a second click. */
  confirming = $state('')

  #active = false
  #timer: ReturnType<typeof setInterval> | null = null
  #staleTimer: ReturnType<typeof setTimeout> | null = null
  #loading = false
  #again = false
  /** Kills already seen, so a boss only ever glows once. */
  #celebrated = new Set<string>()

  /** How many monsters are still standing; the header badge reads this. */
  get aliveCount(): number {
    return this.board.alive.length
  }

  /** True when there is anything at all to show. */
  get any(): boolean {
    return this.board.alive.length > 0 || this.board.recent.length > 0
  }

  /**
   * Called by the page while it is mounted: loads now, then polls. `#loading`
   * is the in-flight guard rather than the reactive `loading`, and the body is
   * untracked on top of that, so calling this from an `$effect` can never
   * subscribe that effect to state the load itself writes.
   */
  activate(): () => void {
    return untrack(() => {
      this.#active = true
      void this.load()
      this.#timer = setInterval(() => void this.load(), REFRESH_MS)
      return () => {
        this.#active = false
        if (this.#timer) clearInterval(this.#timer)
        if (this.#staleTimer) clearTimeout(this.#staleTimer)
        this.#timer = this.#staleTimer = null
      }
    })
  }

  /** A boss moved and the board does not know. Refresh soon, but only while
   * the section is on screen; otherwise its own load-on-mount covers it. */
  markStale(): void {
    if (!this.#active) return
    if (this.#staleTimer) clearTimeout(this.#staleTimer)
    this.#staleTimer = setTimeout(() => void this.load(), STALE_DEBOUNCE_MS)
  }

  async load(): Promise<void> {
    if (this.#loading) {
      this.#again = true
      return
    }
    this.#loading = true
    try {
      const board = await fetchBosses()
      this.#celebrate(board.recent)
      this.board = board
      this.error = ''
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err)
    } finally {
      this.loading = false
      this.#loading = false
      if (this.#again) {
        this.#again = false
        void this.load()
      }
    }
  }

  /**
   * Lights up bosses killed just now. The first load of a session marks
   * everything as already celebrated, so opening the page does not replay a
   * month of victories.
   */
  #celebrate(recent: Boss[]): void {
    const firstLoad = this.#celebrated.size === 0 && this.board.recent.length === 0
    const now = Date.now()
    for (const boss of recent) {
      if (boss.status !== 'slain' || this.#celebrated.has(boss.id)) continue
      this.#celebrated.add(boss.id)
      if (firstLoad || !boss.slain_at) continue
      if (now - new Date(boss.slain_at).getTime() > FRESH_KILL_MS) continue
      this.flashing = [...this.flashing, boss.id]
      setTimeout(() => {
        this.flashing = this.flashing.filter((id) => id !== boss.id)
      }, FLASH_MS)
    }
  }

  isBusy(id: string): boolean {
    return this.busy.includes(id)
  }

  /** Arms the inline confirm. A kill pays a real drop, so it is worth one
   * more click than a dismissal is. */
  askConfirm(id: string): void {
    this.confirming = this.confirming === id ? '' : id
  }

  cancelConfirm(): void {
    this.confirming = ''
  }

  /** Says you fixed it. The epic drop arrives over the socket. */
  async slay(id: string): Promise<void> {
    this.confirming = ''
    this.busy = [...this.busy, id]
    try {
      const slain = await slayBoss(id)
      this.#celebrated.add(slain.id)
      this.board = {
        alive: this.board.alive.filter((b) => b.id !== id),
        recent: [slain, ...this.board.recent].slice(0, 20),
      }
      this.flashing = [...this.flashing, slain.id]
      setTimeout(() => {
        this.flashing = this.flashing.filter((f) => f !== slain.id)
      }, FLASH_MS)
      // The server's own reward is minted asynchronously; re-read once so the
      // card shows the XP the kill actually paid.
      this.markStale()
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err)
    } finally {
      this.busy = this.busy.filter((b) => b !== id)
    }
  }
}

export const bossesState = new BossesState()
