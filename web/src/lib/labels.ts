/**
 * Identifiers that are also labels.
 *
 * A rarity, a settlement tier, an era, an achievement tier, a quest metric, a
 * mystery kind: the API sends an identifier and the UI has always printed it
 * raw, because in English `"legendary"` is both. Once the UI speaks more than
 * one language that stops being true, so each one gets a lookup here.
 *
 * Every lookup falls back to the identifier it was given. A rarity Loot has
 * never heard of — a newer server talking to an older tab, a rule file with a
 * tier of its own — then still renders as itself rather than as a blank, which
 * is exactly what it used to do.
 *
 * Source names (App Store, Flathub, RevenueCat) are deliberately absent: they
 * are brand names and are the same in every language.
 */
import { m } from '../paraglide/messages'
import type { Tab } from './route.svelte'
import type { Metric, MysteryKind, Rarity, Tier } from './types'

/** `common` → "common". */
export function rarityLabel(rarity: Rarity | string): string {
  switch (rarity) {
    case 'common':
      return m.rarity_common()
    case 'uncommon':
      return m.rarity_uncommon()
    case 'rare':
      return m.rarity_rare()
    case 'epic':
      return m.rarity_epic()
    case 'legendary':
      return m.rarity_legendary()
    case 'cursed':
      return m.rarity_cursed()
    default:
      return rarity
  }
}

/** A settlement's rung on the ladder: `outpost` → "outpost". */
export function tierLabel(tier: string): string {
  switch (tier) {
    case 'outpost':
      return m.tier_outpost()
    case 'hamlet':
      return m.tier_hamlet()
    case 'village':
      return m.tier_village()
    case 'town':
      return m.tier_town()
    case 'city':
      return m.tier_city()
    case 'metropolis':
      return m.tier_metropolis()
    default:
      return tier
  }
}

/** Where the whole account stands: `Kingdom` → "Kingdom". */
export function eraLabel(era: string): string {
  switch (era) {
    case 'Camp':
      return m.era_camp()
    case 'Village':
      return m.era_village()
    case 'Town':
      return m.era_town()
    case 'City':
      return m.era_city()
    case 'Kingdom':
      return m.era_kingdom()
    case 'Empire':
      return m.era_empire()
    case 'Dynasty':
      return m.era_dynasty()
    default:
      return era
  }
}

/** How grand a trophy is: `bronze` → "bronze". */
export function achievementTierLabel(tier: Tier | string): string {
  switch (tier) {
    case 'bronze':
      return m.ach_tier_bronze()
    case 'silver':
      return m.ach_tier_silver()
    case 'gold':
      return m.ach_tier_gold()
    case 'legendary':
      return m.ach_tier_legendary()
    default:
      return tier
  }
}

/** What a quest counts, as it reads in a sentence: `settlements` → "new countries". */
export function metricLabel(metric: Metric | string): string {
  switch (metric) {
    case 'revenue':
      return m.metric_revenue()
    case 'units':
      return m.metric_units()
    case 'installs':
      return m.metric_installs()
    case 'subscribers':
      return m.metric_subscribers()
    case 'drops':
      return m.metric_drops()
    case 'settlements':
      return m.metric_settlements()
    case 'stars':
      return m.metric_stars()
    case 'xp':
      return m.metric_xp()
    default:
      return metric
  }
}

/** How a flagged day reads on its card: `refund_spike` → "refunds". */
export function mysteryKindLabel(kind: MysteryKind | string): string {
  switch (kind) {
    case 'spike':
      return m.mystery_kind_spike()
    case 'dip':
      return m.mystery_kind_dip()
    case 'refund_spike':
      return m.mystery_kind_refund_spike()
    case 'record':
      return m.mystery_kind_record()
    case 'new_country_cluster':
      return m.mystery_kind_new_country_cluster()
    case 'silence':
      return m.mystery_kind_silence()
    default:
      return kind
  }
}

/** What a boss's HP counts: `crashes` or `users`. */
export function bossUnitLabel(unit: string): string {
  switch (unit) {
    case 'crashes':
      return m.boss_unit_crashes()
    case 'users':
      return m.boss_unit_users()
    default:
      return unit
  }
}

/** A navigation tab's name. */
export function tabLabel(tab: Tab | string): string {
  switch (tab) {
    case 'feed':
      return m.tab_feed()
    case 'vault':
      return m.tab_vault()
    case 'hearth':
      return m.tab_hearth()
    case 'quests':
      return m.tab_quests()
    case 'codex':
      return m.tab_codex()
    default:
      return tab
  }
}

/**
 * Every achievement title and description, by key.
 *
 * Paraglide compiles one function per message, so there is no `m[key]` to
 * index: a table like this one is what turns a key that only exists at runtime
 * back into a message. It is the price of a missing message being a build
 * error rather than a blank card, which is the trade phase 1 made.
 *
 * The API still sends the English title and description, and they are what a
 * key missing from this table falls back to — a newer server with a trophy
 * this build has never heard of shows it in English rather than not at all.
 */
const ACH_TITLES: Record<string, () => string> = {
  cartographer: () => m.ach_cartographer_title(),
  cursed_but_unbowed: () => m.ach_cursed_but_unbowed_title(),
  era_city: () => m.ach_era_city_title(),
  era_empire: () => m.ach_era_empire_title(),
  era_kingdom: () => m.ach_era_kingdom_title(),
  era_town: () => m.ach_era_town_title(),
  first_blood: () => m.ach_first_blood_title(),
  first_sale: () => m.ach_first_sale_title(),
  first_subscriber: () => m.ach_first_subscriber_title(),
  hoarder_1: () => m.ach_hoarder_1_title(),
  hoarder_2: () => m.ach_hoarder_2_title(),
  hoarder_3: () => m.ach_hoarder_3_title(),
  installs_100k: () => m.ach_installs_100k_title(),
  installs_10k: () => m.ach_installs_10k_title(),
  installs_1k: () => m.ach_installs_1k_title(),
  legendary_hunter: () => m.ach_legendary_hunter_title(),
  merchant_2: () => m.ach_merchant_2_title(),
  merchant_3: () => m.ach_merchant_3_title(),
  merchant_5: () => m.ach_merchant_5_title(),
  mysteries_10: () => m.ach_mysteries_10_title(),
  mysteries_1: () => m.ach_mysteries_1_title(),
  polyglot_1: () => m.ach_polyglot_1_title(),
  polyglot_2: () => m.ach_polyglot_2_title(),
  quests_10: () => m.ach_quests_10_title(),
  quests_1: () => m.ach_quests_1_title(),
  quests_50: () => m.ach_quests_50_title(),
  record_1: () => m.ach_record_1_title(),
  record_25: () => m.ach_record_25_title(),
  record_5: () => m.ach_record_5_title(),
  revenue_100: () => m.ach_revenue_100_title(),
  revenue_100k: () => m.ach_revenue_100k_title(),
  revenue_10k: () => m.ach_revenue_10k_title(),
  revenue_1k: () => m.ach_revenue_1k_title(),
  settler_1: () => m.ach_settler_1_title(),
  settler_2: () => m.ach_settler_2_title(),
  settler_3: () => m.ach_settler_3_title(),
  settler_4: () => m.ach_settler_4_title(),
  stars_1000: () => m.ach_stars_1000_title(),
  stars_100: () => m.ach_stars_100_title(),
  stars_10: () => m.ach_stars_10_title(),
  steady_30: () => m.ach_steady_30_title(),
  steady_7: () => m.ach_steady_7_title(),
  subscribers_1000: () => m.ach_subscribers_1000_title(),
  subscribers_100: () => m.ach_subscribers_100_title(),
  subscribers_10: () => m.ach_subscribers_10_title(),
  units_100: () => m.ach_units_100_title(),
  units_100k: () => m.ach_units_100k_title(),
  units_10k: () => m.ach_units_10k_title(),
  units_1k: () => m.ach_units_1k_title(),
}

const ACH_DESCS: Record<string, () => string> = {
  cartographer: () => m.ach_cartographer_desc(),
  cursed_but_unbowed: () => m.ach_cursed_but_unbowed_desc(),
  era_city: () => m.ach_era_city_desc(),
  era_empire: () => m.ach_era_empire_desc(),
  era_kingdom: () => m.ach_era_kingdom_desc(),
  era_town: () => m.ach_era_town_desc(),
  first_blood: () => m.ach_first_blood_desc(),
  first_sale: () => m.ach_first_sale_desc(),
  first_subscriber: () => m.ach_first_subscriber_desc(),
  hoarder_1: () => m.ach_hoarder_1_desc(),
  hoarder_2: () => m.ach_hoarder_2_desc(),
  hoarder_3: () => m.ach_hoarder_3_desc(),
  installs_100k: () => m.ach_installs_100k_desc(),
  installs_10k: () => m.ach_installs_10k_desc(),
  installs_1k: () => m.ach_installs_1k_desc(),
  legendary_hunter: () => m.ach_legendary_hunter_desc(),
  merchant_2: () => m.ach_merchant_2_desc(),
  merchant_3: () => m.ach_merchant_3_desc(),
  merchant_5: () => m.ach_merchant_5_desc(),
  mysteries_10: () => m.ach_mysteries_10_desc(),
  mysteries_1: () => m.ach_mysteries_1_desc(),
  polyglot_1: () => m.ach_polyglot_1_desc(),
  polyglot_2: () => m.ach_polyglot_2_desc(),
  quests_10: () => m.ach_quests_10_desc(),
  quests_1: () => m.ach_quests_1_desc(),
  quests_50: () => m.ach_quests_50_desc(),
  record_1: () => m.ach_record_1_desc(),
  record_25: () => m.ach_record_25_desc(),
  record_5: () => m.ach_record_5_desc(),
  revenue_100: () => m.ach_revenue_100_desc(),
  revenue_100k: () => m.ach_revenue_100k_desc(),
  revenue_10k: () => m.ach_revenue_10k_desc(),
  revenue_1k: () => m.ach_revenue_1k_desc(),
  settler_1: () => m.ach_settler_1_desc(),
  settler_2: () => m.ach_settler_2_desc(),
  settler_3: () => m.ach_settler_3_desc(),
  settler_4: () => m.ach_settler_4_desc(),
  stars_1000: () => m.ach_stars_1000_desc(),
  stars_100: () => m.ach_stars_100_desc(),
  stars_10: () => m.ach_stars_10_desc(),
  steady_30: () => m.ach_steady_30_desc(),
  steady_7: () => m.ach_steady_7_desc(),
  subscribers_1000: () => m.ach_subscribers_1000_desc(),
  subscribers_100: () => m.ach_subscribers_100_desc(),
  subscribers_10: () => m.ach_subscribers_10_desc(),
  units_100: () => m.ach_units_100_desc(),
  units_100k: () => m.ach_units_100k_desc(),
  units_10k: () => m.ach_units_10k_desc(),
  units_1k: () => m.ach_units_1k_desc(),
}

/** A trophy's name: "Settler III". */
export function achievementTitle(key: string, fallback: string): string {
  return ACH_TITLES[key]?.() ?? fallback
}

/** The one line under it: what it took. */
export function achievementDescription(key: string, fallback: string): string {
  return ACH_DESCS[key]?.() ?? fallback
}

/**
 * The noun a progress line reads in: "18 / 25 countries".
 *
 * `count` is what the noun has to agree with, and it is deliberately the
 * *target* rather than the current value: English says "1 / 5 countries", not
 * "1 / 5 country". Money units ("revenue") are here too — the numbers beside
 * them are formatted as money, but the noun is still a word.
 */
const ACH_UNITS: Record<string, (count: number) => string> = {
  countries: (count: number) => m.ach_unit_countries({ count }),
  continents: (count: number) => m.ach_unit_continents({ count }),
  chests: (count: number) => m.ach_unit_chests({ count }),
  currencies: (count: number) => m.ach_unit_currencies({ count }),
  days: (count: number) => m.ach_unit_days({ count }),
  revenue: (count: number) => m.ach_unit_revenue({ count }),
  units: (count: number) => m.ach_unit_units({ count }),
  installs: (count: number) => m.ach_unit_installs({ count }),
  subscribers: (count: number) => m.ach_unit_subscribers({ count }),
  quests: (count: number) => m.ach_unit_quests({ count }),
  mysteries: (count: number) => m.ach_unit_mysteries({ count }),
  stars: (count: number) => m.ach_unit_stars({ count }),
  stores: (count: number) => m.ach_unit_stores({ count }),
  record_days: (count: number) => m.ach_unit_record_days({ count }),
  "XP": (count: number) => m.ach_unit_xp({ count }),
}

/** `record_days` → "record days", and an unknown unit as itself. */
export function unitLabel(unit: string, count: number): string {
  return ACH_UNITS[unit]?.(count) ?? unit
}

/**
 * A refused quest request, in the reader's language.
 *
 * The API answers with a sentence *and* a stable code (see internal/quests).
 * The code is what can be translated; the sentence is the fallback, for a
 * refusal this build has no message for and for the ones that carry no code at
 * all. `value` is the offending input, for the two refusals that quote one.
 */
export function questErrorLabel(code: string, fallback: string, value = ""): string {
  switch (code) {
    case 'unknown_metric':
      return m.quest_error_unknown_metric({ value })
    case 'target_positive':
      return m.quest_error_target_positive()
    case 'unknown_window':
      return m.quest_error_unknown_window({ value })
    case 'window_start_format':
      return m.quest_error_window_start_format()
    case 'window_end_format':
      return m.quest_error_window_end_format()
    case 'window_order':
      return m.quest_error_window_order()
    case 'custom_only':
      return m.quest_error_custom_only()
    case 'quests_disabled':
      return m.quest_error_quests_disabled()
    case 'not_found':
      return m.quest_error_not_found()
    case 'bad_window_json':
      return m.quest_error_bad_window_json()
    default:
      return fallback
  }
}
