package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/nickhirras/loot/internal/bosses"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/store"
)

// The demo world's crashes, and the two boss fights made out of them.
//
// Everything here emits the same events the real Play vitals source emits —
// a silent `crash` row per (app, day, version) and a `crash_day` heartbeat per
// app per day — so the boss the demo opens on is found by the *real* detector
// reading *plausible* numbers. Nothing about the live fight is hard-coded: its
// name, its hit points and how far it has drained are all worked out from the
// series below exactly as they would be for a real app.
//
// The one exception is the fight the demo world already won. It happened five
// weeks ago, which is outside the detector's fourteen-day spawn window on
// purpose — Loot does not reopen old spikes — so the finished boss is written
// into history directly, with its spawn and kill drops backdated into the feed
// on the days they happened. A trophy shelf with nothing on it would undersell
// the whole feature.

// crashSource is the source name the demo's vitals wear. It matches the real
// Android vitals source, so the header row and the boss cards read the same in
// a demo as they do on real data.
const crashSource = "playvitals"

// crashDay emits one app's Android vitals for one day: the ordinary background
// crashes on the shipping version, plus whatever scripted fight is running,
// plus the daily heartbeat that lets a quiet day be told apart from silence.
func (d *Demo) crashDay(ctx context.Context, pipe *pipeline.Pipeline, p dayPlan, a app) error {
	if a.CrashBase == 0 {
		return nil
	}

	// Ordinary crashes: a handful a day, worse during the bad week like
	// everything else. Deliberately *not* scaled by the app's traffic curve —
	// the record sales day multiplies traffic by three, and a background that
	// tripled with it would trip the spawn floor and hand the demo a monster
	// nobody meant to invent.
	background := p.count(a.CrashBase * (0.6 + 0.8*p.rng.Float64()) * badWeekCrashes(p))
	total := background
	users := 0
	if background > 0 {
		users = 1 + p.count(float64(background)*0.7)
		if err := d.crashRow(ctx, pipe, p, a, a.CrashVersion, "", core.BossKindCrash,
			background, users); err != nil {
			return err
		}
	}

	for _, f := range a.Fights {
		crashes, affected := f.on(p)
		if crashes <= 0 {
			continue
		}
		kind := f.Kind
		if kind == "" {
			kind = core.BossKindCrash
		}
		if err := d.crashRow(ctx, pipe, p, a, f.Version, f.Title, kind, crashes, affected); err != nil {
			return err
		}
		total += crashes
		if affected > users {
			users = affected
		}
	}

	return d.crashHeartbeat(ctx, pipe, p, a, total, users)
}

// badWeekCrashes is how much worse the ordinary crashes get during the week
// where everything went wrong. It stays under the boss spawn floor on purpose:
// a bad week is a mystery, not a boss.
func badWeekCrashes(p dayPlan) float64 {
	if p.badWeek() {
		return 2.2
	}
	return 1
}

// on returns the fight's crash count and affected users on one day of the
// window, or zero when the fight is not running that day.
func (f fight) on(p dayPlan) (int, int) {
	spike := p.span - 1 - f.SpikeBefore
	delta := p.index - spike
	if delta < 0 || delta >= f.Days {
		return 0, 0
	}
	decay := f.Decay
	if decay <= 0 || decay >= 1 {
		decay = 0.85
	}
	factor := math.Pow(decay, float64(delta))
	// A little noise, but never enough to turn a draining fight back into a
	// climbing one: a demo boss that enrages by accident tells a story nobody
	// meant to tell.
	factor *= 1 + 0.06*p.rng.NormFloat64()
	factor = math.Max(0, factor)

	crashes := int(math.Round(f.Peak * factor))
	users := int(math.Round(f.Users * factor))
	if delta == 0 {
		// The spike day is the fight's opening strength, and it is not allowed
		// to roll a low die: it is what the health bar is measured against.
		crashes, users = int(f.Peak), int(f.Users)
	}
	return crashes, users
}

// crashRow emits one silent `crash` event, shaped exactly as Play vitals would
// emit it.
func (d *Demo) crashRow(ctx context.Context, pipe *pipeline.Pipeline, p dayPlan, a app,
	version, title, kind string, crashes, users int,
) error {
	payload, err := json.Marshal(core.CrashPayload{
		Version:       version,
		IssueTitle:    title,
		UsersAffected: users,
		Kind:          kind,
		CrashRate:     crashRate(crashes, a),
		URL:           "https://play.google.com/console/developers/app/" + a.Package + "/vitals/crashes",
	})
	if err != nil {
		return fmt.Errorf("demo: encode crash payload: %w", err)
	}
	return d.ingest(ctx, pipe, core.Event{
		ID:         core.NewIDAt(p.day),
		Source:     crashSource,
		Kind:       core.KindCrash,
		App:        a.Name,
		OccurredAt: p.day,
		ObservedAt: p.day,
		Day:        p.date,
		Quantity:   crashes,
		DedupeKey: fmt.Sprintf("%s:crash:%s:%s:%s:%s",
			crashSource, a.Package, p.date, version, kind),
		Silent:  true,
		Payload: payload,
	})
}

// crashHeartbeat emits the day's app-wide total, including on the days the
// total is zero. It is the event that makes "the crashes stopped" a fact Loot
// can act on rather than an absence it has to guess about.
func (d *Demo) crashHeartbeat(ctx context.Context, pipe *pipeline.Pipeline, p dayPlan, a app, total, users int) error {
	payload, err := json.Marshal(core.CrashPayload{
		UsersAffected: users,
		Kind:          core.BossKindCrash,
		CrashRate:     crashRate(total, a),
	})
	if err != nil {
		return fmt.Errorf("demo: encode crash_day payload: %w", err)
	}
	return d.ingest(ctx, pipe, core.Event{
		ID:         core.NewIDAt(p.day),
		Source:     crashSource,
		Kind:       core.KindCrashDay,
		App:        a.Name,
		OccurredAt: p.day,
		ObservedAt: p.day,
		Day:        p.date,
		Quantity:   total,
		DedupeKey:  fmt.Sprintf("%s:crash_day:%s:%s", crashSource, a.Package, p.date),
		Silent:     true,
		Payload:    payload,
	})
}

// crashRate is the crashes-per-active-user figure the Play Console shows,
// invented here so a demo payload reads like a real one in the inspector.
func crashRate(crashes int, a app) float64 {
	if crashes == 0 || a.InstallBase == 0 {
		return 0
	}
	active := a.InstallBase * 30
	return math.Round(float64(crashes)/active*10000) / 10000
}

// ------------------------------------------------------------ finished fights

// seedWonFights writes the boss battles the demo world already won.
//
// They sit outside the detector's spawn window on purpose, so they have to be
// placed by hand — but everything about how they are *shaped* comes from the
// same constants the detector uses, so the trophy on the shelf is one the
// detector would genuinely have awarded.
func (d *Demo) seedWonFights(ctx context.Context, tx *store.Store, pipe *pipeline.Pipeline, yesterday time.Time, span int) error {
	for _, a := range apps {
		for _, f := range a.Fights {
			if f.SpikeBefore <= bosses.DetectDays {
				continue // the live detector will find this one itself
			}
			if err := d.seedWonFight(ctx, tx, pipe, a, f, yesterday, span); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Demo) seedWonFight(ctx context.Context, tx *store.Store, pipe *pipeline.Pipeline,
	a app, f fight, yesterday time.Time, span int,
) error {
	spikeDay := yesterday.AddDate(0, 0, -f.SpikeBefore)
	kind := f.Kind
	if kind == "" {
		kind = core.BossKindCrash
	}
	key := core.BossKey(crashSource, a.Name, f.Version, "")

	// Walk the fight forward exactly as the detector would, and stop where it
	// would have declared the kill: two completed days at or under a tenth of
	// its opening strength.
	var (
		series []core.BossPoint
		quiet  int
		slain  time.Time
	)
	for delta := 0; delta < f.Days; delta++ {
		day := spikeDay.AddDate(0, 0, delta)
		p := dayPlan{date: core.DayOf(day), day: day, index: span - 1 - f.SpikeBefore + delta, span: span,
			rng: d.rngFor(core.DayOf(day))}
		crashes, _ := f.on(p)
		series = append(series, core.BossPoint{Day: core.DayOf(day), Value: float64(crashes)})
		if float64(crashes) <= f.Peak*bosses.SlayFraction {
			quiet++
		} else {
			quiet = 0
		}
		if quiet >= bosses.SlayQuietDays {
			slain = day
			break
		}
	}
	if slain.IsZero() {
		slain = spikeDay.AddDate(0, 0, len(series)-1)
	}

	detail, err := json.Marshal(core.BossDetail{
		Series:    trimSeries(series, bosses.SeriesPoints),
		Baseline:  a.CrashBase,
		Unit:      "crashes",
		Kind:      kind,
		URL:       "https://play.google.com/console/developers/app/" + a.Package + "/vitals/crashes",
		Why:       fmt.Sprintf("%.0f crashes in a day against a usual %.0f", f.Peak, a.CrashBase),
		QuietDays: bosses.SlayQuietDays,
		LastDay:   core.DayOf(slain),
		Slayer:    "recovered",
	})
	if err != nil {
		return fmt.Errorf("demo: encode boss detail: %w", err)
	}

	slainAt := slain.UTC()
	b := core.Boss{
		ID:            core.NewIDAt(spikeDay),
		Key:           key,
		Source:        crashSource,
		App:           a.Name,
		Name:          bosses.Name(key, f.Version, kind),
		Title:         f.Title,
		Version:       f.Version,
		HPMax:         f.Peak,
		HP:            0,
		UsersAffected: int(f.Users),
		SpawnedAt:     spikeDay.UTC(),
		SpawnedDay:    core.DayOf(spikeDay),
		// Inserted alive and then closed, rather than inserted dead: CloseBoss
		// is guarded on "still alive", and it is the only thing that stamps
		// the day a boss died. Writing the row as slain would leave slain_at
		// NULL and the card would claim the fight had been running for five
		// weeks.
		Status:  core.BossAlive,
		SlainAt: &slainAt,
		PeakDay: core.DayOf(spikeDay),
		Detail:  detail,
	}

	created, err := tx.InsertBoss(ctx, b)
	if err != nil {
		return err
	}
	if !created {
		return nil
	}
	if _, err := tx.CloseBoss(ctx, b.ID, core.BossSlain, 0, detail, slainAt); err != nil {
		return err
	}
	b.Status = core.BossSlain

	days := core.DaysBetween(b.SpawnedDay, core.DayOf(slain))
	if err := d.bossDrop(ctx, pipe, b, bosses.KindSpawn, spikeDay, map[string]any{
		"detail": fmt.Sprintf("%.0f crashes · v%s · %s", f.Peak, f.Version, a.Name),
	}); err != nil {
		return err
	}
	xp, err := d.bossSlainDrop(ctx, pipe, b, slain, days)
	if err != nil {
		return err
	}
	return tx.SetBossXP(ctx, b.ID, xp)
}

// trimSeries keeps the last n points.
func trimSeries(points []core.BossPoint, n int) []core.BossPoint {
	if len(points) <= n {
		return points
	}
	return points[len(points)-n:]
}

// bossPayload mirrors internal/bosses' own event payload. It is spelled out
// here rather than exported from there because the two have different jobs:
// the service builds one from a live fight, and the demo builds one from a
// story it is telling.
func bossPayload(b core.Boss, extra map[string]any) map[string]any {
	out := map[string]any{
		"boss_id": b.ID,
		"key":     b.Key,
		"name":    b.Name,
		"title":   b.Title,
		"app":     b.App,
		"version": b.Version,
		"hp":      b.HP,
		"hp_max":  b.HPMax,
		"unit":    "crashes",
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (d *Demo) bossDrop(ctx context.Context, pipe *pipeline.Pipeline, b core.Boss,
	kind string, at time.Time, extra map[string]any,
) error {
	payload, err := json.Marshal(bossPayload(b, extra))
	if err != nil {
		return fmt.Errorf("demo: encode boss payload: %w", err)
	}
	return d.ingest(ctx, pipe, core.Event{
		ID:         core.NewIDAt(at),
		Source:     "loot",
		Kind:       kind,
		App:        b.App,
		OccurredAt: at.UTC(),
		ObservedAt: at.UTC(),
		Day:        core.DayOf(at),
		DedupeKey:  "loot:" + kind + ":" + b.ID,
		Payload:    payload,
	})
}

// bossSlainDrop mints the kill and returns the XP it paid, so the stored boss
// can show "+1,500 XP" on its card exactly as a live kill would.
func (d *Demo) bossSlainDrop(ctx context.Context, pipe *pipeline.Pipeline, b core.Boss, at time.Time, days int) (int, error) {
	scale := "epic"
	if b.HPMax >= bosses.LegendaryUsers || b.UsersAffected >= bosses.LegendaryUsers || days >= bosses.LegendaryDays {
		scale = "legendary"
	}
	payload, err := json.Marshal(bossPayload(b, map[string]any{
		"days":           days,
		"scale":          scale,
		"slayer":         "recovered",
		"users_affected": b.UsersAffected,
		"detail": fmt.Sprintf("down from %.0f in %d days · %d people no longer crashing",
			b.HPMax, days, b.UsersAffected),
	}))
	if err != nil {
		return 0, fmt.Errorf("demo: encode boss payload: %w", err)
	}
	drop, err := pipe.Ingest(ctx, core.Event{
		ID:         core.NewIDAt(at),
		Source:     "loot",
		Kind:       bosses.KindSlain,
		App:        b.App,
		OccurredAt: at.UTC(),
		ObservedAt: at.UTC(),
		Day:        core.DayOf(at),
		DedupeKey:  "loot:" + bosses.KindSlain + ":" + b.ID,
		Payload:    payload,
	})
	if err != nil {
		return 0, fmt.Errorf("demo: boss slain drop: %w", err)
	}
	if drop == nil {
		return 0, nil
	}
	return drop.XP, nil
}
