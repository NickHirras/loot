package rules

import _ "embed"

// DefaultYAML is the built-in rule set, embedded so a bare binary classifies
// events sensibly with no config file at all. Override with rules_path.
//
//go:embed default.yaml
var DefaultYAML []byte
