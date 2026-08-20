import type {
  Achievement,
  AppProduct,
  AppsResponse,
  Boss,
  BossBoard,
  Casebook,
  ChestSummary,
  CodexBoard,
  Drop,
  Hearth,
  HearthCountry,
  HearthDrop,
  HearthTier,
  Mystery,
  NewQuest,
  Quest,
  QuestBoard,
  Recap,
  RecapPeriod,
  RecapResponse,
  SourceInfo,
  Stats,
  UnmappedApp,
  VaultRange,
  VaultSummary,
} from './types'

/**
 * The app scope, as a plain module variable rather than as reactive state.
 *
 * Every read endpoint takes `?app=<product>`, so rather than thread a scope
 * argument through two dozen call sites, the scope lives here and every
 * request picks it up. It is deliberately *not* `$state`: `scope.svelte.ts`
 * owns the reactive copy the UI renders and pushes it down here, which keeps
 * api.ts free of Svelte imports and — more usefully — keeps a fetch from
 * becoming a reactive dependency of whatever effect happened to trigger it.
 */
let currentScope = ''

/** Sets the product every subsequent request is scoped to. "" is all apps. */
export function setScope(product: string): void {
  currentScope = product
}

/** The product requests are currently scoped to. */
export function getScope(): string {
  return currentScope
}

/** Adds `app=` to a parameter set when a scope is active. */
function withScope(params: URLSearchParams): URLSearchParams {
  if (currentScope) params.set('app', currentScope)
  return params
}

/** Renders a scoped query string, including the leading "?", or "". */
function scopeQuery(): string {
  return currentScope ? `?app=${encodeURIComponent(currentScope)}` : ''
}

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: 'application/json' } })
  if (!res.ok) throw new Error(`${path}: ${res.status} ${res.statusText}`)
  return (await res.json()) as T
}

async function postJSON(path: string, body: unknown): Promise<Response> {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new Error(await errorText(res, path))
  return res
}

/**
 * The API answers a rejected request with `{"error": "…"}` and a reason worth
 * showing — "target must be greater than zero" is more use in a form than
 * "400 Bad Request".
 */
async function errorText(res: Response, path: string): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string }
    if (body?.error) return body.error
  } catch {
    // Not JSON, or no body at all: fall back to the status line.
  }
  return `${path}: ${res.status} ${res.statusText}`
}

export async function fetchDrops(limit = 100, before?: string): Promise<{ drops: Drop[]; next_before: string }> {
  const params = withScope(new URLSearchParams({ limit: String(limit) }))
  if (before) params.set('before', before)
  const data = await getJSON<{ drops: Drop[] | null; next_before: string }>(`/api/drops?${params}`)
  return { drops: data.drops ?? [], next_before: data.next_before ?? '' }
}

export function fetchStats(): Promise<Stats> {
  return getJSON<Stats>(`/api/stats${scopeQuery()}`)
}

export async function fetchSources(): Promise<SourceInfo[]> {
  const data = await getJSON<{ sources: SourceInfo[] | null }>('/api/sources')
  return data.sources ?? []
}

/**
 * What apps this Loot knows about: the configured products, anything a source
 * reported that no mapping claimed, and the evidence for both.
 *
 * Deliberately unscoped — it is the list you pick a scope *from*.
 */
export async function fetchApps(): Promise<AppsResponse> {
  const data = await getJSON<{ products: AppProduct[] | null; unmapped: UnmappedApp[] | null }>('/api/apps')
  return { products: data.products ?? [], unmapped: data.unmapped ?? [] }
}

export function fetchVaultSummary(range: VaultRange): Promise<VaultSummary> {
  const params = withScope(new URLSearchParams({ range }))
  return getJSON<VaultSummary>(`/api/vault/summary?${params}`)
}

export async function fetchChests(): Promise<ChestSummary[]> {
  const data = await getJSON<{ chests: ChestSummary[] | null }>('/api/chest')
  return data.chests ?? []
}

/** What POST /api/chest/open answers with. */
export interface ChestOpenResult {
  /** The first (oldest) chest opened, or "" if there was nothing to open. */
  opened: string
  /**
   * Every chest opened, oldest first. One entry for a single open; the whole
   * batch for `{"all":true}`. `drops` is one flat list across all of them,
   * already in the order it should be revealed: chest by chest, and within a
   * chest quietest news first.
   */
  opened_dates: string[]
  count: number
  drops: Drop[]
  /**
   * What is left unopened. `null` means the server said nothing about it —
   * which is not the same as "nothing left", so the caller must refetch rather
   * than believe an empty list it was never given.
   */
  chests: ChestSummary[] | null
}

async function postChestOpen(body: Record<string, unknown>): Promise<ChestOpenResult> {
  const res = await postJSON('/api/chest/open', body)
  const data = (await res.json()) as Partial<ChestOpenResult>
  const drops = data.drops ?? []
  return {
    opened: data.opened ?? '',
    // A server that predates bulk opens answers with `opened` alone; derive
    // the list from it so callers only have to read one field.
    opened_dates: data.opened_dates ?? (data.opened ? [data.opened] : []),
    count: data.count ?? 0,
    drops,
    chests: data.chests ?? null,
  }
}

/** Opens a chest. An empty date opens the oldest one, which is the default. */
export function openChest(date = ''): Promise<ChestOpenResult> {
  return postChestOpen(date ? { date } : {})
}

/** Opens every chest waiting, oldest first, in one request. */
export function openAllChests(): Promise<ChestOpenResult> {
  return postChestOpen({ all: true })
}

/** The body POST /api/dev/fake accepts; every field is optional. */
export interface FakeDropRequest {
  rarity?: string
  kind?: string
  app?: string
  country?: string
  amount?: number
  currency?: string
  quantity?: number
  day?: string
  chest?: boolean
  silent?: boolean
}

/** Fires a synthetic drop. Only mounted when the server runs with dev enabled. */
export async function fireFakeDrop(req: FakeDropRequest): Promise<void> {
  await postJSON('/api/dev/fake', req)
}

/** The websocket URL for this page, honouring https and any base path. */
export function websocketURL(): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${location.host}/ws`
}

/**
 * The globe: settlements, era, capital and the recent arrivals ticker.
 *
 * Go marshals an empty slice as `null`, so every list the page iterates is
 * defaulted here rather than guarded at each of its half-dozen call sites.
 */
export async function fetchHearth(): Promise<Hearth> {
  const data = await getJSON<
    Omit<Hearth, 'countries' | 'tiers' | 'recent'> & {
      countries: HearthCountry[] | null
      tiers: HearthTier[] | null
      recent: HearthDrop[] | null
    }
  >(`/api/hearth${scopeQuery()}`)
  return {
    ...data,
    countries: data.countries ?? [],
    tiers: data.tiers ?? [],
    recent: data.recent ?? [],
  }
}

// ---------------------------------------------------------------- quests

/** The quest board: what is running, and what recently finished or ended. */
export async function fetchQuests(): Promise<QuestBoard> {
  const data = await getJSON<{ active: Quest[] | null; recent: Quest[] | null }>(`/api/quests${scopeQuery()}`)
  return { active: data.active ?? [], recent: data.recent ?? [] }
}

/** Sets a quest of your own. */
export async function createQuest(req: NewQuest): Promise<Quest> {
  const res = await postJSON('/api/quests', req)
  const data = (await res.json()) as { quest: Quest }
  return data.quest
}

/** Removes a custom quest. Generated ones expire on their own instead. */
export async function deleteQuest(id: string): Promise<void> {
  const res = await fetch(`/api/quests/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) throw new Error(await errorText(res, '/api/quests'))
}

// -------------------------------------------------------------- mysteries

/** The casebook: what is unexplained, and the notes on what was. */
export async function fetchMysteries(): Promise<Casebook> {
  const data = await getJSON<{ open: Mystery[] | null; resolved: Mystery[] | null }>(`/api/mysteries${scopeQuery()}`)
  return { open: data.open ?? [], resolved: data.resolved ?? [] }
}

/** Writes down what you think happened, which pays a drop. */
export async function solveMystery(id: string, note: string): Promise<Mystery> {
  const res = await postJSON(`/api/mysteries/${encodeURIComponent(id)}/solve`, { note })
  const data = (await res.json()) as { mystery: Mystery }
  return data.mystery
}

/** Closes one quietly: no drop, no XP, no judgement. */
export async function dismissMystery(id: string): Promise<Mystery> {
  const res = await postJSON(`/api/mysteries/${encodeURIComponent(id)}/dismiss`, {})
  const data = (await res.json()) as { mystery: Mystery }
  return data.mystery
}

// ------------------------------------------------------------------ codex

/**
 * The trophy wall: every achievement with its progress, plus the records and
 * lifetime totals, which the server computes on read.
 */
export async function fetchCodex(): Promise<CodexBoard> {
  const data = await getJSON<Omit<CodexBoard, 'achievements'> & { achievements: Achievement[] | null }>(
    `/api/codex${scopeQuery()}`,
  )
  return { ...data, achievements: data.achievements ?? [] }
}

/**
 * One season recap. `period` is a month key ("2026-07") or a year ("2026");
 * omitting it asks for the last complete month, which is the one worth
 * sharing.
 */
export async function fetchRecap(period = ''): Promise<RecapResponse> {
  const params = withScope(new URLSearchParams())
  if (/^\d{4}$/.test(period)) params.set('season', period)
  else if (period) params.set('month', period)
  const query = params.toString()
  const data = await getJSON<{ recap: Recap; periods: RecapPeriod[] | null }>(
    query ? `/api/recap?${query}` : '/api/recap',
  )
  return { recap: data.recap, periods: data.periods ?? [] }
}

// ----------------------------------------------------------------- bosses

/** The boss board: the fights in progress, and the last few that ended. */
export async function fetchBosses(): Promise<BossBoard> {
  const data = await getJSON<{ alive: Boss[] | null; recent: Boss[] | null }>(`/api/bosses${scopeQuery()}`)
  return { alive: data.alive ?? [], recent: data.recent ?? [] }
}

/** Says you fixed it. The epic drop arrives over the socket. */
export async function slayBoss(id: string): Promise<Boss> {
  const res = await postJSON(`/api/bosses/${encodeURIComponent(id)}/slay`, {})
  const data = (await res.json()) as { boss: Boss }
  return data.boss
}
