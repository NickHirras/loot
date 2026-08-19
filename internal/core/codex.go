package core

import (
	"encoding/json"
	"strings"
	"time"
)

// The Codex is Loot's permanent record: achievements that only ever unlock,
// records that only ever improve, and a season recap that celebrates what
// happened.
//
// One rule shapes all of it, and it is the same rule quests are built on:
//
//	Nothing here can ever be lost.
//
// An achievement, once earned, is earned — there is no decay, no expiry and no
// "you dropped below the threshold". A record is only ever beaten, never
// broken. A recap states a decline as a plain fact ("revenue $412, down from
// $610") and never as a verdict. The Codex is a trophy cabinet, not a report
// card.

// AchievementTier is how grand a trophy is. It maps onto a drop rarity, which
// is what makes an unlock feel like everything else in Loot: a real drop, with
// a colour and a sound.
type AchievementTier string

const (
	TierBronze    AchievementTier = "bronze"
	TierSilver    AchievementTier = "silver"
	TierGold      AchievementTier = "gold"
	TierLegendary AchievementTier = "legendary"
)

// AchievementTiers lists every tier, lowest first.
var AchievementTiers = []AchievementTier{TierBronze, TierSilver, TierGold, TierLegendary}

// Valid reports whether t is a known tier.
func (t AchievementTier) Valid() bool {
	for _, k := range AchievementTiers {
		if k == t {
			return true
		}
	}
	return false
}

// tierRarity maps a tier onto the rarity its unlock drop is minted at. The
// rules file states the same mapping — this is what the catalog and the tests
// agree on, and internal/rules/default.yaml is what the pipeline reads.
var tierRarity = map[AchievementTier]Rarity{
	TierBronze:    Uncommon,
	TierSilver:    Rare,
	TierGold:      Epic,
	TierLegendary: Legendary,
}

// tierXP is what an unlock at each tier pays.
var tierXP = map[AchievementTier]int{
	TierBronze:    50,
	TierSilver:    150,
	TierGold:      400,
	TierLegendary: 1000,
}

// Rarity is the drop rarity an unlock at this tier is minted at.
func (t AchievementTier) Rarity() Rarity {
	if r, ok := tierRarity[t]; ok {
		return r
	}
	return Uncommon
}

// XP is what an unlock at this tier pays.
func (t AchievementTier) XP() int {
	if xp, ok := tierXP[t]; ok {
		return xp
	}
	return 50
}

// Rank orders tiers so the trophy wall can sort by how hard-won a trophy is.
func (t AchievementTier) Rank() int {
	for i, k := range AchievementTiers {
		if k == t {
			return i
		}
	}
	return 0
}

// KindAchievement is the event kind an unlock ingests, and AchievementDedupePfx
// the prefix of its dedupe key. One key per achievement, forever: an unlock can
// never mint two drops, however many times evaluation runs.
const (
	KindAchievement      = "achievement"
	AchievementDedupePfx = "loot:achievement:"
)

// Achievement is one trophy: locked with a progress bar, or unlocked with the
// day it was earned.
type Achievement struct {
	ID    string          `json:"id"`
	Key   string          `json:"key"`
	Tier  AchievementTier `json:"tier"`
	Title string          `json:"title"`
	// Description is the one line the card shows on hover: what it took.
	Description string `json:"description"`
	// UnlockedAt is nil while the achievement is still locked. It is the time
	// the achievement was *earned*, which on a backfill is a day in the past
	// rather than the moment Loot noticed.
	UnlockedAt *time.Time `json:"unlocked_at"`
	// ProgressValue and ProgressTarget drive the bar on a locked card
	// ("Settler III · 18/25 countries"). They are kept fresh for locked
	// achievements and frozen at the target once unlocked.
	ProgressValue  float64 `json:"progress_value"`
	ProgressTarget float64 `json:"progress_target"`
	// Unit is how the numbers read: "countries", "chests", "USD"…
	Unit string `json:"unit"`
	// Money marks a progress pair denominated in the display currency.
	Money bool `json:"money"`
	// Meta carries small facts about the unlock — notably `backfilled`, set
	// when the achievement was earned before Loot was watching.
	Meta json.RawMessage `json:"meta,omitempty"`
	// Pct is ProgressValue/ProgressTarget clamped to 0..1, computed for the UI.
	Pct float64 `json:"pct"`
}

// Unlocked reports whether this trophy has been earned.
func (a Achievement) Unlocked() bool { return a.UnlockedAt != nil }

// LevelFor places total XP on the soft level curve the header draws: every
// level costs a little more than the last, and level 1 starts at zero XP. It
// lives here so the season recap can say "you went from level 12 to level 14"
// with exactly the number the header was showing at the time.
func LevelFor(totalXP int) int {
	if totalXP < 0 {
		totalXP = 0
	}
	level := 1
	for level*level*100 <= totalXP {
		level++
	}
	return level
}

// FlagEmoji converts an ISO 3166-1 alpha-2 code into its regional-indicator
// flag emoji, or "" for anything that is not two letters.
func FlagEmoji(iso2 string) string {
	c := strings.ToUpper(strings.TrimSpace(iso2))
	if len(c) != 2 {
		return ""
	}
	var r []rune
	for _, ch := range c {
		if ch < 'A' || ch > 'Z' {
			return ""
		}
		r = append(r, rune(0x1F1E6+(ch-'A')))
	}
	return string(r)
}

// AchievementPayload is what an unlock event carries, so the rules file can
// pick a rarity from the tier without arithmetic.
type AchievementPayload struct {
	Key         string          `json:"key"`
	Title       string          `json:"title"`
	Tier        AchievementTier `json:"tier"`
	Description string          `json:"description,omitempty"`
	// Backfilled marks an achievement that was already earned when Loot first
	// looked. Its drop goes into today's chest rather than onto the live feed,
	// so importing a year of history does not fire twenty-five legendary
	// sounds at once.
	Backfilled bool `json:"backfilled,omitempty"`
}
