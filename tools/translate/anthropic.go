package main

// The one place this tool talks to the Claude API.
//
// Everything above it works on catalogs and plans; this turns a BatchRequest
// into a request and a reply into values. It is behind the Translator
// interface so the rest of the pipeline is testable without a key.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// defaultModel is what does the translating unless LOOT_TRANSLATE_MODEL says
// otherwise. Sonnet: this is short UI copy with a strong brief, and a run
// happens on every English edit, so the cost of the routine case matters
// more than the last few per cent of nuance. Set LOOT_TRANSLATE_MODEL to
// "claude-opus-5" for a one-off -force run when quality is the point. A
// plain string id: the SDK's typed constants lag model launches, and the id
// works on every SDK version.
const defaultModel = "claude-sonnet-5"

// modelID is the model a run uses.
func modelID() string {
	if m := strings.TrimSpace(os.Getenv("LOOT_TRANSLATE_MODEL")); m != "" {
		return m
	}
	return defaultModel
}

// maxTokens is the ceiling for one batch's reply. Forty short UI strings and
// their plural arms come nowhere near it; the headroom is for thinking, which
// is on by default on this model and shares the budget.
const maxTokens = 32000

// APITranslator is the real Translator.
type APITranslator struct {
	client anthropic.Client
	ctx    context.Context
	// system is the cached prefix: the stable prompt plus the glossary,
	// identical for every batch of every language in the run.
	system []anthropic.TextBlockParam
	// verbose prints token usage per batch.
	verbose bool
}

// NewAPITranslator builds a translator. The SDK reads ANTHROPIC_API_KEY from
// the environment on its own; apiKey overrides it when non-empty.
func NewAPITranslator(ctx context.Context, apiKey string, g *Glossary, verbose bool) *APITranslator {
	var opts []option.RequestOption
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	return &APITranslator{
		client: anthropic.NewClient(opts...),
		ctx:    ctx,
		system: []anthropic.TextBlockParam{{
			Text: SystemPrompt(g),
			// One breakpoint at the end of the stable prefix. Every batch in
			// the run reads the same cache entry instead of re-paying for the
			// prompt and the glossary.
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		verbose: verbose,
	}
}

// SystemPrompt is the cached prefix every request in a run shares: the stable
// prompt and the whole glossary. Offline mode writes the same string into each
// exported request, so a subagent answering one is told exactly what the API
// would have been told.
func SystemPrompt(g *Glossary) string { return systemPrompt + "\n\n" + g.Prompt() }

// errRefused is returned when the model declined rather than answered. The
// caller retries the batch smaller once; a refusal that survives that is
// reported against the keys rather than failing the run.
var errRefused = errors.New("the model declined to answer")

// Translate sends one batch and decodes the reply.
func (t *APITranslator) Translate(req BatchRequest) (map[string]Value, error) {
	// Structured outputs: the reply is constrained to this schema, so there is
	// no prose to strip and no half-written JSON to guess at.
	params := anthropic.MessageNewParams{
		Model:     modelID(),
		MaxTokens: maxTokens,
		System:    t.system,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt(req))),
		},
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: replySchema()},
		},
		// Thinking is left unset: adaptive is the default on this model, and
		// budget_tokens is not accepted on it at all.
	}

	// The client retries transport errors and 429/5xx on its own.
	msg, err := t.client.Messages.New(t.ctx, params)
	if err != nil {
		return nil, err
	}
	if msg.StopReason == anthropic.StopReasonRefusal {
		return nil, fmt.Errorf("%w: %s (%s)", errRefused, msg.StopDetails.Explanation, msg.StopDetails.Category)
	}
	if t.verbose {
		fmt.Printf("    %s/%s batch of %d: in %d (cache write %d, read %d), out %d\n",
			req.Kind, req.Locale, len(req.Items),
			msg.Usage.InputTokens, msg.Usage.CacheCreationInputTokens,
			msg.Usage.CacheReadInputTokens, msg.Usage.OutputTokens)
	}

	var text strings.Builder
	for _, block := range msg.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(tb.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return nil, fmt.Errorf("empty reply (stop reason %q)", msg.StopReason)
	}
	return DecodeReply(text.String(), req)
}

// reply is the shape the schema constrains the model to. The entries are held
// raw so that one malformed entry costs only itself: a hand-written reply file
// in offline mode is likelier to have a typo in it than a constrained one from
// the API, and the rest of the file is still worth having.
type reply struct {
	Translations []json.RawMessage `json:"translations"`
}

// translation is one entry of that array.
type translation struct {
	Key      string `json:"key"`
	Text     string `json:"text"`
	Variants []struct {
		Match string `json:"match"`
		Text  string `json:"text"`
	} `json:"variants"`
}

// replySchema is that shape as JSON Schema. Every property is required and
// additional ones are refused, which is what makes the constraint strict: a
// plain message comes back with an empty "variants", a plural one with an
// empty "text".
func replySchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"translations": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"key": map[string]any{
							"type":        "string",
							"description": "The key you were given, copied exactly.",
						},
						"text": map[string]any{
							"type":        "string",
							"description": "The translation, for a plain message. Empty for a plural message.",
						},
						"variants": map[string]any{
							"type":        "array",
							"description": "One entry per requested match key, for a plural message. Empty for a plain one.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"match": map[string]any{
										"type":        "string",
										"description": "One of the match keys you were asked for, copied exactly.",
									},
									"text": map[string]any{
										"type":        "string",
										"description": "The translation for that arm.",
									},
								},
								"required":             []string{"match", "text"},
								"additionalProperties": false,
							},
						},
					},
					"required":             []string{"key", "text", "variants"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"translations"},
		"additionalProperties": false,
	}
}

// DecodeReply turns a reply into values, keeping only the keys that were asked
// for and rebuilding each variant message's machinery from the English.
//
// The declarations and selectors are copied from the source rather than taken
// from the model. They name variables the dashboard passes in; there is
// nothing to translate about them, and not asking is one fewer way to be
// wrong.
func DecodeReply(body string, req BatchRequest) (map[string]Value, error) {
	out, _, err := DecodeReplyFor(body, ItemsByKey(req.Items))
	return out, err
}

// ItemsByKey indexes a batch's items by the key they translate.
func ItemsByKey(items []Item) map[string]Item {
	out := make(map[string]Item, len(items))
	for _, it := range items {
		out[it.Key] = it
	}
	return out
}

// DecodeReplyFor is DecodeReply against an arbitrary set of wanted items
// rather than one batch's. Offline mode decodes a reply file against every key
// of a catalog, because a file written by hand may answer keys from any batch
// — or from several.
//
// It returns the values it understood, a complaint per entry it could not read,
// and an error only when the reply as a whole was unusable.
func DecodeReplyFor(body string, wanted map[string]Item) (map[string]Value, []string, error) {
	obj, err := extractJSONObject(body)
	if err != nil {
		return nil, nil, err
	}
	var r reply
	if err := json.Unmarshal([]byte(obj), &r); err != nil {
		return nil, nil, fmt.Errorf("decode reply: %w", err)
	}
	if r.Translations == nil {
		return nil, nil, fmt.Errorf(`reply has no "translations" array`)
	}

	var complaints []string
	out := map[string]Value{}
	for i, raw := range r.Translations {
		var tr translation
		if err := json.Unmarshal(raw, &tr); err != nil {
			complaints = append(complaints, fmt.Sprintf("entry #%d does not match the schema: %v", i+1, err))
			continue
		}
		it, ok := wanted[tr.Key]
		if !ok {
			// A key nobody asked about. Dropping it is safer than writing it:
			// there is no English to validate it against.
			continue
		}
		if !it.English.IsVariant() {
			out[tr.Key] = Value{Text: tr.Text}
			continue
		}
		match := make(map[string]string, len(tr.Variants))
		for _, v := range tr.Variants {
			match[strings.TrimSpace(v.Match)] = v.Text
		}
		out[tr.Key] = Value{Variant: &Variant{
			Declarations: append([]string(nil), it.English.Variant.Declarations...),
			Selectors:    append([]string(nil), it.English.Variant.Selectors...),
			Match:        match,
		}}
	}
	if len(out) == 0 {
		return nil, complaints, fmt.Errorf("reply named none of the %d requested key(s)", len(wanted))
	}
	return out, complaints, nil
}

// extractJSONObject returns the first balanced {…} in a body.
//
// Structured outputs make this unnecessary in the normal case, and it costs
// nothing to be right anyway when a reply arrives wrapped in a fence or
// trailed by a sentence.
func extractJSONObject(body string) (string, error) {
	start := strings.IndexByte(body, '{')
	if start < 0 {
		return "", fmt.Errorf("no JSON object in reply")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(body); i++ {
		c := body[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("unterminated JSON object in reply")
}
