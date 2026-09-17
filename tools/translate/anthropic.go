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
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// model is what does the translating. A plain string id: the SDK's typed
// constants lag model launches, and the id works on every SDK version.
const model = "claude-opus-5"

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
			Text: systemPrompt + "\n\n" + g.Prompt(),
			// One breakpoint at the end of the stable prefix. Every batch in
			// the run reads the same cache entry instead of re-paying for the
			// prompt and the glossary.
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		verbose: verbose,
	}
}

// errRefused is returned when the model declined rather than answered. The
// caller retries the batch smaller once; a refusal that survives that is
// reported against the keys rather than failing the run.
var errRefused = errors.New("the model declined to answer")

// Translate sends one batch and decodes the reply.
func (t *APITranslator) Translate(req BatchRequest) (map[string]Value, error) {
	// Structured outputs: the reply is constrained to this schema, so there is
	// no prose to strip and no half-written JSON to guess at.
	params := anthropic.MessageNewParams{
		Model:     model,
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

// reply is the shape the schema constrains the model to.
type reply struct {
	Translations []struct {
		Key      string `json:"key"`
		Text     string `json:"text"`
		Variants []struct {
			Match string `json:"match"`
			Text  string `json:"text"`
		} `json:"variants"`
	} `json:"translations"`
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
	obj, err := extractJSONObject(body)
	if err != nil {
		return nil, err
	}
	var r reply
	if err := json.Unmarshal([]byte(obj), &r); err != nil {
		return nil, fmt.Errorf("decode reply: %w", err)
	}

	wanted := make(map[string]Item, len(req.Items))
	for _, it := range req.Items {
		wanted[it.Key] = it
	}

	out := map[string]Value{}
	for _, tr := range r.Translations {
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
		return nil, fmt.Errorf("reply named none of the %d requested key(s)", len(req.Items))
	}
	return out, nil
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
