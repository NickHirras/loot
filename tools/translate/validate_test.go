package main

import (
	"strings"
	"testing"
)

// variant is a shorthand for building a plural message in a test.
func variant(selectors []string, match map[string]string) Value {
	return Value{Variant: &Variant{
		Declarations: []string{"input count", "local countPlural = count: plural"},
		Selectors:    selectors,
		Match:        match,
	}}
}

// check runs the validator and reports whether it passed, plus the joined
// messages so a failing test says what the validator actually complained about.
func check(t *testing.T, kind Kind, locale string, g *Glossary, english, translated Value) (bool, string) {
	t.Helper()
	v, err := NewValidator(kind, locale, g)
	if err != nil {
		t.Fatal(err)
	}
	ps, ok := v.Check("test_key", english, translated)
	var msgs []string
	for _, p := range ps {
		msgs = append(msgs, p.String())
	}
	return ok, strings.Join(msgs, "\n")
}

func TestValidatePlaceholders(t *testing.T) {
	en := Value{Text: "{value} / {target} {unit}"}

	// Reordering is fine; that is most of what translating is.
	if ok, msgs := check(t, KindMessages, "de", nil, en, Value{Text: "{unit}: {value} von {target}"}); !ok {
		t.Errorf("reordered placeholders should pass: %s", msgs)
	}
	if ok, msgs := check(t, KindMessages, "de", nil, en, Value{Text: "{value} / {target}"}); ok {
		t.Error("a dropped placeholder must fail")
	} else if !strings.Contains(msgs, "dropped placeholder") {
		t.Errorf("wrong complaint: %s", msgs)
	}
	if ok, msgs := check(t, KindMessages, "de", nil, en, Value{Text: "{value} / {target} {unit} {extra}"}); ok {
		t.Error("an invented placeholder must fail")
	} else if !strings.Contains(msgs, "invented placeholder") {
		t.Errorf("wrong complaint: %s", msgs)
	}
	// A renamed placeholder is both at once.
	if ok, _ := check(t, KindMessages, "de", nil, en, Value{Text: "{wert} / {target} {unit}"}); ok {
		t.Error("a translated placeholder name must fail")
	}
}

func TestValidatePlaceholdersInVariantArms(t *testing.T) {
	en := variant([]string{"countPlural"}, map[string]string{
		"countPlural=one":   "{count} drop",
		"countPlural=other": "{count} drops",
	})
	good := variant([]string{"countPlural"}, map[string]string{
		"countPlural=one":   "{count} Fund",
		"countPlural=other": "{count} Funde",
	})
	if ok, msgs := check(t, KindMessages, "de", nil, en, good); !ok {
		t.Errorf("should pass: %s", msgs)
	}
	bad := variant([]string{"countPlural"}, map[string]string{
		"countPlural=one":   "ein Fund",
		"countPlural=other": "{count} Funde",
	})
	if ok, msgs := check(t, KindMessages, "de", nil, en, bad); ok {
		t.Error("an arm that dropped a placeholder every English arm had must fail")
	} else if !strings.Contains(msgs, "dropped placeholder") {
		t.Errorf("wrong complaint: %s", msgs)
	}
}

func TestValidateVariantCategories(t *testing.T) {
	en := variant([]string{"countPlural"}, map[string]string{
		"countPlural=one":   "{count} drop",
		"countPlural=other": "{count} drops",
	})

	// Russian needs four arms.
	ru := variant([]string{"countPlural"}, map[string]string{
		"countPlural=one":   "{count} предмет",
		"countPlural=few":   "{count} предмета",
		"countPlural=many":  "{count} предметов",
		"countPlural=other": "{count} предмета",
	})
	if ok, msgs := check(t, KindMessages, "ru", nil, en, ru); !ok {
		t.Errorf("a full Russian set should pass: %s", msgs)
	}
	// Two arms is a German answer to a Russian question.
	ruShort := variant([]string{"countPlural"}, map[string]string{
		"countPlural=one":   "{count} предмет",
		"countPlural=other": "{count} предмета",
	})
	if ok, msgs := check(t, KindMessages, "ru", nil, en, ruShort); ok {
		t.Error("Russian is missing few/many and must fail")
	} else if !strings.Contains(msgs, "missing plural arms") {
		t.Errorf("wrong complaint: %s", msgs)
	}

	// Japanese has exactly one arm.
	ja := variant([]string{"countPlural"}, map[string]string{"countPlural=other": "{count} 件のドロップ"})
	if ok, msgs := check(t, KindMessages, "ja", nil, en, ja); !ok {
		t.Errorf("a single `other` arm should pass for Japanese: %s", msgs)
	}
	jaTwo := variant([]string{"countPlural"}, map[string]string{
		"countPlural=one":   "{count} 件のドロップ",
		"countPlural=other": "{count} 件のドロップ",
	})
	if ok, msgs := check(t, KindMessages, "ja", nil, en, jaTwo); ok {
		t.Error("Japanese has no `one` category; that arm could never be selected")
	} else if !strings.Contains(msgs, "not categories") {
		t.Errorf("wrong complaint: %s", msgs)
	}

	// No `other` at all.
	noOther := variant([]string{"countPlural"}, map[string]string{"countPlural=one": "eins"})
	if ok, _ := check(t, KindMessages, "de", nil, en, noOther); ok {
		t.Error("a message with no `other` arm must fail")
	}
}

func TestValidateVariantMachineryIsUntouched(t *testing.T) {
	en := variant([]string{"countPlural"}, map[string]string{
		"countPlural=one":   "one",
		"countPlural=other": "many",
	})
	tr := variant([]string{"countPlural"}, map[string]string{
		"countPlural=one":   "eins",
		"countPlural=other": "viele",
	})
	tr.Variant.Declarations = []string{"input anzahl", "local anzahlPlural = anzahl: plural"}
	if ok, msgs := check(t, KindMessages, "de", nil, en, tr); ok {
		t.Error("translated declarations must fail: they name variables the code passes in")
	} else if !strings.Contains(msgs, "declarations changed") {
		t.Errorf("wrong complaint: %s", msgs)
	}

	tr2 := variant([]string{"anzahlPlural"}, map[string]string{"anzahlPlural=other": "viele"})
	if ok, msgs := check(t, KindMessages, "de", nil, en, tr2); ok {
		t.Error("translated selectors must fail")
	} else if !strings.Contains(msgs, "selectors changed") {
		t.Errorf("wrong complaint: %s", msgs)
	}
}

func TestValidateTwoSelectorCrossProduct(t *testing.T) {
	en := Value{Variant: &Variant{
		Declarations: []string{"input drops", "input chests"},
		Selectors:    []string{"dropsPlural", "chestsPlural"},
		Match: map[string]string{
			"dropsPlural=one, chestsPlural=one":     "{dropsFmt} drop in {chests} chest.",
			"dropsPlural=one, chestsPlural=other":   "{dropsFmt} drop in {chests} chests.",
			"dropsPlural=other, chestsPlural=one":   "{dropsFmt} drops in {chests} chest.",
			"dropsPlural=other, chestsPlural=other": "{dropsFmt} drops in {chests} chests.",
		},
	}}
	// Japanese collapses a 2×2 into a 1×1.
	ja := Value{Variant: &Variant{
		Declarations: en.Variant.Declarations,
		Selectors:    en.Variant.Selectors,
		Match: map[string]string{
			"dropsPlural=other, chestsPlural=other": "{chests} 個のチェストに {dropsFmt} 件。",
		},
	}}
	if ok, msgs := check(t, KindMessages, "ja", nil, en, ja); !ok {
		t.Errorf("should pass: %s", msgs)
	}
	// French needs 3×3.
	if got := len(MatchKeys(en.Variant.Selectors, []string{"one", "many", "other"})); got != 9 {
		t.Errorf("French needs 9 arms for two selectors, MatchKeys gave %d", got)
	}
}

func TestValidateShapeMismatch(t *testing.T) {
	en := variant([]string{"countPlural"}, map[string]string{"countPlural=one": "a", "countPlural=other": "b"})
	if ok, msgs := check(t, KindMessages, "de", nil, en, Value{Text: "flach"}); ok {
		t.Error("a plural message answered with a flat string must fail")
	} else if !strings.Contains(msgs, "plural variants") {
		t.Errorf("wrong complaint: %s", msgs)
	}
	if ok, _ := check(t, KindMessages, "de", nil, Value{Text: "flat"}, en); ok {
		t.Error("a flat message answered with plural variants must fail")
	}
}

func TestValidateEmptyAndOverlong(t *testing.T) {
	en := Value{Text: "No loot yet."}
	if ok, msgs := check(t, KindMessages, "de", nil, en, Value{Text: "   "}); ok {
		t.Error("an empty translation must fail")
	} else if !strings.Contains(msgs, "empty") {
		t.Errorf("wrong complaint: %s", msgs)
	}
	long := strings.Repeat("sehr lang ", 20)
	if ok, msgs := check(t, KindMessages, "de", nil, en, Value{Text: long}); ok {
		t.Error("a translation more than 4× the English must fail")
	} else if !strings.Contains(msgs, "more than 4×") {
		t.Errorf("wrong complaint: %s", msgs)
	}
	// German runs long; 2× must still pass.
	if ok, msgs := check(t, KindMessages, "de", nil, en, Value{Text: "Noch keine Beute vorhanden."}); !ok {
		t.Errorf("ordinary German should pass: %s", msgs)
	}
}

func TestValidateTemplates(t *testing.T) {
	en := Value{Text: "Best day ever on {{.SourceName}}"}
	if ok, msgs := check(t, KindRules, "de", nil, en, Value{Text: "Bester Tag aller Zeiten auf {{.SourceName}}"}); !ok {
		t.Errorf("should pass: %s", msgs)
	}
	if ok, msgs := check(t, KindRules, "de", nil, en, Value{Text: "Bester Tag aller Zeiten auf {{.QuelleName}}"}); ok {
		t.Error("a translated expression must fail")
	} else if !strings.Contains(msgs, "not in the English template") {
		t.Errorf("wrong complaint: %s", msgs)
	}
	if ok, _ := check(t, KindRules, "de", nil, en, Value{Text: "Bester Tag aller Zeiten"}); ok {
		t.Error("a dropped expression must fail")
	}
	if ok, msgs := check(t, KindRules, "de", nil, en, Value{Text: "{{if .App}}{{.SourceName}}"}); ok {
		t.Error("an unbalanced template must fail")
	} else if !strings.Contains(msgs, "does not parse") {
		t.Errorf("wrong complaint: %s", msgs)
	}

	// Reordering is the point.
	enPair := Value{Text: "{{.App}} · {{.SourceName}}"}
	if ok, msgs := check(t, KindRules, "de", nil, enPair, Value{Text: "{{.SourceName}} · {{.App}}"}); !ok {
		t.Errorf("reordering expressions should pass: %s", msgs)
	}
	// Repetition is counted, not just membership.
	if ok, _ := check(t, KindRules, "de", nil, enPair, Value{Text: "{{.App}} · {{.App}}"}); ok {
		t.Error("using one expression twice and dropping another must fail")
	}
}

// German cannot say "Verkauf"+s the way English says "sale"+s, so the
// translation needs a branch English did not. That is a translation doing its
// job, not one editing the data, and the validator has to let it through.
func TestValidateTemplatesAllowExtraElseForPlurals(t *testing.T) {
	en := Value{Text: `{{.QuantityFmt}} sale{{if ne .Quantity 1}}s{{end}}`}
	de := Value{Text: `{{.QuantityFmt}} {{if ne .Quantity 1}}Verkäufe{{else}}Verkauf{{end}}`}
	if ok, msgs := check(t, KindRules, "de", nil, en, de); !ok {
		t.Errorf("an added {{else}} branch should pass: %s", msgs)
	}
	// But an added *expression* still must not.
	bad := Value{Text: `{{.QuantityFmt}} {{if ne .Quantity 1}}Verkäufe{{else if .App}}{{.App}}{{end}}`}
	if ok, _ := check(t, KindRules, "de", nil, en, bad); ok {
		t.Error("an invented {{else if}} expression must fail")
	}
}

func TestValidateGlossaryWarnsWithoutFailing(t *testing.T) {
	g := &Glossary{Terms: []Term{{
		Term:         "chest",
		Translations: map[string]string{"de": "Truhe"},
	}}}
	en := Value{Text: "Open the chest"}
	ok, msgs := check(t, KindMessages, "de", g, en, Value{Text: "Kiste öffnen"})
	if !ok {
		t.Errorf("a glossary miss is a warning, not a failure: %s", msgs)
	}
	if !strings.Contains(msgs, "warn:") || !strings.Contains(msgs, "Truhe") {
		t.Errorf("expected a glossary warning naming the pinned term: %s", msgs)
	}
	if _, msgs := check(t, KindMessages, "de", g, en, Value{Text: "Truhe öffnen"}); strings.Contains(msgs, "warn:") {
		t.Errorf("the pinned term was used; there should be no warning: %s", msgs)
	}
	// A string that does not use the English term is none of the glossary's
	// business.
	if _, msgs := check(t, KindMessages, "de", g, Value{Text: "Open the vault"}, Value{Text: "Tresor öffnen"}); strings.Contains(msgs, "warn:") {
		t.Errorf("unrelated string warned: %s", msgs)
	}
	// Whole words only: "chestnut" is not "chest".
	if _, msgs := check(t, KindMessages, "de", g, Value{Text: "A chestnut"}, Value{Text: "Eine Kastanie"}); strings.Contains(msgs, "warn:") {
		t.Errorf("substring match warned: %s", msgs)
	}
}

func TestPlaceholderExtraction(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"no placeholders", nil},
		{"{a} and {b}", []string{"{a}", "{b}"}},
		{"{a} and {a}", []string{"{a}"}},
		{`escaped \{not\} a slot`, nil},
		{"{count} / {target}", []string{"{count}", "{target}"}},
	}
	for _, c := range cases {
		got := placeholders(c.in)
		if len(got) != len(c.want) {
			t.Errorf("placeholders(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("placeholders(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestCountActionsIgnoresElseAndEnd(t *testing.T) {
	got := countActions(`{{if .App}}{{.App}}{{else}}x{{end}}`)
	if len(got) != 2 {
		t.Fatalf("got %v, want just the two expressions", got)
	}
	if got["{{if .App}}"] != 1 || got["{{.App}}"] != 1 {
		t.Errorf("got %v", got)
	}
}

func TestActionsRespectQuotedStrings(t *testing.T) {
	src := `{{if ne (printf "%v" .Payload.downloads) "1"}}s{{end}}`
	got := actions(src)
	if len(got) != 2 {
		t.Fatalf("got %d actions, want 2: %q", len(got), got)
	}
	if got[0] != `{{if ne (printf "%v" .Payload.downloads) "1"}}` {
		t.Errorf("first action was cut short: %q", got[0])
	}
}
