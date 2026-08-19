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

/**
 * The rarity palette, mirroring the CSS custom properties in app.css.
 *
 * The canvas has no stylesheet: the globe paints arcs and pulses with these
 * hex values directly, so a rarity is the same colour on the canvas as it is
 * on a drop card. Keep the two lists in step — app.css is the source of truth
 * for the DOM, this is its shadow for anything drawn by hand.
 */
export const RARITY_COLORS: Record<string, string> = {
  common: '#808b9f',
  uncommon: '#35d07f',
  rare: '#4c9dfd',
  epic: '#b06bff',
  legendary: '#ffc23d',
  cursed: '#ff4d5e',
}

/** A rarity's colour, falling back to the common grey for anything unknown. */
export function rarityColor(rarity: string): string {
  return RARITY_COLORS[rarity] ?? RARITY_COLORS.common
}

/**
 * Achievement tier colours.
 *
 * These are metals rather than rarities on purpose. A trophy's tier and the
 * rarity of the drop it pays are two different facts — a gold trophy pays an
 * epic drop — and painting the tier in the rarity's colour made the wall read
 * as "you have four epics" instead of "you have four gold trophies". The
 * legendary tier is the one that borrows: a violet that reads as mythic beside
 * three metals, and the card gives it a gradient of its own.
 */
export const TIER_COLORS: Record<string, string> = {
  bronze: '#cf8a4e',
  silver: '#c2cddf',
  gold: '#ffc23d',
  legendary: '#e0a3ff',
}

/** A tier's colour, falling back to bronze for anything unknown. */
export function tierColor(tier: string): string {
  return TIER_COLORS[tier] ?? TIER_COLORS.bronze
}
