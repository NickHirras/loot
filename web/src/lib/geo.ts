/**
 * The Hearth's geography: where a country is, and what it is called.
 *
 * Loot ships one map — Natural Earth's 110m country boundaries, via the
 * `world-atlas` package (~39 KB gzipped) — and derives everything else from
 * it, so there is no second table of coordinates to drift out of date. A
 * settlement's position is `d3.geoCentroid` of that country's polygon, with a
 * short override list for the cases where a centroid is the wrong answer.
 */
import { geoCentroid } from 'd3-geo'
import type { ExtendedFeature } from 'd3-geo'
import type { Feature, FeatureCollection, Geometry, MultiLineString } from 'geojson'
import topology from 'world-atlas/countries-110m.json'
import { feature, mesh } from 'topojson-client'
import type { GeometryCollection, Topology } from 'topojson-specification'

/**
 * ISO 3166-1 alpha-2 to numeric, packed five characters per country
 * ("US840"), because 250 object literal entries is a page of noise for what is
 * a lookup table. The numeric code is how a country is addressed in the
 * topology, whose feature ids are zero-padded numeric strings.
 */
const PACKED_ISO =
  'AD020AE784AF004AG028AI660AL008AM051AO024AQ010AR032AS016AT040AU036AW533AX248AZ031' +
  'BA070BB052BD050BE056BF854BG100BH048BI108BJ204BL652BM060BN096BO068BQ535BR076BS044' +
  'BT064BV074BW072BY112BZ084CA124CC166CD180CF140CG178CH756CI384CK184CL152CM120CN156' +
  'CO170CR188CU192CV132CW531CX162CY196CZ203DE276DJ262DK208DM212DO214DZ012EC218EE233' +
  'EG818EH732ER232ES724ET231FI246FJ242FK238FM583FO234FR250GA266GB826GD308GE268GF254' +
  'GG831GH288GI292GL304GM270GN324GP312GQ226GR300GS239GT320GU316GW624GY328HK344HM334' +
  'HN340HR191HT332HU348ID360IE372IL376IM833IN356IO086IQ368IR364IS352IT380JE832JM388' +
  'JO400JP392KE404KG417KH116KI296KM174KN659KP408KR410KW414KY136KZ398LA418LB422LC662' +
  'LI438LK144LR430LS426LT440LU442LV428LY434MA504MC492MD498ME499MF663MG450MH584MK807' +
  'ML466MM104MN496MO446MP580MQ474MR478MS500MT470MU480MV462MW454MX484MY458MZ508NA516' +
  'NC540NE562NF574NG566NI558NL528NO578NP524NR520NU570NZ554OM512PA591PE604PF258PG598' +
  'PH608PK586PL616PM666PN612PR630PS275PT620PW585PY600QA634RE638RO642RS688RU643RW646' +
  'SA682SB090SC690SD729SE752SG702SH654SI705SJ744SK703SL694SM674SN686SO706SR740SS728' +
  'ST678SV222SX534SY760SZ748TC796TD148TF260TG768TH764TJ762TK772TL626TM795TN788TO776' +
  'TR792TT780TV798TW158TZ834UA804UG800UM581US840UY858UZ860VA336VC670VE862VG092VI850' +
  'VN704VU548WF876WS882XK983YE887YT175ZA710ZM894ZW716'


const ISO_NUMERIC = new Map<string, string>()
for (let i = 0; i + 5 <= PACKED_ISO.length; i += 5) {
  ISO_NUMERIC.set(PACKED_ISO.slice(i, i + 2), PACKED_ISO.slice(i + 2, i + 5))
}

/**
 * Positions the atlas gets wrong, as [longitude, latitude].
 *
 * Two kinds of entry live here. The first is a country whose centroid is not
 * inside its own borders — an archipelago (Indonesia, the Philippines, Fiji),
 * a country bent around a bay (Croatia, Vietnam) or one with distant overseas
 * territory (France, whose centroid sits in the Atlantic). The second is a
 * country whose centroid is technically correct but visually wrong: the United
 * States is dragged towards Alaska, Canada towards the Arctic, so their
 * settlements are placed where the customers actually are.
 *
 * Everything else is computed, so this list only ever grows when something
 * looks wrong on screen.
 */
const CENTROID_OVERRIDES: Record<string, [number, number]> = {
  BS: [-77.35, 24.25],
  CA: [-96.0, 54.5],
  FJ: [178.0, -17.75],
  FR: [2.4, 46.6],
  HR: [16.0, 45.6],
  HT: [-72.3, 19.0],
  ID: [110.0, -6.9],
  IL: [34.85, 31.9],
  JP: [138.2, 36.2],
  MY: [101.7, 3.14],
  NO: [9.5, 61.2],
  NZ: [174.78, -41.29],
  PH: [121.0, 14.6],
  SB: [160.0, -9.45],
  US: [-98.5, 39.5],
  VN: [105.85, 21.03],
}

/**
 * Places the 110m atlas has never heard of: microstates, city states and small
 * islands, all of which can absolutely be your best country. Without these,
 * a Singaporean or Maltese customer would have nowhere to stand.
 */
const MISSING_CENTROIDS: Record<string, [number, number]> = {
  AD: [1.52, 42.51],
  AG: [-61.8, 17.06],
  AI: [-63.07, 18.22],
  AS: [-170.7, -14.31],
  AW: [-69.97, 12.52],
  AX: [19.95, 60.18],
  BB: [-59.54, 13.19],
  BH: [50.55, 26.07],
  BL: [-62.83, 17.9],
  BM: [-64.75, 32.32],
  BQ: [-68.26, 12.18],
  BV: [3.36, -54.42],
  CC: [96.87, -12.16],
  CK: [-159.78, -21.24],
  CV: [-23.62, 15.12],
  CW: [-68.93, 12.17],
  CX: [105.69, -10.49],
  DM: [-61.37, 15.41],
  FM: [158.19, 6.92],
  FO: [-6.91, 61.89],
  GD: [-61.68, 12.12],
  GF: [-53.13, 3.93],
  GG: [-2.58, 49.45],
  GI: [-5.35, 36.14],
  GP: [-61.55, 16.27],
  GS: [-36.59, -54.43],
  GU: [144.79, 13.44],
  HK: [114.17, 22.32],
  HM: [73.51, -53.08],
  IM: [-4.55, 54.24],
  IO: [72.42, -7.34],
  JE: [-2.11, 49.21],
  KI: [172.98, 1.45],
  KM: [43.33, -11.65],
  KN: [-62.73, 17.36],
  KY: [-81.25, 19.31],
  LC: [-60.98, 13.91],
  LI: [9.55, 47.17],
  MC: [7.42, 43.74],
  MF: [-63.06, 18.08],
  MH: [171.18, 7.13],
  MO: [113.54, 22.2],
  MP: [145.75, 15.19],
  MQ: [-61.02, 14.64],
  MS: [-62.19, 16.74],
  MT: [14.38, 35.94],
  MU: [57.55, -20.35],
  MV: [73.51, 3.2],
  NF: [167.95, -29.04],
  NR: [166.93, -0.52],
  NU: [-169.87, -19.05],
  PF: [-149.43, -17.68],
  PM: [-56.33, 46.94],
  PN: [-128.32, -24.38],
  PW: [134.58, 7.51],
  RE: [55.54, -21.12],
  SC: [55.49, -4.68],
  SG: [103.82, 1.35],
  SH: [-5.72, -15.96],
  SJ: [17.88, 78.22],
  SM: [12.46, 43.94],
  ST: [6.61, 0.19],
  SX: [-63.05, 18.04],
  TC: [-71.8, 21.72],
  TK: [-171.86, -9.2],
  TO: [-175.2, -21.18],
  TV: [179.19, -8.52],
  UM: [-177.38, 28.2],
  VA: [12.45, 41.9],
  VC: [-61.22, 13.25],
  VG: [-64.62, 18.42],
  VI: [-64.8, 18.34],
  WF: [-177.16, -13.77],
  WS: [-172.1, -13.76],
  XK: [20.9, 42.6],
  YT: [45.17, -12.83],
}

/**
 * The atlas, retyped. The published JSON is a plain object; naming its shape
 * here is what lets `feature` and `mesh` be called without casts at every use.
 */
type CountryProperties = { name?: string }
const topo = topology as unknown as Topology<{ countries: GeometryCollection<CountryProperties> }>

/** Every country as a GeoJSON feature — the filled land of the globe. */
export const countries: FeatureCollection<Geometry, CountryProperties> = feature(
  topo,
  topo.objects.countries,
) as FeatureCollection<Geometry, CountryProperties>

/**
 * Interior borders only. Drawing shared borders once (rather than once per
 * country) keeps them a hairline instead of doubling up into a seam.
 */
export const borders: MultiLineString = mesh(topo, topo.objects.countries, (a, b) => a !== b)

const byNumeric = new Map<string, Feature<Geometry, CountryProperties>>()
for (const f of countries.features) {
  if (f.id !== undefined && f.id !== null) byNumeric.set(String(f.id), f)
}

const centroidCache = new Map<string, [number, number] | null>()

/**
 * Where a country's settlement stands, as [longitude, latitude], or null when
 * Loot has no idea where the country is (an unknown or made-up code).
 */
export function centroidOf(iso2: string): [number, number] | null {
  const code = iso2.toUpperCase()
  const cached = centroidCache.get(code)
  if (cached !== undefined) return cached

  let point: [number, number] | null = CENTROID_OVERRIDES[code] ?? MISSING_CENTROIDS[code] ?? null
  if (!point) {
    const numeric = ISO_NUMERIC.get(code)
    const shape = numeric ? byNumeric.get(numeric) : undefined
    if (shape) {
      // d3-geo's own loose feature type, which GeoJSON's strict one predates.
      const c = geoCentroid(shape as unknown as ExtendedFeature)
      if (Number.isFinite(c[0]) && Number.isFinite(c[1])) point = [c[0], c[1]]
    }
  }
  centroidCache.set(code, point)
  return point
}

/** The country's own polygon, for hit testing and highlights. */
export function shapeOf(iso2: string): Feature<Geometry, CountryProperties> | null {
  const numeric = ISO_NUMERIC.get(iso2.toUpperCase())
  if (!numeric) return null
  return byNumeric.get(numeric) ?? null
}

const displayNames = (() => {
  try {
    return new Intl.DisplayNames(undefined, { type: 'region' })
  } catch {
    return null
  }
})()

const nameCache = new Map<string, string>()

/**
 * The country's name in the reader's language. `Intl.DisplayNames` knows every
 * ISO 3166-1 code and localizes for free, which is why no name table ships
 * here; the atlas name is the fallback, and the bare code the last resort.
 */
export function countryName(iso2: string): string {
  const code = iso2.toUpperCase()
  const cached = nameCache.get(code)
  if (cached) return cached

  let name = ''
  try {
    const resolved = displayNames?.of(code)
    if (resolved && resolved !== code) name = resolved
  } catch {
    // An invalid code throws rather than returning undefined.
  }
  if (!name) name = shapeOf(code)?.properties?.name ?? code
  nameCache.set(code, name)
  return name
}
