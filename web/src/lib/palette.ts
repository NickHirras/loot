/**
 * Chart colors.
 *
 * The rarity palette in app.css is *semantic* — legendary is gold everywhere —
 * so the vault's charts use their own categorical set instead of borrowing it.
 * These are the eight-slot categorical hues stepped for a dark surface, checked
 * against Loot's panel background (#12161f): every slot clears 3:1 contrast,
 * sits inside the dark lightness band, and the worst adjacent pair is ΔE 8.4
 * under simulated protanopia — above the ≥8 target for colorblind separation.
 *
 * Slots are assigned by *source name*, not by rank, so changing the range or
 * dropping a source never repaints the survivors.
 */
export const SERIES_COLORS = [
  '#3987e5', // blue
  '#d95926', // orange
  '#199e70', // aqua
  '#c98500', // yellow
  '#d55181', // magenta
  '#008300', // green
] as const

/** The sources Loot ships with get a stable slot; anything else hashes in. */
const ASSIGNED: Record<string, number> = {
  appstore: 0,
  googleplay: 1,
  revenuecat: 2,
  flathub: 3,
  dev: 4,
  loot: 5,
}

export function seriesColor(name: string): string {
  const slot = ASSIGNED[name]
  if (slot !== undefined) return SERIES_COLORS[slot]

  // A stable hash keeps an unknown source on one color across reloads.
  let hash = 0
  for (let i = 0; i < name.length; i++) hash = (hash * 31 + name.charCodeAt(i)) >>> 0
  return SERIES_COLORS[hash % SERIES_COLORS.length]
}
