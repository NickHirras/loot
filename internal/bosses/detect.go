package bosses

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// How a boss spawns, drains and dies.
//
// The baseline is the same robust one the mystery detector uses — the median
// of the trailing 28 days — for the same reason: the thing being looked for is
// one enormous day, and a mean would let that day poison the average into
// never noticing the next one. It is measured per *app*, not per version,
// because a brand new version has no history of its own and "three times what
// this app usually does" is the question that actually means something.
//
// Every threshold below has a floor beside it. A ratio on its own would spawn
// a boss the first time an app with two crashes a day had six, which is not a
// boss, it is a Tuesday.
const (
	// BaselineDays is how many trailing days the baseline median is taken over.
	BaselineDays = 28
	// DetectDays is how far back a spawn may be found. Older spikes are
	// history: Loot does not reopen them.
	DetectDays = 14
	// LookbackDays is how much series is read in one pass — enough for the
	// baseline, the detection window and a fortnight of fade.
	LookbackDays = BaselineDays + DetectDays + 3
	// SeriesPoints is how many days of context a boss carries for its
	// sparkline.
	SeriesPoints = 14

	// SpawnMultiple is how many times its usual crashes a day needs, and
	// SpawnFloor the absolute number below which nothing is ever a boss. An
	// app that crashes twice a day and then crashes eight times has tripled;
	// it has also crashed eight times.
	SpawnMultiple = 3
	SpawnFloor    = 20
	// UsersFloor is the other way in: fifty distinct people hit by the same
	// crash in one day is a boss whatever the ratio says.
	UsersFloor = 50
	// MinHistoryDays is how many days the source must already have reported on
	// before a spawn is believed. Without it, connecting a crash source to a
	// healthy app would spawn a boss for the app's ordinary Tuesday, because
	// its 28-day baseline is 28 days of nothing.
	MinHistoryDays = 7
	// MaxSpawnsPerPass caps a single evaluation. A backfill that lands a month
	// of crash history at once should introduce a few monsters, not forty.
	MaxSpawnsPerPass = 5

	// EnrageCap is how far past its opening strength a boss may climb. Beyond
	// 1.5× the bar would say nothing new, and the number stops being a health
	// bar and starts being a graph.
	EnrageCap = 1.5
	// SlayFraction is the share of opening strength a day must fall to, and
	// SlayQuietDays how many such days in a row make a kill. Two days, because
	// one quiet day is a weekend.
	SlayFraction  = 0.10
	SlayQuietDays = 2
	// FadeDays is how long a fight waits for its source to say anything before
	// Loot admits it has lost sight of it.
	FadeDays = 14
)

// Result reports what one evaluation pass did.
type Result struct {
	Spawned []core.Boss
	Enraged []core.Boss
	Slain   []core.Boss
	Faded   []core.Boss
}

// Changed reports whether the pass moved anything.
func (r Result) Changed() bool {
	return len(r.Spawned)+len(r.Enraged)+len(r.Slain)+len(r.Faded) > 0
}

// world is everything one evaluation reads, gathered once.
type world struct {
	// series maps a boss key to that fight's days, oldest first.
	series map[string][]store.BossDay
	// keys maps a boss key back to the (source, app, version, issue) it came
	// from, so a spawn can be described without re-parsing the key.
	keys map[string]store.BossSeriesKey
	// totals is crashes per (source, app) per day: the baseline.
	totals map[store.SeriesKey]map[string]float64
	// reported is the days each (source, app) said anything at all, ascending.
	// It answers "have we heard from this source?" and drives the fade test.
	reported map[store.SeriesKey][]string
	// attested is the subset of those days the source positively vouched for
	// the app's whole crash total on — the `crash_day` heartbeat. Only these
	// days may be counted quiet, because only a source that looked can say
	// nothing happened. See store.CrashAttestedDays.
	attested map[store.SeriesKey][]string
	// resolved maps a boss key to the day somebody closed it upstream.
	resolved map[string]string
	// forced maps a boss key to the first day a source demanded a boss.
	forced map[string]string

	// lastDay is the newest completed day; detectFrom the oldest day a spawn
	// may be found on.
	lastDay    string
	detectFrom string
}

// Evaluate reads the recent crash history and moves every fight one step:
// spawning what has broken away from its baseline, draining what is being
// fixed, killing what has gone quiet and fading what has gone silent.
//
// Only *completed* days are examined. Today is still accumulating, and a
// half-finished day looks exactly like a fix that worked.
func (s *Service) Evaluate(ctx context.Context) (Result, error) {
	var res Result

	now := s.now()
	w := world{
		lastDay:    core.DayOf(now.AddDate(0, 0, -1)),
		detectFrom: core.DayOf(now.AddDate(0, 0, -DetectDays)),
	}
	from := core.DayOf(now.AddDate(0, 0, -LookbackDays))

	raw, err := s.Store.CrashSeries(ctx, from, w.lastDay)
	if err != nil {
		return res, err
	}
	w.series = make(map[string][]store.BossDay, len(raw))
	w.keys = make(map[string]store.BossSeriesKey, len(raw))
	for key, days := range raw {
		store.SortBossDays(days)
		w.series[key.Key()] = days
		w.keys[key.Key()] = key
	}
	if w.totals, err = s.Store.CrashTotals(ctx, from, w.lastDay); err != nil {
		return res, err
	}
	if w.reported, err = s.Store.CrashReportedDays(ctx, from, w.lastDay); err != nil {
		return res, err
	}
	if w.attested, err = s.Store.CrashAttestedDays(ctx, from, w.lastDay); err != nil {
		return res, err
	}
	if w.resolved, err = s.Store.CrashResolutions(ctx, from); err != nil {
		return res, err
	}
	if w.forced, err = s.Store.ForcedBossKeys(ctx, w.detectFrom); err != nil {
		return res, err
	}

	spawned, err := s.spawn(ctx, w)
	if err != nil {
		return res, err
	}
	res.Spawned = spawned

	// Drive *after* spawning and from a fresh read, so a boss born a moment
	// ago is immediately measured against today — a spike that has already
	// recovered appears and dies in the same pass, which is exactly the story
	// its two drops should tell.
	alive, err := s.Store.ListBosses(ctx, store.BossQuery{Statuses: []string{core.BossAlive}})
	if err != nil {
		return res, err
	}
	for _, b := range alive {
		outcome, err := s.drive(ctx, w, b)
		if err != nil {
			return res, err
		}
		switch outcome.kind {
		case outcomeEnraged:
			res.Enraged = append(res.Enraged, outcome.boss)
		case outcomeSlain:
			res.Slain = append(res.Slain, outcome.boss)
		case outcomeFaded:
			res.Faded = append(res.Faded, outcome.boss)
		}
	}

	if res.Changed() {
		s.changed()
	}
	return res, nil
}

// ------------------------------------------------------------------- spawning

// spawn looks for fights that do not exist yet and should.
func (s *Service) spawn(ctx context.Context, w world) ([]core.Boss, error) {
	var out []core.Boss

	for _, key := range sortedKeys(w.series) {
		if len(out) >= MaxSpawnsPerPass {
			s.log().Info("boss spawn cap reached this pass", "cap", MaxSpawnsPerPass)
			break
		}
		fight := w.keys[key]
		app := store.SeriesKey{Source: fight.Source, App: fight.App}
		days := w.series[key]

		for _, day := range days {
			if day.Day < w.detectFrom {
				continue
			}
			base := baseline(w.totals[app], day.Day)
			hp, unit := opening(day)
			if hp <= 0 {
				continue
			}
			forcedFrom, forced := w.forced[key]
			forced = forced && day.Day >= forcedFrom
			if !forced {
				if history(w.reported[app], day.Day) < MinHistoryDays {
					continue
				}
				if !qualifies(hp, day.UsersAffected, base) {
					continue
				}
			}

			boss, created, err := s.spawnOne(ctx, w, fight, key, day, base, hp, unit, forced)
			if err != nil {
				return out, err
			}
			if created {
				out = append(out, boss)
			}
			break // one boss per key, ever
		}
	}
	return out, nil
}

// qualifies is the spawn predicate: a big multiple of the usual with a real
// floor under it, or a lot of distinct people in one day.
func qualifies(value float64, users int, base float64) bool {
	if value >= math.Max(SpawnMultiple*base, SpawnFloor) {
		return true
	}
	return users >= UsersFloor && value >= SpawnMultiple*base
}

// opening decides a fight's hit points and what they count. Crashes are the
// natural unit; a source that only knows how many people were affected gets a
// health bar in people, which is if anything the more honest number.
func opening(day store.BossDay) (float64, string) {
	if day.Crashes > 0 {
		return day.Crashes, "crashes"
	}
	if day.UsersAffected > 0 {
		return float64(day.UsersAffected), "users"
	}
	return 0, "crashes"
}

func (s *Service) spawnOne(ctx context.Context, w world, fight store.BossSeriesKey, key string,
	day store.BossDay, base, hp float64, unit string, forced bool,
) (core.Boss, bool, error) {
	kind := day.Kind
	if kind == "" {
		kind = core.BossKindCrash
	}
	spawnedAt, err := time.Parse(core.DayLayout, day.Day)
	if err != nil {
		spawnedAt = s.now()
	}

	b := core.Boss{
		ID:            core.NewID(),
		Key:           key,
		Source:        fight.Source,
		App:           fight.App,
		Name:          Name(key, fight.Version, kind),
		Title:         titleFor(day.Title, fight.Version, kind),
		Version:       fight.Version,
		IssueID:       fight.Issue,
		HPMax:         hp,
		HP:            hp,
		UsersAffected: day.UsersAffected,
		SpawnedAt:     spawnedAt.UTC(),
		SpawnedDay:    day.Day,
		Status:        core.BossAlive,
		PeakDay:       day.Day,
	}

	detail := core.BossDetail{
		Series:   seriesFor(w.series[key], w.lastDay),
		Baseline: round1(base),
		Unit:     unit,
		Kind:     kind,
		URL:      day.URL,
		Why:      whyLine(hp, base, day.UsersAffected, unit, forced),
		LastDay:  day.Day,
	}
	if b.Detail, err = json.Marshal(detail); err != nil {
		return b, false, fmt.Errorf("boss detail: %w", err)
	}

	created, err := s.Store.InsertBoss(ctx, b)
	if err != nil {
		return b, false, err
	}
	if !created {
		return b, false, nil
	}

	s.log().Info("boss spawned", "name", b.Name, "app", b.App, "hp", b.HPMax, "day", b.SpawnedDay)
	if err := s.awardSpawn(ctx, b); err != nil {
		s.log().Error("boss spawn drop failed", "error", err, "boss", b.ID)
	}
	return b, true, nil
}

// ------------------------------------------------------------------- driving

type outcomeKind int

const (
	outcomeNone outcomeKind = iota
	outcomeEnraged
	outcomeSlain
	outcomeFaded
)

type outcome struct {
	kind outcomeKind
	boss core.Boss
}

// drive advances one live fight by whatever the completed days say.
func (s *Service) drive(ctx context.Context, w world, b core.Boss) (outcome, error) {
	detail := decodeDetail(b.Detail)
	app := store.SeriesKey{Source: b.Source, App: b.App}
	byDay := index(w.series[b.Key])
	unit := detail.Unit
	if unit == "" {
		unit = "crashes"
	}

	// The timeline is every day the source *reported on* since the spawn. A
	// day the source said nothing about is not a quiet day, it is an unknown
	// one, and treating the two the same is how a broken credential would
	// otherwise be mistaken for a fix.
	timeline := pointsOver(w.reported[app], b.SpawnedDay, byDay, unit)
	// The quiet run is counted over the stricter set: only days the source
	// *attested* to the whole app's total on. A push-only source's rows about
	// some other issue are not a statement that this issue went quiet, and
	// counting them as one would let a boss spawn and die in the same pass.
	attested := pointsOver(w.attested[app], b.SpawnedDay, byDay, unit)

	if len(timeline) > 0 {
		last := timeline[len(timeline)-1]
		b.HP = math.Min(last.Value, b.HPMax*EnrageCap)
		detail.LastDay = last.Day
	}
	for _, p := range timeline {
		if p.Value > b.HPMax && !detail.Enraged {
			detail.Enraged = true
			detail.EnragedDay = p.Day
		}
		if p.Value >= peakValue(byDay, b.PeakDay, unit) {
			b.PeakDay = p.Day
		}
	}
	if u := maxUsers(byDay, timeline); u > b.UsersAffected {
		b.UsersAffected = u
	}
	detail.Series = seriesFor(w.series[b.Key], w.lastDay)
	detail.QuietDays = quietDays(attested, b.HPMax)

	newlyEnraged := detail.Enraged && !decodeDetail(b.Detail).Enraged

	// --- did it die? ------------------------------------------------------
	resolvedDay, resolved := w.resolved[b.Key]
	switch {
	case resolved && resolvedDay >= b.SpawnedDay:
		detail.Slayer = "resolved"
	case detail.QuietDays >= SlayQuietDays:
		detail.Slayer = "recovered"
	}

	raw, err := json.Marshal(detail)
	if err != nil {
		return outcome{}, fmt.Errorf("boss detail: %w", err)
	}
	b.Detail = raw

	if detail.Slayer != "" {
		hp := b.HP
		if detail.Slayer == "resolved" {
			hp = 0
		}
		changed, err := s.Store.CloseBoss(ctx, b.ID, core.BossSlain, hp, raw, s.now())
		if err != nil {
			return outcome{}, err
		}
		if !changed {
			return outcome{}, nil
		}
		b.Status = core.BossSlain
		b.HP = hp
		slain := s.now()
		b.SlainAt = &slain
		if err := s.awardSlain(ctx, &b, w.lastDay); err != nil {
			s.log().Error("boss reward failed", "error", err, "boss", b.ID)
		}
		s.log().Info("boss slain", "name", b.Name, "by", detail.Slayer, "xp", b.XPAwarded)
		return outcome{kind: outcomeSlain, boss: b}, nil
	}

	// --- has Loot lost sight of it? ---------------------------------------
	if faded(w.reported[app], b.SpawnedDay, w.lastDay) {
		changed, err := s.Store.CloseBoss(ctx, b.ID, core.BossFaded, b.HP, raw, s.now())
		if err != nil {
			return outcome{}, err
		}
		if !changed {
			return outcome{}, nil
		}
		b.Status = core.BossFaded
		// Deliberately quiet: no event, no drop, no sound. The source stopped
		// reporting, which is a fact about the source.
		s.log().Info("boss faded", "name", b.Name, "app", b.App, "since", b.SpawnedDay)
		return outcome{kind: outcomeFaded, boss: b}, nil
	}

	// --- still standing ---------------------------------------------------
	if err := s.Store.UpdateBossFight(ctx, b, s.now()); err != nil {
		return outcome{}, err
	}
	if newlyEnraged {
		if err := s.awardEnrage(ctx, b); err != nil {
			s.log().Error("boss enrage drop failed", "error", err, "boss", b.ID)
		}
		s.log().Info("boss enraged", "name", b.Name, "hp", b.HP, "hp_max", b.HPMax)
		return outcome{kind: outcomeEnraged, boss: b}, nil
	}
	return outcome{kind: outcomeNone, boss: b}, nil
}

// pointsOver reads one value per day of `days` from the spawn day onwards.
func pointsOver(days []string, spawnedDay string, byDay map[string]store.BossDay, unit string) []core.BossPoint {
	var out []core.BossPoint
	for _, day := range days {
		if day < spawnedDay {
			continue
		}
		out = append(out, core.BossPoint{Day: day, Value: valueOn(byDay, day, unit)})
	}
	return out
}

// quietDays counts the trailing run of *attested* days at or below the slay
// threshold. The spawn day itself always fails the test (it *is* the maximum),
// so a one-day fight can never kill itself the moment it appears.
func quietDays(timeline []core.BossPoint, hpMax float64) int {
	if hpMax <= 0 {
		return 0
	}
	threshold := hpMax * SlayFraction
	n := 0
	for i := len(timeline) - 1; i >= 0; i-- {
		if timeline[i].Value > threshold {
			break
		}
		n++
	}
	return n
}

// faded reports whether the source has said nothing for FadeDays, on a fight
// that has itself been running at least that long.
func faded(reported []string, spawnedDay, lastDay string) bool {
	if core.DaysBetween(spawnedDay, lastDay) <= FadeDays {
		return false
	}
	newest := ""
	for _, day := range reported {
		if day > newest {
			newest = day
		}
	}
	if newest == "" {
		return true
	}
	return core.DaysBetween(newest, lastDay) > FadeDays
}

// ------------------------------------------------------------------- helpers

func index(days []store.BossDay) map[string]store.BossDay {
	out := make(map[string]store.BossDay, len(days))
	for _, d := range days {
		out[d.Day] = d
	}
	return out
}

func valueOn(byDay map[string]store.BossDay, day, unit string) float64 {
	d, ok := byDay[day]
	if !ok {
		return 0
	}
	if unit == "users" {
		return float64(d.UsersAffected)
	}
	return d.Crashes
}

func peakValue(byDay map[string]store.BossDay, day, unit string) float64 {
	return valueOn(byDay, day, unit)
}

func maxUsers(byDay map[string]store.BossDay, timeline []core.BossPoint) int {
	best := 0
	for _, p := range timeline {
		if d, ok := byDay[p.Day]; ok && d.UsersAffected > best {
			best = d.UsersAffected
		}
	}
	return best
}

// history counts how many days the source reported on strictly before `day`.
func history(reported []string, day string) int {
	n := 0
	for _, d := range reported {
		if d < day {
			n++
		}
	}
	return n
}

// baseline is the median of the trailing BaselineDays of app-wide crashes
// before `day`, zero-filled — a day with no crashes is the most informative
// day a baseline can have.
func baseline(totals map[string]float64, day string) float64 {
	end, err := time.Parse(core.DayLayout, day)
	if err != nil {
		return 0
	}
	vals := make([]float64, 0, BaselineDays)
	for i := BaselineDays; i >= 1; i-- {
		vals = append(vals, totals[core.DayOf(end.AddDate(0, 0, -i))])
	}
	return median(vals)
}

func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// seriesFor builds the sparkline: the last SeriesPoints calendar days ending
// at `to`, zero-filled, so the shape of the fight is readable at a glance.
func seriesFor(days []store.BossDay, to string) []core.BossPoint {
	end, err := time.Parse(core.DayLayout, to)
	if err != nil {
		return nil
	}
	byDay := index(days)
	out := make([]core.BossPoint, 0, SeriesPoints)
	for i := SeriesPoints - 1; i >= 0; i-- {
		day := core.DayOf(end.AddDate(0, 0, -i))
		out = append(out, core.BossPoint{Day: day, Value: byDay[day].Crashes})
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// titleFor is the human line under the monster name: the crash's own title
// when the source knows one, and otherwise a plain description of what is
// crashing.
func titleFor(issueTitle, version, kind string) string {
	if issueTitle != "" {
		return issueTitle
	}
	noun := "Crashes"
	if kind == core.BossKindANR {
		noun = "ANRs"
	}
	if label := VersionLabel(version); label != "" {
		return noun + " in " + label
	}
	return noun
}

func whyLine(hp, base float64, users int, unit string, forced bool) string {
	if forced {
		return fmt.Sprintf("your crash reporter flagged this one: %s %s in a day",
			core.FormatCount(hp), unit)
	}
	if base > 0 {
		line := fmt.Sprintf("%s %s in a day against a usual %s",
			core.FormatCount(hp), unit, core.FormatCount(base))
		if users > 0 {
			line += fmt.Sprintf(" · %s people affected", core.FormatCount(float64(users)))
		}
		return line
	}
	if users > 0 {
		return fmt.Sprintf("%s %s in a day, hitting %s people",
			core.FormatCount(hp), unit, core.FormatCount(float64(users)))
	}
	return fmt.Sprintf("%s %s in a day", core.FormatCount(hp), unit)
}

func spawnDetailLine(b core.Boss) string {
	detail := decodeDetail(b.Detail)
	unit := detail.Unit
	if unit == "" {
		unit = "crashes"
	}
	line := fmt.Sprintf("%s %s", core.FormatCount(b.HPMax), unit)
	if label := VersionLabel(b.Version); label != "" {
		line += " · " + label
	}
	if b.App != "" {
		line += " · " + b.App
	}
	return line
}

func enrageDetailLine(b core.Boss) string {
	return fmt.Sprintf("it got worse before it got better — %s now, %s when it appeared",
		core.FormatCount(b.HP), core.FormatCount(b.HPMax))
}

func slainDetailLine(b core.Boss, days int) string {
	word := "days"
	if days == 1 {
		word = "day"
	}
	line := fmt.Sprintf("down from %s in %d %s", core.FormatCount(b.HPMax), days, word)
	if b.UsersAffected > 0 {
		line += fmt.Sprintf(" · %s people no longer crashing", core.FormatCount(float64(b.UsersAffected)))
	}
	return line
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
