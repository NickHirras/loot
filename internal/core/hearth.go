package core

// The Hearth is Loot's map of the civilization your app has built: one
// settlement per country, growing as customers arrive, and one era for the
// whole account, advancing with total XP.
//
// This file holds the two tables that decide how big a settlement is and how
// grand the era is. They are deliberately pure data plus two lookups, so the
// UI can render the ladder ("what do I need for a City?") without duplicating
// the thresholds, and so the numbers can be tuned in one place.

// Tier is one rung of the settlement ladder. MinShare is the fraction of the
// *largest* country's population a settlement must reach to earn this tier —
// relative, not absolute, so the map looks the same whether you have 300
// customers or 3 million, and so growth shows as a shifting skyline rather
// than every dot maxing out.
type Tier struct {
	Name  string `json:"name"`
	Index int    `json:"index"`
	// MinShare is the inclusive lower bound on population / max population.
	MinShare float64 `json:"min_share"`
}

// Tiers is the settlement ladder, smallest first. A country with any
// population at all is at least an Outpost, so nobody who bought your app
// vanishes from the globe.
var Tiers = []Tier{
	{Name: "outpost", Index: 0, MinShare: 0},
	{Name: "hamlet", Index: 1, MinShare: 0.002},
	{Name: "village", Index: 2, MinShare: 0.01},
	{Name: "town", Index: 3, MinShare: 0.05},
	{Name: "city", Index: 4, MinShare: 0.15},
	{Name: "metropolis", Index: 5, MinShare: 0.5},
}

// TierForShare returns the tier a settlement holding `share` of the biggest
// settlement's population belongs to. A share at or below zero still returns
// the outpost tier; callers decide whether a country with no population
// belongs on the map at all.
func TierForShare(share float64) Tier {
	best := Tiers[0]
	for _, t := range Tiers {
		if share >= t.MinShare {
			best = t
		}
	}
	return best
}

// Era is one rung of the account-wide ladder, unlocked by total XP.
type Era struct {
	Name string `json:"name"`
	// Index is the era's position, 0 for Camp.
	Index int `json:"index"`
	// MinXP is the total XP at which this era begins.
	MinXP int `json:"min_xp"`
}

// Eras is the era ladder, lowest first. The thresholds climb roughly 4x a
// rung, so an era is always a milestone rather than a weekly occurrence.
var Eras = []Era{
	{Name: "Camp", Index: 0, MinXP: 0},
	{Name: "Village", Index: 1, MinXP: 1_000},
	{Name: "Town", Index: 2, MinXP: 5_000},
	{Name: "City", Index: 3, MinXP: 20_000},
	{Name: "Kingdom", Index: 4, MinXP: 75_000},
	{Name: "Empire", Index: 5, MinXP: 250_000},
	{Name: "Dynasty", Index: 6, MinXP: 1_000_000},
}

// EraProgress is one account's standing on the era ladder: which era it is in,
// how far into it, and what the next one costs. It is what the Hearth's era
// card renders, so it carries everything that card needs and nothing else.
type EraProgress struct {
	Name  string `json:"name"`
	Index int    `json:"index"`
	// MinXP is where this era started.
	MinXP int `json:"min_xp"`
	// XP is how much XP has been earned inside this era (total - MinXP).
	XP int `json:"xp"`
	// NextName is the era after this one, or "" at the top of the ladder.
	NextName string `json:"next_name"`
	// NextXP is the total XP the next era begins at, 0 at the top.
	NextXP int `json:"next_xp"`
	// ToNext is how much more XP the next era needs, 0 at the top.
	ToNext int `json:"to_next"`
	// Progress is 0..1 through the current era, and 1 at the top.
	Progress float64 `json:"progress"`
}

// EraFor places totalXP on the era ladder. Negative XP is treated as zero.
func EraFor(totalXP int) EraProgress {
	if totalXP < 0 {
		totalXP = 0
	}

	current := Eras[0]
	next := -1
	for i, e := range Eras {
		if totalXP >= e.MinXP {
			current = e
			continue
		}
		next = i
		break
	}

	p := EraProgress{
		Name:     current.Name,
		Index:    current.Index,
		MinXP:    current.MinXP,
		XP:       totalXP - current.MinXP,
		Progress: 1,
	}
	if next < 0 {
		// Top of the ladder: a Dynasty has nothing left to become.
		return p
	}

	n := Eras[next]
	p.NextName = n.Name
	p.NextXP = n.MinXP
	p.ToNext = n.MinXP - totalXP
	if span := n.MinXP - current.MinXP; span > 0 {
		p.Progress = float64(totalXP-current.MinXP) / float64(span)
	}
	return p
}
