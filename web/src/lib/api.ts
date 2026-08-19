import type { ChestSummary, Drop, SourceInfo, Stats, VaultRange, VaultSummary } from './types'

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
  if (!res.ok) throw new Error(`${path}: ${res.status} ${res.statusText}`)
  return res
}

export async function fetchDrops(limit = 100, before?: string): Promise<{ drops: Drop[]; next_before: string }> {
  const params = new URLSearchParams({ limit: String(limit) })
  if (before) params.set('before', before)
  const data = await getJSON<{ drops: Drop[] | null; next_before: string }>(`/api/drops?${params}`)
  return { drops: data.drops ?? [], next_before: data.next_before ?? '' }
}

export function fetchStats(): Promise<Stats> {
  return getJSON<Stats>('/api/stats')
}

export async function fetchSources(): Promise<SourceInfo[]> {
  const data = await getJSON<{ sources: SourceInfo[] | null }>('/api/sources')
  return data.sources ?? []
}

export function fetchVaultSummary(range: VaultRange): Promise<VaultSummary> {
  return getJSON<VaultSummary>(`/api/vault/summary?range=${encodeURIComponent(range)}`)
}

export async function fetchChests(): Promise<ChestSummary[]> {
  const data = await getJSON<{ chests: ChestSummary[] | null }>('/api/chest')
  return data.chests ?? []
}

/** What POST /api/chest/open answers with. */
export interface ChestOpenResult {
  opened: string
  count: number
  drops: Drop[]
  chests: ChestSummary[]
}

/** Opens a chest. An empty date opens the oldest one, which is the default. */
export async function openChest(date = ''): Promise<ChestOpenResult> {
  const res = await postJSON('/api/chest/open', date ? { date } : {})
  const data = (await res.json()) as Partial<ChestOpenResult>
  return {
    opened: data.opened ?? '',
    count: data.count ?? 0,
    drops: data.drops ?? [],
    chests: data.chests ?? [],
  }
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
