package core

import (
	"encoding/json"
	"time"
)

// Crashes are the least fun number an app developer owns. Loot does not try to
// make them fun by pretending they are good news — it makes them a *fight*.
//
// A cluster of crashes that breaks away from its own baseline becomes a boss:
// a named monster with hit points. The spawn is a cursed drop, because
// something did go wrong. Everything after it is a fight you can win: HP is
// simply the crash count on the most recent completed day, so shipping a fix
// and watching it roll out drains the bar, and the kill pays an epic drop.
//
// Three rules keep it from becoming a scold:
//
//   - a boss that lingers is "still standing", never "you failed". There is no
//     red overdue state, no streak, no nagging.
//   - a boss that runs out of data simply *fades*: the source stopped
//     reporting, which is a fact about the source, not about you. Fading is
//     silent and pays nothing.
//   - only the spawn is cursed. The enrage is a single quiet note, and the
//     slay is the loudest thing on the page.

// Boss statuses. A boss is only ever alive, slain or faded — there is
// deliberately no "failed" and no "overdue".
const (
	// BossAlive is a fight in progress.
	BossAlive = "alive"
	// BossSlain is a fight you won: the crash rate came back down, the issue
	// was resolved, or you said so yourself.
	BossSlain = "slain"
	// BossFaded is a fight that ran out of data. The source stopped reporting
	// for a fortnight, so Loot closes the card quietly rather than leaving a
	// monster on the board forever.
	BossFaded = "faded"
)

// BossKindCrash and BossKindANR are the two shapes a boss can take. ANRs
// ("application not responding") are Android's own flavour of crash and are
// worth telling apart, because the fix is usually a different one.
const (
	BossKindCrash = "crash"
	BossKindANR   = "anr"
)

// The event kinds crash sources emit. All three are silent: a crash never
// makes a drop of its own, because one drop per crash is the exact dashboard
// bosses exist to avoid being. The *boss* is the drop.
const (
	// KindCrash is one (source, app, day, version, issue) count. Quantities
	// add up inside a day, so a source may report deltas or one total.
	KindCrash = "crash"
	// KindCrashDay is a source's daily "I looked, and here is the whole app's
	// total" heartbeat, emitted even when the total is zero. It is what lets a
	// day with no crashes at all be told apart from a day the source never
	// reported — which is the difference between a boss that was slain and one
	// that merely faded from view.
	KindCrashDay = "crash_day"
	// KindCrashResolved says somebody closed the issue upstream. It slays the
	// boss without waiting for the graph to agree, because the graph is slower
	// than you are.
	KindCrashResolved = "crash_resolved"
)

// CrashPayload is what every crash event carries. The field names are read
// back out of the stored JSON by the boss detector, so this struct is
// effectively the schema every crash source writes to.
type CrashPayload struct {
	// Version is the app version (or Play's numeric version code).
	Version string `json:"version,omitempty"`
	// IssueID and IssueTitle identify one crash cluster inside a version.
	IssueID    string `json:"issue_id,omitempty"`
	IssueTitle string `json:"issue_title,omitempty"`
	// UsersAffected is how many distinct people the day's crashes hit.
	UsersAffected int `json:"users_affected,omitempty"`
	// Kind is BossKindCrash or BossKindANR.
	Kind string `json:"kind,omitempty"`
	// URL links out to wherever the issue actually lives.
	URL string `json:"url,omitempty"`
	// CrashRate and ANRRate are Play's own per-user rates, kept because they
	// are the numbers the Play Console shows you next to the same crash.
	CrashRate float64 `json:"crash_rate,omitempty"`
	ANRRate   float64 `json:"anr_rate,omitempty"`
	// Boss forces a spawn regardless of the baseline: a crash reporter saying
	// "this one is bad" out loud. Only the generic crash webhook sets it.
	Boss bool `json:"boss,omitempty"`
	// Project, Action and Via are provenance: which Sentry project, which
	// webhook action, which relay posted it.
	Project string `json:"project,omitempty"`
	Action  string `json:"action,omitempty"`
	Via     string `json:"via,omitempty"`
}

// BossPoint is one day of the series a boss carries with it, so the card can
// be read long after the numbers moved on.
type BossPoint struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

// BossDetail is the context a boss keeps: the daily series behind the HP bar,
// the baseline it was measured against, and where to go to actually fix it.
type BossDetail struct {
	// Series is the trailing window ending on the most recent completed day
	// with data, oldest first.
	Series []BossPoint `json:"series"`
	// Baseline is the trailing median daily count the spawn was measured
	// against.
	Baseline float64 `json:"baseline"`
	// Unit is what HP counts: "crashes", "events" or "users".
	Unit string `json:"unit"`
	// Kind is BossKindCrash or BossKindANR.
	Kind string `json:"kind,omitempty"`
	// URL links out to wherever the issue actually lives (Sentry, Play
	// Console, your own tracker).
	URL string `json:"url,omitempty"`
	// Why is a one-line explanation of what made this a boss.
	Why string `json:"why,omitempty"`
	// Enraged records that the fight got worse before it got better. It is a
	// fact about the crash, not a judgement about you.
	Enraged bool `json:"enraged,omitempty"`
	// EnragedDay is the day the enrage happened.
	EnragedDay string `json:"enraged_day,omitempty"`
	// QuietDays counts consecutive completed days at or below the slay
	// threshold. Two in a row is a kill.
	QuietDays int `json:"quiet_days,omitempty"`
	// LastDay is the newest completed day the fight has data for.
	LastDay string `json:"last_day,omitempty"`
	// Slayer says how the boss died: "recovered", "resolved" or "manual".
	Slayer string `json:"slayer,omitempty"`
}

// Boss is one crash cluster, dressed as a monster.
type Boss struct {
	ID string `json:"id"`
	// Key is the fight's identity: "<source>:<app>:<version>|<issue>". One
	// alive boss per key, which is what makes every part of this idempotent.
	Key    string `json:"key"`
	Source string `json:"source"`
	App    string `json:"app"`
	// Name is the generated monster name, deterministic from Key.
	Name string `json:"name"`
	// Title is the human line under it: the issue title, or "Crashes in v2.3.1".
	Title   string `json:"title"`
	Version string `json:"version"`
	IssueID string `json:"issue_id"`
	// HPMax is the count on the spawn day (the worst it has been allowed to
	// get); HP is the count on the most recent completed day.
	HPMax float64 `json:"hp_max"`
	HP    float64 `json:"hp"`
	// UsersAffected is the most people one day of this fight touched, when the
	// source reports it.
	UsersAffected int `json:"users_affected"`

	SpawnedAt  time.Time  `json:"spawned_at"`
	SpawnedDay string     `json:"spawned_day"`
	LastHitAt  *time.Time `json:"last_hit_at,omitempty"`
	Status     string     `json:"status"`
	SlainAt    *time.Time `json:"slain_at,omitempty"`
	// PeakDay is the worst day of the fight.
	PeakDay string          `json:"peak_day"`
	Detail  json.RawMessage `json:"detail,omitempty"`
	// XPAwarded is what the slain drop paid.
	XPAwarded int `json:"xp_awarded"`

	// Pct is HP/HPMax clamped to 0..1: how full the bar is.
	Pct float64 `json:"pct"`
	// DownPct is how far HP has fallen from HPMax, 0..1 — the "−63% since it
	// appeared" line.
	DownPct float64 `json:"down_pct"`
	// DaysAlive counts the spawn day as day one.
	DaysAlive int `json:"days_alive"`
	// Enraged mirrors Detail.Enraged so the card needs no decode.
	Enraged bool `json:"enraged"`
	// Unit, URL and Kind are lifted out of Detail for the same reason.
	Unit string `json:"unit"`
	URL  string `json:"url,omitempty"`
	Kind string `json:"kind,omitempty"`
	// Series is Detail.Series, decoded for the sparkline.
	Series []BossPoint `json:"series"`
}

// BossPct clamps hp/hpMax into 0..1. A zero maximum reads as an empty bar
// rather than as a division by zero.
func BossPct(hp, hpMax float64) float64 {
	if hpMax <= 0 {
		return 0
	}
	p := hp / hpMax
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// BossKey builds the fight identity from its parts. Version and issue are both
// optional: a source that reports neither still gets one boss per app.
func BossKey(source, app, version, issue string) string {
	key := source + ":" + app + ":" + version
	if issue != "" {
		key += "|" + issue
	}
	return key
}

// DaysBetween returns the inclusive number of days from day `from` through day
// `to`, or 0 when either is unparseable. It is what "5 days alive" counts.
func DaysBetween(from, to string) int {
	start, err := time.Parse(DayLayout, from)
	if err != nil {
		return 0
	}
	end, err := time.Parse(DayLayout, to)
	if err != nil {
		return 0
	}
	n := int(end.Sub(start).Hours()/24) + 1
	if n < 0 {
		return 0
	}
	return n
}
