package quests

import "fmt"

// Refusals somebody will read.
//
// "target must be greater than zero" is a sentence, and a sentence has a
// language. The API still sends it — an older dashboard, a curl, a log line
// all want words — but it sends a Code beside it, so a dashboard that speaks
// something other than English can write the same refusal itself rather than
// showing the reader an English string it merely relayed.
//
// The codes are part of the API. Rename one and every translated form of that
// refusal silently falls back to English, so they are as permanent as an
// achievement key.
const (
	// CodeUnknownMetric is a metric no quest can count. Value is what was asked for.
	CodeUnknownMetric = "unknown_metric"
	// CodeTargetPositive is a target of zero or less.
	CodeTargetPositive = "target_positive"
	// CodeUnknownWindow is a window name that is not week, month or explicit
	// days. Value is what was asked for.
	CodeUnknownWindow = "unknown_window"
	// CodeWindowStartFormat and CodeWindowEndFormat are unparseable days.
	CodeWindowStartFormat = "window_start_format"
	CodeWindowEndFormat   = "window_end_format"
	// CodeWindowOrder is a window that ends before it starts.
	CodeWindowOrder = "window_order"
	// CodeCustomOnly is an attempt to delete a generated quest.
	CodeCustomOnly = "custom_only"
	// CodeQuestsDisabled is the whole feature being switched off.
	CodeQuestsDisabled = "quests_disabled"
	// CodeNotFound is an id that matches nothing.
	CodeNotFound = "not_found"
	// CodeBadWindowJSON is a `window` field that is neither a name nor a pair
	// of days. It is raised while decoding, in internal/server.
	CodeBadWindowJSON = "bad_window_json"
)

// Error is a refusal with a stable code: the English sentence for anybody who
// wants words, and the code for anybody who would rather write their own.
type Error struct {
	// Code is one of the constants above.
	Code string
	// Value is the offending input, for the refusals that quote one, and ""
	// for the ones that do not.
	Value string
	// Message is the English sentence, exactly as it has always read.
	Message string
}

func (e *Error) Error() string { return e.Message }

// errorf builds a coded refusal. `value` is the offending input for the
// refusals whose sentence quotes one, and "" otherwise.
func errorf(code, value, format string, args ...any) *Error {
	return &Error{Code: code, Value: value, Message: fmt.Sprintf(format, args...)}
}
