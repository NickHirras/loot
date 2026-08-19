import type { Drop, SourceInfo, Stats } from './types'

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: 'application/json' } })
  if (!res.ok) throw new Error(`${path}: ${res.status} ${res.statusText}`)
  return (await res.json()) as T
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

/** Fires a synthetic drop. Only mounted when the server runs with dev enabled. */
export async function fireFakeDrop(rarity: string, country = ''): Promise<void> {
  const res = await fetch('/api/dev/fake', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ rarity, country }),
  })
  if (!res.ok) throw new Error(`dev/fake: ${res.status} ${res.statusText}`)
}

/** The websocket URL for this page, honouring https and any base path. */
export function websocketURL(): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${location.host}/ws`
}
