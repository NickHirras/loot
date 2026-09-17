package main

import (
	"strings"
	"testing"
)

func decodeRequest() BatchRequest {
	en, _ := ParseMessages([]byte(sampleEN))
	return BatchRequest{
		Kind:         KindMessages,
		Locale:       "de",
		LanguageName: "German",
		Categories:   []string{"one", "other"},
		Items: []Item{
			{Key: "feed_empty_title", English: en["feed_empty_title"]},
			{
				Key:       "chest_row_drops",
				English:   en["chest_row_drops"],
				MatchKeys: MatchKeys(en["chest_row_drops"].Variant.Selectors, []string{"one", "other"}),
			},
		},
	}
}

func TestDecodeReply(t *testing.T) {
	req := decodeRequest()
	body := `{"translations":[
	  {"key":"feed_empty_title","text":"Noch keine Beute.","variants":[]},
	  {"key":"chest_row_drops","text":"","variants":[
	    {"match":"countPlural=one","text":"{count} Fund"},
	    {"match":"countPlural=other","text":"{count} Funde"}
	  ]},
	  {"key":"a_key_nobody_asked_for","text":"nope","variants":[]}
	]}`
	got, err := DecodeReply(body, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d values, want 2 (the unrequested key must be dropped)", len(got))
	}
	if got["feed_empty_title"].Text != "Noch keine Beute." {
		t.Errorf("plain message = %q", got["feed_empty_title"].Text)
	}
	v := got["chest_row_drops"]
	if !v.IsVariant() {
		t.Fatal("chest_row_drops should have come back as a variant")
	}
	// The machinery is copied from the English, not taken from the reply.
	src := req.Items[1].English.Variant
	if !equalStrings(v.Variant.Declarations, src.Declarations) {
		t.Errorf("declarations = %v, want the English ones %v", v.Variant.Declarations, src.Declarations)
	}
	if !equalStrings(v.Variant.Selectors, src.Selectors) {
		t.Errorf("selectors = %v, want %v", v.Variant.Selectors, src.Selectors)
	}
	if v.Variant.Match["countPlural=other"] != "{count} Funde" {
		t.Errorf("arms = %v", v.Variant.Match)
	}
}

func TestDecodeReplyIgnoresSurroundingProse(t *testing.T) {
	req := decodeRequest()
	body := "Here you go:\n```json\n" +
		`{"translations":[{"key":"feed_empty_title","text":"Noch keine Beute.","variants":[]}]}` +
		"\n```\nLet me know if you need more."
	got, err := DecodeReply(body, req)
	if err != nil {
		t.Fatal(err)
	}
	if got["feed_empty_title"].Text != "Noch keine Beute." {
		t.Errorf("got %v", got)
	}
}

func TestDecodeReplyRejectsGarbage(t *testing.T) {
	req := decodeRequest()
	for _, body := range []string{
		"I'm sorry, I can't help with that.",
		`{"translations":[{"key":"feed_empty_title"`,
		`{"translations":[]}`,
	} {
		if _, err := DecodeReply(body, req); err == nil {
			t.Errorf("expected an error for %q", body)
		}
	}
}

func TestExtractJSONObject(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"a":1}`, `{"a":1}`},
		{"prefix {\"a\":{\"b\":2}} suffix", `{"a":{"b":2}}`},
		{`{"a":"a } brace in a string"}`, `{"a":"a } brace in a string"}`},
		{`{"a":"an escaped \" quote }"}`, `{"a":"an escaped \" quote }"}`},
	}
	for _, c := range cases {
		got, err := extractJSONObject(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("extractJSONObject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, err := extractJSONObject("no object here"); err == nil {
		t.Error("expected an error when there is no object")
	}
	if _, err := extractJSONObject(`{"a": {`); err == nil {
		t.Error("expected an error for an unterminated object")
	}
}

func TestReplySchemaIsStrict(t *testing.T) {
	s := replySchema()
	if s["additionalProperties"] != false {
		t.Error("the top level should refuse additional properties")
	}
	props, _ := s["properties"].(map[string]any)
	arr, _ := props["translations"].(map[string]any)
	item, _ := arr["items"].(map[string]any)
	req, _ := item["required"].([]string)
	want := map[string]bool{"key": true, "text": true, "variants": true}
	if len(req) != len(want) {
		t.Errorf("required = %v, want key/text/variants (a strict schema needs them all)", req)
	}
	for _, r := range req {
		if !want[r] {
			t.Errorf("unexpected required property %q", r)
		}
	}
}

func TestUserPromptCarriesTheRequiredArmsAndHints(t *testing.T) {
	req := decodeRequest()
	req.Items[0].Hint = "The live feed of drops."
	p := userPrompt(req)
	for _, want := range []string{
		"German", "de", "one, other",
		"feed_empty_title", "The live feed of drops.",
		"chest_row_drops", "countPlural=one | countPlural=other",
		"No loot yet.",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestSystemPromptStatesTheHardRules(t *testing.T) {
	for _, want := range []string{
		"{placeholder}", "{{…}}", "App Store", "RevenueCat", "Loot",
		"CLDR", "short",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("the system prompt never mentions %q", want)
		}
	}
}
