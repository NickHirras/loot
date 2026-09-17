/**
 * Sentences the server used to write.
 *
 * A mystery's headline, a boss's subtitle, an auto quest's title, a line of
 * the recap's caption: every one of these was once English prose built in Go
 * and printed here verbatim. They are built here now, out of the structured
 * facts the API sends instead — a kind, a metric, a day, a count — so they can
 * be written in whatever language the dashboard is speaking.
 *
 * Two rules run through the whole file:
 *
 *   - **The stored text is the fallback.** A row written before this change
 *     carries the old English sentence and none of the facts, so every builder
 *     here answers with `undefined` when it cannot see what it needs, and the
 *     card prints what the server wrote. The same fallback covers a kind a
 *     newer server invented that this build has never heard of.
 *   - **The numbers are formatted the way Go formatted them**, because the
 *     English these produce has to be the English they replaced, down to the
 *     comma in "1,240".
 */
import { m } from '../paraglide/messages'
import { eraLabel } from './labels'
import { currentLocale } from './locale'
import { sourceLabel } from './sea'
import type { Boss, Mystery, Quest, RecapHighlight, RecapPeriod } from './types'
import { currency, dayLabel, flagEmoji, integer } from './types'

// ------------------------------------------------------------------- days

const weekdayFormats = new Map<string, Intl.DateTimeFormat>()

function weekdayFormat(): Intl.DateTimeFormat {
  const locale = currentLocale()
  let fmt = weekdayFormats.get(locale)
  if (!fmt) {
    fmt = new Intl.DateTimeFormat(locale, { weekday: 'short', timeZone: 'UTC' })
    weekdayFormats.set(locale, fmt)
  }
  return fmt
}

/**
 * A day with its weekday on the front: "Tue Aug 12".
 *
 * The weekday and the date are formatted separately and then put together by a
 * message, rather than asked of one `Intl` format with all three fields. That
 * is not a detour: `{weekday: 'short', month: 'short', day: 'numeric'}` renders
 * "Tue, Aug 12" in English — a comma Go never printed — and, more importantly,
 * a message is something a translator can reorder.
 */
export function weekdayDay(day: string): string {
  const parsed = new Date(`${day}T00:00:00Z`)
  if (Number.isNaN(parsed.getTime())) return day
  return m.mystery_day({ weekday: weekdayFormat().format(parsed), date: dayLabel(day) })
}

// -------------------------------------------------------------- mysteries

/**
 * How a series reads inside a sentence: `units` → "units sold".
 *
 * The identifiers are store.SeriesMetric's, so revenue arrives as the column
 * it is summed from: `revenue_base`.
 */
function seriesLabel(metric: string): string {
  switch (metric) {
    case 'installs':
      return m.mystery_series_installs()
    case 'units':
      return m.mystery_series_units()
    case 'revenue_base':
      return m.mystery_series_revenue()
    case 'refunds':
      return m.mystery_series_refunds()
    case 'cancellations':
      return m.mystery_series_cancellations()
    default:
      return metric
  }
}

/**
 * A move in the words a person would use: tripled, jumped 60%, dropped 70%.
 *
 * Mirrors `phrase` in internal/mysteries/detect.go, thresholds and rounding
 * included — `Math.round` matches Go's `%.0f` for the positive ratios this is
 * ever handed.
 */
function movement(observed: number, expected: number): string {
  if (expected <= 0) {
    return observed > 0 ? m.mystery_phrase_appeared() : m.mystery_phrase_changed()
  }
  const ratio = observed / expected
  if (ratio >= 4) return m.mystery_phrase_quadrupled()
  if (ratio >= 3) return m.mystery_phrase_tripled()
  if (ratio >= 2) return m.mystery_phrase_doubled()
  if (ratio > 1) return m.mystery_phrase_jumped({ pct: Math.round((ratio - 1) * 100) })
  if (ratio === 1) return m.mystery_phrase_held_still()
  return m.mystery_phrase_dropped({ pct: Math.round((1 - ratio) * 100) })
}

const zFormats = new Map<string, Intl.NumberFormat>()

/**
 * A z-score to one decimal, rounded the way Go's `%.1f` rounds it and
 * separated the way the reader's language separates decimals.
 *
 * The rounding is worth the care. `toFixed` rounds a halfway value away from
 * zero and Go rounds it to even, so a z of 4.25 read "4.2" before this change
 * and would read "4.3" after it. The API sends z to two decimals, which makes
 * the halfway values exactly x.25 and x.75 — the only two-decimal fractions a
 * float can hold exactly — and `n * 4` being a whole number while `n * 2` is
 * not is an exact test for them, because multiplying by a power of two is the
 * one piece of float arithmetic that never rounds.
 */
function zLabel(z: number): string {
  const value = z ?? 0
  const abs = Math.abs(value)
  let rounded: number
  if (Number.isInteger(abs * 4) && !Number.isInteger(abs * 2)) {
    const below = Math.floor(abs * 10)
    rounded = (below % 2 === 0 ? below : below + 1) / 10
  } else {
    rounded = Number(abs.toFixed(1))
  }

  const locale = currentLocale()
  let fmt = zFormats.get(locale)
  if (!fmt) {
    fmt = new Intl.NumberFormat(locale, { minimumFractionDigits: 1, maximumFractionDigits: 1 })
    zFormats.set(locale, fmt)
  }
  return fmt.format(value < 0 ? -rounded : rounded)
}

/** "Loot" for a mystery Loot raised about itself, a brand name otherwise. */
function mysterySource(source: string): string {
  return source ? sourceLabel(source) : sourceLabel('loot')
}

/** Money or a count, exactly as the detector chose between them. */
function mysteryValue(mystery: Mystery, value: number, code: string): string {
  const money = mystery.detail?.unit === 'money' || (!mystery.detail && mystery.metric === 'revenue_base')
  return money ? currency(value, code, 0) : integer(value)
}

/** The headline, phrased as a question wherever it really is one. */
export function mysteryTitle(mystery: Mystery, code: string): string {
  const source = mysterySource(mystery.source)
  const series = seriesLabel(mystery.metric)
  const day = weekdayDay(mystery.day)

  switch (mystery.kind) {
    case 'spike':
      return m.mystery_title_spike({
        source,
        series,
        phrase: movement(mystery.observed, mystery.expected),
        day,
      })
    case 'dip':
      return m.mystery_title_dip({
        source,
        series,
        phrase: movement(mystery.observed, mystery.expected),
        day,
      })
    case 'refund_spike':
      return m.mystery_title_refund_spike({
        count: integer(mystery.observed),
        source,
        day,
        usual: integer(mystery.expected),
      })
    case 'record':
      return m.mystery_title_record({
        value: mysteryValue(mystery, mystery.observed, code),
        series,
        source,
        day,
      })
    case 'new_country_cluster':
      return m.mystery_title_new_country_cluster({
        count: mystery.observed,
        countText: integer(mystery.observed),
        day,
      })
    case 'silence':
      return m.mystery_title_silence({ source, day })
    default:
      // A kind this build has never heard of: the server's own headline.
      return mystery.title
  }
}

/**
 * Why the detector asked, or `undefined` when the mystery predates the facts
 * the sentence needs and only the stored line can answer.
 */
export function mysteryWhy(mystery: Mystery, code: string): string | undefined {
  const detail = mystery.detail
  if (!detail) return undefined

  switch (mystery.kind) {
    case 'refund_spike':
      return m.mystery_why_refund_spike({
        count: integer(mystery.observed),
        usual: integer(detail.baseline),
      })
    case 'record':
      return detail.baseline_days
        ? m.mystery_why_record({ count: detail.baseline_days })
        : undefined
    case 'new_country_cluster': {
      const countries = detail.countries || mystery.observed
      return m.mystery_why_new_country_cluster({ count: countries, countText: integer(countries) })
    }
    case 'silence':
      if (!detail.missing_days) return undefined
      return detail.lag_days
        ? m.mystery_why_silence_lag({
            count: detail.missing_days,
            reported: detail.reported_days ?? 0,
            lag: detail.lag_days,
          })
        : m.mystery_why_silence({
            count: detail.missing_days,
            reported: detail.reported_days ?? 0,
          })
    case 'spike':
    case 'dip':
      return detail.baseline_days
        ? m.mystery_why_measured({
            observed: mysteryValue(mystery, mystery.observed, code),
            days: detail.baseline_days,
            expected: mysteryValue(mystery, detail.baseline, code),
            z: zLabel(mystery.z),
          })
        : undefined
    default:
      return undefined
  }
}

// ------------------------------------------------------------------ bosses

/**
 * The human line under the monster name.
 *
 * `issue_title` is the crash reporter's own words and is passed through as it
 * is; an empty one means Loot wrote the title itself, and that sentence is
 * rebuilt here from the version and the kind.
 */
export function bossTitle(boss: Boss): string {
  if (boss.issue_title) return boss.issue_title
  // An older server sends neither field; its title is already the sentence.
  if (boss.issue_title === undefined) return boss.title
  const anr = boss.kind === 'anr'
  if (!boss.version_label) return anr ? m.boss_title_anrs() : m.boss_title_crashes()
  return anr
    ? m.boss_title_anrs_in({ version: boss.version_label })
    : m.boss_title_crashes_in({ version: boss.version_label })
}

// ------------------------------------------------------------- auto quests

/**
 * How many new countries a generated month quest asks for, mirroring
 * `newCountriesTarget` in internal/quests/generate.go.
 *
 * It cannot be read off the quest: the target counts from the day the quest
 * was written ("two *more* countries"), so a quest set on the fourteenth has a
 * target of three and a title that still says two.
 */
const AUTO_NEW_COUNTRIES = 2

/** A window of exactly seven days is a week's quest; anything else is a month's. */
function windowDays(quest: Quest): number {
  return (
    Math.round(
      (Date.parse(`${quest.window_end}T00:00:00Z`) - Date.parse(`${quest.window_start}T00:00:00Z`)) / 86_400_000,
    ) + 1
  )
}

/**
 * A generated quest's title, or `undefined` for one this build cannot write.
 *
 * An auto quest is fully determined by its metric, its window and its target,
 * so nothing had to be stored for this: the generator writes seven titles and
 * these are the same seven. Anything else — a custom quest, a metric the
 * generator has learned since — falls back to the stored title.
 */
export function autoQuestTitle(quest: Quest, code: string): string | undefined {
  if (quest.kind !== 'auto') return undefined
  const target = quest.metric === 'revenue' ? currency(quest.target, code, 0) : integer(quest.target)
  const week = windowDays(quest) === 7

  switch (quest.metric) {
    case 'revenue':
      return week ? m.quest_auto_revenue_week({ target }) : m.quest_auto_revenue_month({ target })
    case 'units':
      return week ? m.quest_auto_units_week({ target }) : undefined
    case 'installs':
      return week ? m.quest_auto_installs_week({ target }) : undefined
    case 'stars':
      return week ? m.quest_auto_stars_week({ target }) : undefined
    case 'xp':
      return week ? m.quest_auto_xp_week({ target }) : undefined
    case 'settlements':
      return week ? undefined : m.quest_auto_settlements_month({ count: AUTO_NEW_COUNTRIES })
    default:
      return undefined
  }
}

// ---------------------------------------------------------------- the recap

const monthFormats = new Map<string, Intl.DateTimeFormat>()

function monthFormat(): Intl.DateTimeFormat {
  const locale = currentLocale()
  let fmt = monthFormats.get(locale)
  if (!fmt) {
    fmt = new Intl.DateTimeFormat(locale, { month: 'long', year: 'numeric', timeZone: 'UTC' })
    monthFormats.set(locale, fmt)
  }
  return fmt
}

/**
 * The window a recap covers, as the poster's headline reads it: "January 2026"
 * for a month, "2026" for a season.
 *
 * The API still sends `period.label`, and it still says exactly this — in
 * English. The card builds its own so the poster is in one language.
 */
export function periodLabel(period: RecapPeriod): string {
  if (period.kind === 'season') return period.key
  const parsed = new Date(`${period.from}T00:00:00Z`)
  if (Number.isNaN(parsed.getTime())) return period.key
  return monthFormat().format(parsed)
}

function str(args: Record<string, unknown> | undefined, key: string): string {
  const v = args?.[key]
  return typeof v === 'string' ? v : ''
}

function num(args: Record<string, unknown> | undefined, key: string): number {
  const v = args?.[key]
  return typeof v === 'number' ? v : 0
}

/**
 * One line of the poster's caption, or "" for a kind this build does not know.
 *
 * `title` is the English title the API sends with an `unlocked` highlight; it
 * is the fallback for a trophy whose key has no message here, exactly as it is
 * on the trophy wall.
 */
export function highlightLine(
  highlight: RecapHighlight,
  code: string,
  achievementTitle: (key: string, fallback: string) => string,
): string {
  const args = highlight.args
  switch (highlight.kind) {
    case 'best_day':
      return m.recap_highlight_best_day({
        day: dayLabel(str(args, 'day')),
        value: currency(num(args, 'value'), code, 0),
      })
    case 'unlocked': {
      const title = achievementTitle(str(args, 'key'), str(args, 'title'))
      const more = num(args, 'more')
      return more > 0
        ? m.recap_highlight_unlocked_more({ title, count: more })
        : m.recap_highlight_unlocked({ title })
    }
    case 'settled': {
      const country = str(args, 'country')
      const parts = { flag: flagEmoji(country), country, day: dayLabel(str(args, 'day')) }
      const more = num(args, 'more')
      return more > 0
        ? m.recap_highlight_settled_more({ ...parts, count: more })
        : m.recap_highlight_settled(parts)
    }
    case 'legendary_drops':
      return m.recap_highlight_legendary_drops({ count: num(args, 'count') })
    case 'epic_drops':
      return m.recap_highlight_epic_drops({ count: num(args, 'count') })
    case 'era_reached':
      return m.recap_highlight_era_reached({ era: eraLabel(str(args, 'era')) })
    case 'level_up':
      return m.recap_highlight_level_up({ from: num(args, 'from'), to: num(args, 'to') })
    case 'most_countries':
      return m.recap_highlight_most_countries({
        count: num(args, 'count'),
        day: dayLabel(str(args, 'day')),
      })
    case 'quests_completed':
      return m.recap_highlight_quests_completed({ count: num(args, 'count') })
    case 'mysteries_solved':
      return m.recap_highlight_mysteries_solved({ count: num(args, 'count') })
    case 'chests_opened':
      return m.recap_highlight_chests_opened({ count: num(args, 'count') })
    case 'top_country': {
      const country = str(args, 'country')
      return m.recap_highlight_top_country({ flag: flagEmoji(country), country })
    }
    default:
      return ''
  }
}
