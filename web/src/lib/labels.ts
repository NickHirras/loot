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
