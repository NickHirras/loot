package rules_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/rules"
)

// The default rules for a boss fight. These are assertions about *tone* as much
// as about mechanics: the spawn is bad news worth no XP, the enrage is one
// quiet note, and the kill is one of the loudest drops in the file.
func bossDrop(t *testing.T, kind string, payload map[string]any) core.Drop {
	t.Helper()
	engine, err := rules.Load("", nil)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	drop, err := engine.Classify(context.Background(), core.Event{
		ID: "e1", Source: "loot", Kind: kind, App: "com.example.app", Payload: raw,
	})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	return drop
}

func TestBossSpawnIsCursedAndFree(t *testing.T) {
	drop := bossDrop(t, "boss_spawn", map[string]any{
		"name":   "The Null-Dereferencing Wyrm of v2.3.1",
		"title":  "NPE in SyncWorker.onRun",
		"detail": "312 crashes · v2.3.1 · com.example.app",
	})
	if drop.Rarity != core.Cursed {
		t.Errorf("rarity = %s, want cursed", drop.Rarity)
	}
	if drop.XP != 0 {
		t.Errorf("xp = %d, want 0: the crash is news, not a punishment", drop.XP)
	}
	if drop.Title != "A boss appears: The Null-Dereferencing Wyrm of v2.3.1" {
		t.Errorf("title = %q", drop.Title)
	}
	if drop.Subtitle != "NPE in SyncWorker.onRun · 312 crashes · v2.3.1 · com.example.app" {
		t.Errorf("subtitle = %q", drop.Subtitle)
	}
}

func TestBossEnrageIsOneQuietNote(t *testing.T) {
	drop := bossDrop(t, "boss_enrage", map[string]any{
		"name":   "Grimjaw the ANR Lich",
		"detail": "it got worse before it got better",
	})
	if drop.Rarity != core.Cursed {
		t.Errorf("rarity = %s, want cursed", drop.Rarity)
	}
	if drop.XP != 0 {
		t.Errorf("xp = %d, want 0", drop.XP)
	}
	if drop.Title != "Grimjaw the ANR Lich enrages" {
		t.Errorf("title = %q", drop.Title)
	}
}

func TestBossSlain(t *testing.T) {
	drop := bossDrop(t, "boss_slain", map[string]any{
		"name":   "The Segfault Hydra",
		"detail": "down from 312 in 4 days",
	})
	if drop.Rarity != core.Epic {
		t.Errorf("rarity = %s, want epic", drop.Rarity)
	}
	if drop.XP != 500 {
		t.Errorf("xp = %d, want 500", drop.XP)
	}
	if drop.Title != "Boss slain: The Segfault Hydra" {
		t.Errorf("title = %q", drop.Title)
	}
}

// `scale` is the boss engine's own word for "this was a big one", and the more
// specific rule has to sit first for it to win.
func TestBossSlainLegendaryWins(t *testing.T) {
	drop := bossDrop(t, "boss_slain", map[string]any{
		"name":   "The Immortal Kraken",
		"detail": "down from 900 in 9 days",
		"scale":  "legendary",
	})
	if drop.Rarity != core.Legendary {
		t.Errorf("rarity = %s, want legendary", drop.Rarity)
	}
	if drop.XP != 1500 {
		t.Errorf("xp = %d, want 1500", drop.XP)
	}
}
