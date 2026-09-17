package main

// i18n.lock.json: what the English source said the last time each translation
// was generated.
//
// The lock is the whole reason a run is cheap and a hand edit survives. It
// records a hash of the *English* value behind every translated key, per
// language. A key whose hash still matches is left alone — including a
// translation somebody corrected by hand, which is the point. A key whose hash
// moved is retranslated, because the English it was a translation of is gone.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// lockFileName is the lock's name at the repository root.
const lockFileName = "i18n.lock.json"

// lockPath is the lock file for a checkout.
func lockPath(root string) string { return filepath.Join(root, lockFileName) }

// Lock maps language → key → hash of the English value, for each catalog.
//
// The two catalogs are kept apart because they are keyed differently and
// translated separately: `-only-messages` must not disturb what the rules half
// recorded.
type Lock struct {
	Messages map[string]map[string]string `json:"messages"`
	Rules    map[string]map[string]string `json:"rules"`
}

// NewLock returns an empty lock.
func NewLock() *Lock {
	return &Lock{
		Messages: map[string]map[string]string{},
		Rules:    map[string]map[string]string{},
	}
}

// LoadLock reads the lock file. A missing file is an empty lock: the first run
// in a fresh checkout translates everything, which is correct.
func LoadLock(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewLock(), nil
	}
	if err != nil {
		return nil, err
	}
	l := NewLock()
	if err := json.Unmarshal(data, l); err != nil {
		return nil, err
	}
	if l.Messages == nil {
		l.Messages = map[string]map[string]string{}
	}
	if l.Rules == nil {
		l.Rules = map[string]map[string]string{}
	}
	return l, nil
}

// section returns the per-language map for one catalog kind.
func (l *Lock) section(kind Kind) map[string]map[string]string {
	if kind == KindRules {
		return l.Rules
	}
	return l.Messages
}

// Get returns the recorded English hash for one key, if there is one.
func (l *Lock) Get(kind Kind, locale, key string) (string, bool) {
	byKey, ok := l.section(kind)[locale]
	if !ok {
		return "", false
	}
	h, ok := byKey[key]
	return h, ok
}

// Set records the English hash a translation was generated from.
func (l *Lock) Set(kind Kind, locale, key, hash string) {
	sec := l.section(kind)
	if sec[locale] == nil {
		sec[locale] = map[string]string{}
	}
	sec[locale][key] = hash
}

// Delete forgets a key, for a source string that no longer exists.
func (l *Lock) Delete(kind Kind, locale, key string) {
	if byKey, ok := l.section(kind)[locale]; ok {
		delete(byKey, key)
	}
}

// Marshal renders the lock: sorted keys, two-space indent, trailing newline.
// encoding/json sorts map keys for us, which is all "canonical" has to mean
// here, and is what keeps the diff on a one-key change to one line.
func (l *Lock) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(l); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Save writes the lock file.
func (l *Lock) Save(path string) error {
	data, err := l.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
