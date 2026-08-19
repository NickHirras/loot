package core

import "testing"

func TestTierForShare(t *testing.T) {
	cases := []struct {
		share float64
		want  string
	}{
		{1, "metropolis"},
		{0.5, "metropolis"},
		{0.4999, "city"},
		{0.15, "city"},
		{0.149, "town"},
		{0.05, "town"},
		{0.049, "village"},
		{0.01, "village"},
		{0.0099, "hamlet"},
		{0.002, "hamlet"},
		{0.0019, "outpost"},
		{0, "outpost"},
		{-1, "outpost"},
	}
	for _, c := range cases {
		if got := TierForShare(c.share); got.Name != c.want {
			t.Errorf("TierForShare(%v) = %s, want %s", c.share, got.Name, c.want)
		}
	}
}

func TestTiersAreOrdered(t *testing.T) {
	for i, tier := range Tiers {
		if tier.Index != i {
			t.Errorf("Tiers[%d].Index = %d, want %d", i, tier.Index, i)
		}
		if i > 0 && tier.MinShare <= Tiers[i-1].MinShare {
			t.Errorf("Tiers[%d] (%s) min_share %v does not exceed %s", i, tier.Name, tier.MinShare, Tiers[i-1].Name)
		}
	}
	if Tiers[0].MinShare != 0 {
		t.Errorf("the lowest tier must accept any population, got min_share %v", Tiers[0].MinShare)
	}
}

func TestEraFor(t *testing.T) {
	cases := []struct {
		xp       int
		name     string
		index    int
		nextName string
		toNext   int
	}{
		{-5, "Camp", 0, "Village", 1_000},
		{0, "Camp", 0, "Village", 1_000},
		{999, "Camp", 0, "Village", 1},
		{1_000, "Village", 1, "Town", 4_000},
		{4_999, "Village", 1, "Town", 1},
		{5_000, "Town", 2, "City", 15_000},
		{20_000, "City", 3, "Kingdom", 55_000},
		{75_000, "Kingdom", 4, "Empire", 175_000},
		{250_000, "Empire", 5, "Dynasty", 750_000},
		{1_000_000, "Dynasty", 6, "", 0},
		{9_000_000, "Dynasty", 6, "", 0},
	}
	for _, c := range cases {
		got := EraFor(c.xp)
		if got.Name != c.name || got.Index != c.index {
			t.Errorf("EraFor(%d) = %s/%d, want %s/%d", c.xp, got.Name, got.Index, c.name, c.index)
		}
		if got.NextName != c.nextName {
			t.Errorf("EraFor(%d).NextName = %q, want %q", c.xp, got.NextName, c.nextName)
		}
		if got.ToNext != c.toNext {
			t.Errorf("EraFor(%d).ToNext = %d, want %d", c.xp, got.ToNext, c.toNext)
		}
	}
}

func TestEraProgress(t *testing.T) {
	// Halfway between Village (1k) and Town (5k).
	got := EraFor(3_000)
	if got.XP != 2_000 {
		t.Errorf("XP inside era = %d, want 2000", got.XP)
	}
	if got.Progress != 0.5 {
		t.Errorf("Progress = %v, want 0.5", got.Progress)
	}
	if got.NextXP != 5_000 {
		t.Errorf("NextXP = %d, want 5000", got.NextXP)
	}

	// The top of the ladder is a full bar with nothing left to earn.
	top := EraFor(2_000_000)
	if top.Progress != 1 || top.NextXP != 0 || top.ToNext != 0 {
		t.Errorf("top era = %+v, want a full bar with no next", top)
	}
	if top.XP != 1_000_000 {
		t.Errorf("top era XP = %d, want 1000000", top.XP)
	}
}

func TestErasAreOrdered(t *testing.T) {
	for i, era := range Eras {
		if era.Index != i {
			t.Errorf("Eras[%d].Index = %d, want %d", i, era.Index, i)
		}
		if i > 0 && era.MinXP <= Eras[i-1].MinXP {
			t.Errorf("Eras[%d] (%s) min_xp %d does not exceed %s", i, era.Name, era.MinXP, Eras[i-1].Name)
		}
	}
	if Eras[0].MinXP != 0 {
		t.Errorf("the first era must start at 0 XP, got %d", Eras[0].MinXP)
	}
}
