/**
 * The fleet's geography: where a source's ship sits, and what it is called.
 *
 * Some sources count people but never say where they are — Flathub reports
 * installs per app and nothing more, and every store's per-country file lags
 * its overview file by a poll or two. Those people used to be one number
 * captioned "unknown lands". They are still position-less; this file just
 * gives each source a place to stand while it has nowhere to stand.
 *
 * An anchorage is therefore a *visualization*, not data: it says which source
 * could not place these people, and nothing whatsoever about where they are.
 * Nothing here is ever counted as a country.
 *
 * Every position is open ocean, at least 8° from its neighbours so two vessels
 * never overlap, and themed where the theme was too good to pass up.
 */

/** Where a vessel sits, as [longitude, latitude]. */
export type Anchorage = [number, number]

/** A ship, or — for a source with a homeland worth honouring — a rig. */
export type VesselKind = 'ship' | 'rig'

/**
 * The curated anchorages. Add a source here when it earns a joke; anything
 * missing hashes into SPARE_ANCHORAGES instead, which is a real position too,
 * just not a witty one.
 */
const ANCHORAGES: Record<string, Anchorage> = {
  // North Atlantic, halfway between the Old World and the New — which is
  // roughly where a Linux desktop user is, statistically speaking.
  flathub: [-35, 45],
  // The Central North Sea, off Ubuntu's homeland: an offshore rig, not a ship.
  snapcraft: [3, 57],
  // The mid-Pacific, north-west of Hawaii, pointed at Cupertino.
  appstore: [-152, 22],
  // The Indian Ocean, west of Sumatra.
  googleplay: [75, -12],
  // The South Atlantic, empty water between Brazil and Africa.
  microsoftstore: [-20, -25],
  // The Caribbean, where a cat would rather be.
  revenuecat: [-74, 16],
  // The Southern Ocean, south of the Cape: cold, remote, faintly heroic.
  github: [30, -60],
}

/**
 * Open water for everything else — a webhook, a source Loot has never heard
 * of, the dev panel. Four spots, hashed to, so an unknown source keeps the
 * same anchorage across reloads instead of wandering the globe.
 */
const SPARE_ANCHORAGES: Anchorage[] = [
  [-170, 40], // North Pacific
  [-120, -30], // South Pacific
  [-30, 5], // equatorial Atlantic
  [88, -30], // south-east Indian Ocean
]

/** Sources that float a platform rather than a hull. */
const RIGS = new Set(['snapcraft'])

/**
 * Source display names, mirroring internal/core's own table — the same names
 * the rules engine puts in drop titles, so a vessel is called what everything
 * else calls its source.
 */
const SOURCE_NAMES: Record<string, string> = {
  appstore: 'App Store',
  googleplay: 'Google Play',
  microsoftstore: 'Microsoft Store',
  snapcraft: 'Snapcraft',
  flathub: 'Flathub',
  revenuecat: 'RevenueCat',
  github: 'GitHub',
  webhook: 'webhook',
  playvitals: 'Play vitals',
  sentry: 'Sentry',
  crash: 'crash reports',
  loot: 'Loot',
  dev: 'dev',
}

/** The ships' own names. Anything else is "The <Source> Vessel". */
const VESSEL_NAMES: Record<string, string> = {
  flathub: 'The Flathub Freighter',
  snapcraft: 'Snapcraft Platform Nine',
  appstore: 'The Cupertino Clipper',
  googleplay: 'The Play Trawler',
  microsoftstore: 'The Redmond Barge',
  revenuecat: 'The RevenueCat Cutter',
  github: 'The Octocat Icebreaker',
}

/** A stable hash, so an unlisted source keeps one anchorage forever. */
function hash(name: string): number {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0
  return h
}

/** Where this source's vessel sits, as [longitude, latitude]. */
export function anchorageOf(source: string): Anchorage {
  return ANCHORAGES[source] ?? SPARE_ANCHORAGES[hash(source) % SPARE_ANCHORAGES.length]
}

/** A source's human name, falling back to its own id. */
export function sourceLabel(source: string): string {
  return SOURCE_NAMES[source] ?? source
}

/** What this source's vessel is called. */
export function vesselName(source: string): string {
  return VESSEL_NAMES[source] ?? `The ${sourceLabel(source)} Vessel`
}

/** Ship or rig. */
export function vesselKind(source: string): VesselKind {
  return RIGS.has(source) ? 'rig' : 'ship'
}

/** The one line that explains why anybody is out here at all. */
export function vesselWhy(source: string): string {
  return `somewhere at sea — ${sourceLabel(source)} doesn't say which country its people are from`
}
