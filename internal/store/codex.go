package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// The Codex's persistence and its two read models.
//
// Only achievements are stored: a trophy has to survive the numbers moving on,
// so "you settled your tenth country" is a row, not a query. Everything else —
// records, lifetime totals, the season recap — is computed on read from the
// events and drops already in the database, for the same reason the vault is:
// a derived number can never drift out of step with the money beside it, and a
// restated report improves a record instead of leaving a stale one behind.
//
// The aggregates below are deliberately shaped as *day series*. One grouped
// query per family answers every achievement threshold, every record and every
// lifetime total at once, and a series also dates a threshold: the day the
// running total first crossed 25 countries is the day that trophy was earned,
// which is what lets a backfill unlock with the date it actually happened.

// ---------------------------------------------------------------- storage

const achievementColumns = `id, key, tier, title, description, unlocked_at,
       progress_value, progress_target, meta, created_at`

// UpsertAchievement stores a catalog entry's current state. It is written to be
// safe to run on every evaluation pass:
//
//   - a new key is inserted locked, with its progress;
//   - a locked key has its title, description and progress refreshed, so
//     editing the catalog's wording updates the wall;
//   - an *unlocked* key keeps its unlocked_at, its meta and its progress
//     untouched. A trophy is permanent, and its progress line is frozen at the
//     moment it was won.
func (s *Store) UpsertAchievement(ctx context.Context, a core.Achievement, now time.Time) error {
	if a.Key == "" {
		return errors.New("achievement key is required")
	}
	id := a.ID
	if id == "" {
		id = core.NewID()
	}
	_, err := s.q.ExecContext(ctx, `
        INSERT INTO achievements (`+achievementColumns+`)
        VALUES (?, ?, ?, ?, ?, NULL, ?, ?, NULL, ?)
        ON CONFLICT(key) DO UPDATE SET
            tier            = excluded.tier,
            title           = excluded.title,
            description     = excluded.description,
            progress_value  = CASE WHEN achievements.unlocked_at IS NULL
                                   THEN excluded.progress_value
                                   ELSE achievements.progress_value END,
            progress_target = excluded.progress_target`,
		id, a.Key, string(a.Tier), a.Title, a.Description,
		a.ProgressValue, a.ProgressTarget, now.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("upsert achievement %s: %w", a.Key, err)
	}
	return nil
}

// UnlockAchievement stamps an unlock, but only if the achievement is still
// locked. The UPDATE's own WHERE clause is what guarantees one drop per
// achievement forever: two racing evaluations, a restart mid-flight or a
// catalog re-run all lose the race and mint nothing.
//
// `at` is when the achievement was *earned*, not when Loot noticed.
func (s *Store) UnlockAchievement(ctx context.Context, key string, at time.Time, meta []byte, value float64) (bool, error) {
	if len(meta) == 0 {
		meta = nil
	}
	res, err := s.q.ExecContext(ctx, `
        UPDATE achievements
        SET unlocked_at = ?, meta = ?, progress_value = MAX(progress_value, ?)
        WHERE key = ? AND unlocked_at IS NULL`,
		at.UTC().UnixMilli(), meta, value, key)
	if err != nil {
		return false, fmt.Errorf("unlock achievement %s: %w", key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("unlock achievement rows: %w", err)
	}
	return n > 0, nil
}

// ListAchievements returns every stored achievement: unlocked first (newest
// unlock first), then locked ones by how close they are to their target, so
// the wall reads as "what you have won, then what is nearly won".
func (s *Store) ListAchievements(ctx context.Context) ([]core.Achievement, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+achievementColumns+` FROM achievements`)
	if err != nil {
		return nil, fmt.Errorf("list achievements: %w", err)
	}
	defer rows.Close()

	out := []core.Achievement{}
	for rows.Next() {
		var (
			a          core.Achievement
			tier       string
			unlockedAt sql.NullInt64
			meta       []byte
			createdAt  int64
		)
		if err := rows.Scan(&a.ID, &a.Key, &tier, &a.Title, &a.Description, &unlockedAt,
			&a.ProgressValue, &a.ProgressTarget, &meta, &createdAt); err != nil {
			return nil, fmt.Errorf("scan achievement: %w", err)
		}
		a.Tier = core.AchievementTier(tier)
		if unlockedAt.Valid {
			t := time.UnixMilli(unlockedAt.Int64).UTC()
			a.UnlockedAt = &t
		}
		if len(meta) > 0 {
			a.Meta = append([]byte(nil), meta...)
		}
		a.Pct = core.QuestPct(a.ProgressValue, a.ProgressTarget)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate achievements: %w", err)
	}
	return out, nil
}

// CountAchievements returns how many achievement rows exist and how many of
// them are unlocked. A zero total is what tells the evaluator that this is the
// first pass over this database, which is when a backfill is allowed.
func (s *Store) CountAchievements(ctx context.Context) (total, unlocked int, err error) {
	err = s.q.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(unlocked_at) FROM achievements`).Scan(&total, &unlocked)
	if err != nil {
		return 0, 0, fmt.Errorf("count achievements: %w", err)
	}
	return total, unlocked, nil
}

// AchievementsUnlockedBetween returns the achievements unlocked inside an
// inclusive day window, oldest first. It is what the season recap's trophy row
// reads.
func (s *Store) AchievementsUnlockedBetween(ctx context.Context, from, to string) ([]core.Achievement, error) {
	start, err := time.Parse(core.DayLayout, from)
	if err != nil {
		return nil, fmt.Errorf("bad from day %q: %w", from, err)
	}
	end, err := time.Parse(core.DayLayout, to)
	if err != nil {
		return nil, fmt.Errorf("bad to day %q: %w", to, err)
	}
	lo := start.UTC().UnixMilli()
	hi := end.AddDate(0, 0, 1).UTC().UnixMilli() // exclusive: the end day, whole

	rows, err := s.q.QueryContext(ctx, `SELECT `+achievementColumns+`
        FROM achievements
        WHERE unlocked_at IS NOT NULL AND unlocked_at >= ? AND unlocked_at < ?
        ORDER BY unlocked_at ASC, key ASC`, lo, hi)
	if err != nil {
		return nil, fmt.Errorf("achievements unlocked between: %w", err)
	}
	defer rows.Close()

	out := []core.Achievement{}
	for rows.Next() {
		var (
			a          core.Achievement
			tier       string
			unlockedAt sql.NullInt64
			meta       []byte
			createdAt  int64
		)
		if err := rows.Scan(&a.ID, &a.Key, &tier, &a.Title, &a.Description, &unlockedAt,
			&a.ProgressValue, &a.ProgressTarget, &meta, &createdAt); err != nil {
			return nil, fmt.Errorf("scan achievement: %w", err)
		}
		a.Tier = core.AchievementTier(tier)
		if unlockedAt.Valid {
			t := time.UnixMilli(unlockedAt.Int64).UTC()
			a.UnlockedAt = &t
		}
		if len(meta) > 0 {
			a.Meta = append([]byte(nil), meta...)
		}
		a.Pct = 1
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unlocked achievements: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------- aggregates

// DayValue is one day of a series.
type DayValue struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

// SourceDay is one (source, day) cell of the ledger.
type SourceDay struct {
	Source  string
	Day     string
	Revenue float64
	Units   int
	Refunds int
}

// DropDay is one day's worth of visible drops.
type DropDay struct {
	Day      string
	Count    int
	XP       int
	ByRarity map[string]int
	// Records counts the day's "best day ever" epics — a record-high sales_day
	// or installs_day summary, which is exactly what the record_high rules
	// mint an epic for. Several can land on one day (one per app per source);
	// the achievement counts *days*, so the snapshot flattens this to 0 or 1.
	Records int
}

// BestDrop is the single biggest drop Loot has ever minted, by XP.
type BestDrop struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Subtitle string      `json:"subtitle"`
	Rarity   core.Rarity `json:"rarity"`
	XP       int         `json:"xp"`
	Day      string      `json:"day"`
	Source   string      `json:"source"`
}

// CodexAggregates is one pass over the whole history, shaped so that every
// achievement threshold, every record and every lifetime total can be answered
// from it in Go without another query.
type CodexAggregates struct {
	// LedgerDays is revenue and units per (source, day), the vault's rule
	// exactly: ledger rows only, sales_day summaries excluded, signed.
	LedgerDays []SourceDay
	// InstallDays is installs per day, resolving the overview/per-country
	// double count the same way quests and the Hearth do.
	InstallDays []DayValue
	// DropDays is visible drops, XP and rarities per business day.
	DropDays []DropDay
	// CountryFirstDay maps ISO2 to the day its first ever event landed.
	CountryFirstDay map[string]string
	// CurrencyFirstDay maps a currency code to the day it was first seen on a
	// non-zero amount.
	CurrencyFirstDay map[string]string
	// LedgerSourceFirstDay maps a source to the first day it reported ledger
	// money, so "sell on three stores" can be dated.
	LedgerSourceFirstDay map[string]string
	// ChestDays counts chests opened, keyed by the day they were opened.
	ChestDays []DayValue
	// QuestDays and MysteryDays count completions and solutions per day.
	QuestDays   []DayValue
	MysteryDays []DayValue
	// StarDays counts GitHub stars per day: one entry per star *event*, so it
	// only knows about stars that arrived while Loot was watching.
	StarDays []DayValue
	// StarTotalDays is the reported stargazer level per day, summed over the
	// repos that reported it — how many stars the repos actually have.
	StarTotalDays []DayValue
	// SubscriberDays is the reported active subscriber level per day.
	SubscriberDays []DayValue

	// Point facts, all business days ("" when they have never happened).
	FirstEventDay      string
	FirstDropDay       string
	FirstSaleDay       string
	FirstSubscriberDay string
	FirstLegendaryDay  string
	// CursedRecoveryDay is the day a cursed drop was followed by a rare or
	// better inside 24 hours — the day it turned around.
	CursedRecoveryDay string

	// BestDrop is the biggest single drop by XP, or nil.
	BestDrop *BestDrop
	// ChestsOpened is how many chests have ever been opened.
	ChestsOpened int
}

// CodexAggregates walks the whole history in a dozen grouped queries. It is
// called by the Codex evaluator (debounced after ingest, and every ten
// minutes) and by GET /api/codex behind a five second cache, so it is written
// to group rather than to scan per achievement.
func (s *Store) CodexAggregates(ctx context.Context) (CodexAggregates, error) {
	var agg CodexAggregates
	var err error

	if agg.LedgerDays, err = s.codexLedgerDays(ctx); err != nil {
		return agg, err
	}
	if agg.InstallDays, err = s.codexInstallDays(ctx); err != nil {
		return agg, err
	}
	if agg.DropDays, err = s.codexDropDays(ctx); err != nil {
		return agg, err
	}
	if agg.CountryFirstDay, err = s.codexFirstDays(ctx,
		`SELECT country, MIN(day) FROM events WHERE country <> '' GROUP BY country`); err != nil {
		return agg, err
	}
	if agg.CurrencyFirstDay, err = s.codexFirstDays(ctx,
		`SELECT UPPER(currency), MIN(day) FROM events
         WHERE currency <> '' AND amount <> 0 GROUP BY UPPER(currency)`); err != nil {
		return agg, err
	}
	if agg.LedgerSourceFirstDay, err = s.codexFirstDays(ctx,
		`SELECT e.source, MIN(e.day) FROM events e
         WHERE `+ledgerRows+` AND e.amount_base <> 0 GROUP BY e.source`); err != nil {
		return agg, err
	}
	if agg.ChestDays, err = s.codexDayValues(ctx, `
        SELECT strftime('%Y-%m-%d', revealed_at / 1000, 'unixepoch') AS d,
               COUNT(DISTINCT chest_date)
        FROM drops WHERE revealed_at IS NOT NULL AND chest_date <> ''
        GROUP BY d ORDER BY d`); err != nil {
		return agg, err
	}
	for _, c := range agg.ChestDays {
		agg.ChestsOpened += int(c.Value)
	}
	if agg.QuestDays, err = s.codexDayValues(ctx, `
        SELECT day, COUNT(*) FROM events
        WHERE source = 'loot' AND kind = 'quest_complete' GROUP BY day ORDER BY day`); err != nil {
		return agg, err
	}
	if agg.MysteryDays, err = s.codexDayValues(ctx, `
        SELECT day, COUNT(*) FROM events
        WHERE source = 'loot' AND kind = 'mystery_solved' GROUP BY day ORDER BY day`); err != nil {
		return agg, err
	}
	if agg.StarDays, err = s.codexDayValues(ctx, `
        SELECT day, COALESCE(SUM(CASE WHEN quantity > 0 THEN quantity ELSE 1 END), 0)
        FROM events WHERE kind = 'star' GROUP BY day ORDER BY day`); err != nil {
		return agg, err
	}
	// Star *events* only exist for stars Loot watched arrive. The daily
	// `stars_total` snapshot is where the repo actually stands, which is what
	// keeps a repo that had three thousand stars before Loot was installed
	// from reading as having none. A level, so the day's figure is the sum
	// over the repos that reported it.
	if agg.StarTotalDays, err = s.codexDayValues(ctx, `
        SELECT day, COALESCE(SUM(v), 0) FROM (
            SELECT day AS day, MAX(quantity) AS v FROM events
            WHERE kind = 'stars_total' GROUP BY app, day)
        GROUP BY day ORDER BY day`); err != nil {
		return agg, err
	}
	// A snapshot is a level, not a flow: a day's subscribers is the sum over
	// the apps that reported that day, and the achievement takes the highest
	// level ever reached — a number that, once reached, was reached.
	if agg.SubscriberDays, err = s.codexDayValues(ctx, `
        SELECT day, COALESCE(SUM(quantity), 0) FROM events
        WHERE kind = 'subscription_snapshot' GROUP BY day ORDER BY day`); err != nil {
		return agg, err
	}

	firsts := []struct {
		into  *string
		query string
	}{
		{&agg.FirstEventDay, `SELECT MIN(day) FROM events`},
		// A drop still sealed in an unopened chest has not happened yet — the
		// same rule the feed and the XP counter use. Without it, an unlock
		// drop sitting in today's chest would hand out "your first legendary
		// drop" for a trophy nobody has seen.
		// Webhook test pings and Loot's own bookkeeping drops don't count as
		// "something to report" either.
		{&agg.FirstDropDay, `SELECT MIN(e.day) FROM drops d JOIN events e ON e.id = d.event_id
             WHERE NOT ` + unrevealed + ` AND e.kind <> 'test' AND e.source <> 'loot'`},
		{&agg.FirstSaleDay, `SELECT MIN(e.day) FROM events e
             WHERE ` + ledgerRows + ` AND e.kind IN ('sale', 'iap', 'subscription') AND e.quantity > 0`},
		{&agg.FirstSubscriberDay, `SELECT MIN(day) FROM events
             WHERE (source = 'revenuecat' AND kind = 'purchase')
                OR kind = 'subscription' OR kind = 'subscription_snapshot'`},
		{&agg.FirstLegendaryDay, `SELECT MIN(e.day) FROM drops d JOIN events e ON e.id = d.event_id
             WHERE NOT ` + unrevealed + ` AND d.rarity = 'legendary'`},
	}
	for _, f := range firsts {
		var day sql.NullString
		if err := s.q.QueryRowContext(ctx, f.query).Scan(&day); err != nil {
			return agg, fmt.Errorf("codex first day: %w", err)
		}
		*f.into = day.String
	}

	if agg.CursedRecoveryDay, err = s.codexCursedRecovery(ctx); err != nil {
		return agg, err
	}
	if agg.BestDrop, err = s.codexBestDrop(ctx); err != nil {
		return agg, err
	}
	return agg, nil
}

func (s *Store) codexLedgerDays(ctx context.Context) ([]SourceDay, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.source, e.day,
               COALESCE(SUM(e.amount_base), 0),
               COALESCE(SUM(CASE WHEN `+isPaidUnit+` THEN e.quantity ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN `+isRefund+` THEN ABS(e.quantity) ELSE 0 END), 0)
        FROM events e
        WHERE `+ledgerRows+`
        GROUP BY e.source, e.day
        ORDER BY e.day ASC`)
	if err != nil {
		return nil, fmt.Errorf("codex ledger days: %w", err)
	}
	defer rows.Close()

	out := []SourceDay{}
	for rows.Next() {
		var r SourceDay
		if err := rows.Scan(&r.Source, &r.Day, &r.Revenue, &r.Units, &r.Refunds); err != nil {
			return nil, fmt.Errorf("scan codex ledger day: %w", err)
		}
		r.Revenue = round2(r.Revenue)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) codexInstallDays(ctx context.Context) ([]DayValue, error) {
	// The install rule, once more: a (source, app, day) that has any row
	// without a country *is* the day; otherwise the country rows are the day.
	return s.codexDayValues(ctx, `
        SELECT day, COALESCE(SUM(v), 0) FROM (
            SELECT e.day AS day, `+installValue+` AS v FROM events e
            WHERE e.kind = 'install'
            GROUP BY e.source, e.app, e.day)
        GROUP BY day ORDER BY day`)
}

func (s *Store) codexDropDays(ctx context.Context) ([]DropDay, error) {
	rows, err := s.q.QueryContext(ctx, `
        SELECT e.day, d.rarity, COUNT(*), COALESCE(SUM(d.xp), 0),
               COALESCE(SUM(CASE WHEN d.rarity = 'epic'
                                  AND e.kind IN ('sales_day', 'installs_day')
                            THEN 1 ELSE 0 END), 0)
        FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+`
        GROUP BY e.day, d.rarity
        ORDER BY e.day ASC`)
	if err != nil {
		return nil, fmt.Errorf("codex drop days: %w", err)
	}
	defer rows.Close()

	var (
		out   []DropDay
		index = map[string]int{}
	)
	for rows.Next() {
		var (
			day, rarity          string
			count, xp, recordsIn int
		)
		if err := rows.Scan(&day, &rarity, &count, &xp, &recordsIn); err != nil {
			return nil, fmt.Errorf("scan codex drop day: %w", err)
		}
		i, ok := index[day]
		if !ok {
			out = append(out, DropDay{Day: day, ByRarity: map[string]int{}})
			i = len(out) - 1
			index[day] = i
		}
		out[i].Count += count
		out[i].XP += xp
		out[i].ByRarity[rarity] += count
		out[i].Records += recordsIn
	}
	return out, rows.Err()
}

// codexDayValues runs a query returning (day, value) pairs.
func (s *Store) codexDayValues(ctx context.Context, query string, args ...any) ([]DayValue, error) {
	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("codex day values: %w", err)
	}
	defer rows.Close()

	out := []DayValue{}
	for rows.Next() {
		var (
			day sql.NullString
			v   sql.NullFloat64
		)
		if err := rows.Scan(&day, &v); err != nil {
			return nil, fmt.Errorf("scan codex day value: %w", err)
		}
		if !day.Valid || day.String == "" {
			continue
		}
		out = append(out, DayValue{Day: day.String, Value: v.Float64})
	}
	return out, rows.Err()
}

// codexFirstDays runs a query returning (key, first day) pairs.
func (s *Store) codexFirstDays(ctx context.Context, query string) (map[string]string, error) {
	rows, err := s.q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("codex first days: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var (
			key sql.NullString
			day sql.NullString
		)
		if err := rows.Scan(&key, &day); err != nil {
			return nil, fmt.Errorf("scan codex first day: %w", err)
		}
		if !key.Valid || key.String == "" || !day.Valid || day.String == "" {
			continue
		}
		out[strings.ToUpper(key.String)] = day.String
	}
	return out, rows.Err()
}

// cursedRecoveryWindow is how long "and then it turned around" is allowed to
// take. A day is the natural unit: a cancellation in the morning and a rare
// drop that evening is a recovery; one a fortnight later is just Tuesday.
const cursedRecoveryWindow = 24 * 60 * 60 * 1000 // milliseconds

// codexCursedRecovery finds the first cursed drop that was followed by a rare
// or better within a day, and returns the day it happened.
func (s *Store) codexCursedRecovery(ctx context.Context) (string, error) {
	var at sql.NullInt64
	// Asked as an EXISTS over an indexed range rather than a join, so the
	// answer costs one short range scan per cursed drop instead of a product.
	// Both halves are filtered to drops that have actually been seen, exactly
	// as every other Codex query is: a drop still sealed in an unopened chest
	// has not happened yet, and awarding "it turned around" for a turnaround
	// nobody has watched is the same mistake as dating "your first drop" from
	// a chest that is still shut.
	err := s.q.QueryRowContext(ctx, `
        SELECT MIN(c.created_at) FROM drops c
        WHERE c.rarity = 'cursed'
          AND NOT (c.chest_date <> '' AND c.revealed_at IS NULL)
          AND EXISTS (SELECT 1 FROM drops r
                      WHERE r.rarity IN ('rare', 'epic', 'legendary')
                        AND NOT (r.chest_date <> '' AND r.revealed_at IS NULL)
                        AND r.created_at >  c.created_at
                        AND r.created_at <= c.created_at + ?)`,
		cursedRecoveryWindow).Scan(&at)
	if err != nil {
		return "", fmt.Errorf("codex cursed recovery: %w", err)
	}
	if !at.Valid {
		return "", nil
	}
	return core.DayOf(time.UnixMilli(at.Int64)), nil
}

func (s *Store) codexBestDrop(ctx context.Context) (*BestDrop, error) {
	var (
		b      BestDrop
		rarity string
	)
	err := s.q.QueryRowContext(ctx, `
        SELECT d.id, d.title, d.subtitle, d.rarity, d.xp, e.day, e.source
        FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+`
        ORDER BY d.xp DESC, d.id ASC LIMIT 1`).
		Scan(&b.ID, &b.Title, &b.Subtitle, &rarity, &b.XP, &b.Day, &b.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("codex best drop: %w", err)
	}
	b.Rarity = core.Rarity(rarity)
	return &b, nil
}

// ---------------------------------------------------------------- recap

// RecapCountry is one country founded inside a recap window.
type RecapCountry struct {
	Country string `json:"country"`
	Day     string `json:"day"`
}

// RecapSlice is one row of a recap's "top" answers.
type RecapSlice struct {
	Key         string  `json:"key"`
	RevenueBase float64 `json:"revenue_base"`
	Units       int     `json:"units"`
}

// RecapAggregates is everything a season recap needs from the database for one
// window. The narrative — highlights, deltas, wording — is assembled in
// internal/codex from two of these (the window and the one before it).
type RecapAggregates struct {
	From        string
	To          string
	RevenueBase float64
	Units       int
	Refunds     int
	Installs    int
	Drops       int
	XP          int
	ByRarity    map[string]int
	// Series is revenue per day, zero-filled, for the sparkline.
	Series []DayValue
	// BestDay is the day with the most revenue, "" when there was none.
	BestDay        string
	BestDayRevenue float64
	// NewCountries are countries whose first ever event fell in this window.
	NewCountries []RecapCountry
	TopApp       RecapSlice
	TopCountry   RecapSlice
	TopSource    RecapSlice
	// ChestsOpened counts chests opened inside the window (by the day they
	// were opened, not the day they belong to).
	ChestsOpened     int
	QuestsCompleted  int
	MysteriesSolved  int
	MostCountriesDay string
	MostCountries    int
	// XPBefore is the visible XP earned strictly before the window, which is
	// what dates the era and level the window started at.
	XPBefore int
}

// RecapWindow aggregates one inclusive day window for the season recap.
func (s *Store) RecapWindow(ctx context.Context, from, to string) (RecapAggregates, error) {
	days, err := dayCount(from, to)
	if err != nil {
		return RecapAggregates{}, err
	}
	out := RecapAggregates{
		From: from, To: to,
		ByRarity:     map[string]int{},
		Series:       []DayValue{},
		NewCountries: []RecapCountry{},
	}

	var (
		revenue sql.NullFloat64
		units   sql.NullInt64
		refunds sql.NullInt64
	)
	if err := s.q.QueryRowContext(ctx, `
        SELECT COALESCE(SUM(e.amount_base), 0),
               COALESCE(SUM(CASE WHEN `+isPaidUnit+` THEN e.quantity ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN `+isRefund+` THEN ABS(e.quantity) ELSE 0 END), 0)
        FROM events e
        WHERE `+ledgerRows+` AND e.day BETWEEN ? AND ?`, from, to).
		Scan(&revenue, &units, &refunds); err != nil {
		return out, fmt.Errorf("recap totals: %w", err)
	}
	out.RevenueBase = round2(revenue.Float64)
	out.Units = int(units.Int64)
	out.Refunds = int(refunds.Int64)

	var installs sql.NullFloat64
	if err := s.q.QueryRowContext(ctx, `
        SELECT COALESCE(SUM(v), 0) FROM (
            SELECT `+installValue+` AS v FROM events e
            WHERE e.kind = 'install' AND e.day BETWEEN ? AND ?
            GROUP BY e.source, e.app, e.day)`, from, to).Scan(&installs); err != nil {
		return out, fmt.Errorf("recap installs: %w", err)
	}
	out.Installs = int(installs.Float64)

	rarityRows, err := s.q.QueryContext(ctx, `
        SELECT d.rarity, COUNT(*), COALESCE(SUM(d.xp), 0)
        FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+` AND e.day BETWEEN ? AND ?
        GROUP BY d.rarity`, from, to)
	if err != nil {
		return out, fmt.Errorf("recap rarities: %w", err)
	}
	for rarityRows.Next() {
		var (
			rarity  string
			n, xp   int
			scanErr = rarityRows.Scan(&rarity, &n, &xp)
		)
		if scanErr != nil {
			rarityRows.Close()
			return out, fmt.Errorf("scan recap rarity: %w", scanErr)
		}
		out.ByRarity[rarity] = n
		out.Drops += n
		out.XP += xp
	}
	rarityRows.Close()
	if err := rarityRows.Err(); err != nil {
		return out, fmt.Errorf("iterate recap rarities: %w", err)
	}

	// Zero-filled revenue series: a sparkline with holes in it lies about
	// quiet days.
	start, err := time.Parse(core.DayLayout, from)
	if err != nil {
		return out, fmt.Errorf("recap from: %w", err)
	}
	index := make(map[string]int, days)
	for i := 0; i < days; i++ {
		day := core.DayOf(start.AddDate(0, 0, i))
		index[day] = len(out.Series)
		out.Series = append(out.Series, DayValue{Day: day})
	}
	daily, err := s.codexDayValues(ctx, `
        SELECT e.day, COALESCE(SUM(e.amount_base), 0) FROM events e
        WHERE `+ledgerRows+` AND e.day BETWEEN ? AND ?
        GROUP BY e.day ORDER BY e.day`, from, to)
	if err != nil {
		return out, err
	}
	for _, d := range daily {
		i, ok := index[d.Day]
		if !ok {
			continue
		}
		out.Series[i].Value = round2(d.Value)
		if d.Value > out.BestDayRevenue {
			out.BestDayRevenue = round2(d.Value)
			out.BestDay = d.Day
		}
	}

	// New countries: a country's *first ever* event, not merely its first in
	// this window — a returning customer is not a new settlement.
	countryRows, err := s.q.QueryContext(ctx, `
        SELECT country, first_day FROM (
            SELECT country, MIN(day) AS first_day FROM events
            WHERE country <> '' GROUP BY country)
        WHERE first_day BETWEEN ? AND ?
        ORDER BY first_day ASC, country ASC`, from, to)
	if err != nil {
		return out, fmt.Errorf("recap new countries: %w", err)
	}
	byDay := map[string]int{}
	for countryRows.Next() {
		var c RecapCountry
		if err := countryRows.Scan(&c.Country, &c.Day); err != nil {
			countryRows.Close()
			return out, fmt.Errorf("scan recap country: %w", err)
		}
		out.NewCountries = append(out.NewCountries, c)
		byDay[c.Day]++
		if byDay[c.Day] > out.MostCountries {
			out.MostCountries = byDay[c.Day]
			out.MostCountriesDay = c.Day
		}
	}
	countryRows.Close()
	if err := countryRows.Err(); err != nil {
		return out, fmt.Errorf("iterate recap countries: %w", err)
	}

	tops := []struct {
		column string
		into   *RecapSlice
	}{
		{"e.app", &out.TopApp},
		{"e.country", &out.TopCountry},
		{"e.source", &out.TopSource},
	}
	for _, t := range tops {
		slice, err := s.recapTop(ctx, t.column, from, to)
		if err != nil {
			return out, err
		}
		*t.into = slice
	}

	counts := []struct {
		into  *int
		query string
	}{
		{&out.ChestsOpened, `SELECT COUNT(DISTINCT chest_date) FROM drops
             WHERE revealed_at IS NOT NULL AND chest_date <> ''
               AND strftime('%Y-%m-%d', revealed_at / 1000, 'unixepoch') BETWEEN ? AND ?`},
		{&out.QuestsCompleted, `SELECT COUNT(*) FROM events
             WHERE source = 'loot' AND kind = 'quest_complete' AND day BETWEEN ? AND ?`},
		{&out.MysteriesSolved, `SELECT COUNT(*) FROM events
             WHERE source = 'loot' AND kind = 'mystery_solved' AND day BETWEEN ? AND ?`},
	}
	for _, c := range counts {
		if err := s.q.QueryRowContext(ctx, c.query, from, to).Scan(c.into); err != nil {
			return out, fmt.Errorf("recap count: %w", err)
		}
	}

	if err := s.q.QueryRowContext(ctx, `
        SELECT COALESCE(SUM(d.xp), 0) FROM drops d JOIN events e ON e.id = d.event_id
        WHERE NOT `+unrevealed+` AND e.day < ?`, from).Scan(&out.XPBefore); err != nil {
		return out, fmt.Errorf("recap xp before: %w", err)
	}
	return out, nil
}

// recapTop returns the highest-earning value of a column in the window, by
// revenue and then by units, so a window with installs but no money still has
// an answer.
func (s *Store) recapTop(ctx context.Context, column, from, to string) (RecapSlice, error) {
	var r RecapSlice
	err := s.q.QueryRowContext(ctx, `
        SELECT `+column+`,
               COALESCE(SUM(e.amount_base), 0),
               COALESCE(SUM(CASE WHEN `+isPaidUnit+` THEN e.quantity ELSE 0 END), 0)
        FROM events e
        WHERE `+ledgerRows+` AND e.day BETWEEN ? AND ? AND `+column+` <> ''
        GROUP BY `+column+`
        ORDER BY SUM(e.amount_base) DESC, SUM(e.quantity) DESC
        LIMIT 1`, from, to).Scan(&r.Key, &r.RevenueBase, &r.Units)
	if errors.Is(err, sql.ErrNoRows) {
		return RecapSlice{}, nil
	}
	if err != nil {
		return r, fmt.Errorf("recap top %s: %w", column, err)
	}
	r.RevenueBase = round2(r.RevenueBase)
	return r, nil
}

// SortDayValues orders a series oldest first. Series come out of SQL sorted,
// but anything assembled in Go should say so explicitly.
func SortDayValues(series []DayValue) {
	sort.Slice(series, func(i, j int) bool { return series[i].Day < series[j].Day })
}
