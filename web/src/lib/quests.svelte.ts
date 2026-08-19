import { untrack } from 'svelte'
import { createQuest, deleteQuest, dismissMystery, fetchMysteries, fetchQuests, solveMystery } from './api'
import { scope } from './scope.svelte'
import type { Casebook, Mystery, NewQuest, Quest, QuestBoard } from './types'

/** How often the page re-reads the board while it is open. */
const REFRESH_MS = 60_000
/** A cascade of drops should cause one refresh, not one per drop. */
const STALE_DEBOUNCE_MS = 1_200
/** How long a just-completed quest keeps its glow. */
const FLASH_MS = 5_000
/** A completion older than this was not watched happening; no glow. */
const FRESH_COMPLETION_MS = 30_000

/**
 * The Quests page's data: the board, the casebook, and the small amount of
 * client-only state that makes them feel alive (which quest just lit up, which
 * button is mid-flight).
 *
 * Kept apart from the feed state for the same reason the vault is: the page
 * refreshes on its own clock, and importing it never drags the websocket
 * along.
 */
class QuestsState {
  board = $state<QuestBoard>({ active: [], recent: [] })
  casebook = $state<Casebook>({ open: [], resolved: [] })
  loading = $state(true)
  error = $state('')
  /**
   * True once a fetch has actually succeeded. The header badge prefers this
   * casebook over the stats poll's count from then on — including when the
   * answer is zero, which is exactly the moment that matters: solving the last
   * mystery must clear the badge, not fall back to a stat that still says one.
   */
  loaded = $state(false)
  /** Ids of quests that finished while the page was watching. */
  flashing = $state<string[]>([])
  /** Ids with a request in flight, so a button can disable itself. */
  busy = $state<string[]>([])

  #active = false
  #timer: ReturnType<typeof setInterval> | null = null
  #staleTimer: ReturnType<typeof setTimeout> | null = null
  #loading = false
  #again = false
  /** Completions already seen, so a quest only ever glows once. */
  #celebrated = new Set<string>()

  /** How many questions are still open; the header badge reads this. */
  get openMysteries(): number {
    return this.casebook.open.length
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
      // A scope change is not "stale soon", it is "wrong now".
      const unscope = scope.onChange(() => void this.load())
      return () => {
        this.#active = false
        if (this.#timer) clearInterval(this.#timer)
        if (this.#staleTimer) clearTimeout(this.#staleTimer)
        this.#timer = this.#staleTimer = null
        unscope()
      }
    })
  }

  /**
   * Something happened that the board does not know about — a quest completed,
   * a mystery was flagged, money landed. Refresh soon, but only while the page
   * is on screen; otherwise its own load-on-mount covers it.
   */
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
      const [board, casebook] = await Promise.all([fetchQuests(), fetchMysteries()])
      this.#celebrate(board.recent)
      this.board = board
      this.casebook = casebook
      this.loaded = true
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
   * Lights up quests that finished just now. The first load of a session marks
   * everything as already celebrated, so opening the page does not replay a
   * week of confetti.
   */
  #celebrate(recent: Quest[]): void {
    const firstLoad = this.#celebrated.size === 0 && this.board.recent.length === 0
    const now = Date.now()
    for (const quest of recent) {
      if (quest.status !== 'completed' || this.#celebrated.has(quest.id)) continue
      this.#celebrated.add(quest.id)
      if (firstLoad || !quest.completed_at) continue
      if (now - new Date(quest.completed_at).getTime() > FRESH_COMPLETION_MS) continue
      this.flashing = [...this.flashing, quest.id]
      setTimeout(() => {
        this.flashing = this.flashing.filter((id) => id !== quest.id)
      }, FLASH_MS)
    }
  }

  #startBusy(id: string): void {
    this.busy = [...this.busy, id]
  }

  #endBusy(id: string): void {
    this.busy = this.busy.filter((b) => b !== id)
  }

  isBusy(id: string): boolean {
    return this.busy.includes(id)
  }

  /** Sets a quest of your own. Throws with the API's own reason on refusal. */
  async create(req: NewQuest): Promise<Quest> {
    const quest = await createQuest(req)
    await this.load()
    return quest
  }

  async remove(id: string): Promise<void> {
    this.#startBusy(id)
    try {
      await deleteQuest(id)
      this.board = { ...this.board, active: this.board.active.filter((q) => q.id !== id) }
      await this.load()
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err)
    } finally {
      this.#endBusy(id)
    }
  }

  /** Writes down what you think happened. The drop arrives over the socket. */
  async solve(id: string, note: string): Promise<void> {
    this.#startBusy(id)
    try {
      const solved = await solveMystery(id, note)
      this.#resolve(id, solved)
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err)
    } finally {
      this.#endBusy(id)
    }
  }

  async dismiss(id: string): Promise<void> {
    this.#startBusy(id)
    try {
      const dismissed = await dismissMystery(id)
      this.#resolve(id, dismissed)
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err)
    } finally {
      this.#endBusy(id)
    }
  }

  /** Moves a mystery from the open list into the notebook, without waiting
   * for a round trip the answer already contains. */
  #resolve(id: string, resolved: Mystery): void {
    this.casebook = {
      open: this.casebook.open.filter((m) => m.id !== id),
      resolved: [resolved, ...this.casebook.resolved].slice(0, 20),
    }
  }
}

export const questsState = new QuestsState()
