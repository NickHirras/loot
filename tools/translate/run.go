package main

// The run itself: load English, work out what is stale, translate it,
// validate what comes back, write what passed, record what the English said.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// batchSize is how many keys go in one request. Small enough that a bad reply
// costs little and a truncated one is obvious; large enough that the cached
// prefix is worth having.
const batchSize = 40

// retryBatchSize is the size a failed batch is retried at, once. A batch
// usually fails because one string in it was awkward, and the smaller retry
// isolates that string instead of losing the other thirty-nine.
const retryBatchSize = 8

// Options is everything the flags decide.
type Options struct {
	Root         string
	Languages    []string
	Force        bool
	OnlyMessages bool
	OnlyRules    bool
	DryRun       bool
	Check        bool
	SummaryPath  string
	Verbose      bool
}

// Runner holds a run's inputs: the two English catalogs, the locale list, the
// lock and the glossary.
type Runner struct {
	opts     Options
	locales  []string
	messages Catalog
	rules    Catalog
	order    RuleOrder
	lock     *Lock
	glossary *Glossary
	// translate is the API, or a fake in tests. It is nil for -dry-run and
	// -check, which must never call out.
	translate Translator
}

// NewRunner loads everything a run needs from disk.
func NewRunner(opts Options, translator Translator) (*Runner, error) {
	locales, err := LoadLocales(settingsPath(opts.Root))
	if err != nil {
		return nil, err
	}
	if len(opts.Languages) > 0 {
		locales, err = restrict(locales, opts.Languages)
		if err != nil {
			return nil, err
		}
	}
	messages, err := LoadMessages(messagesPath(opts.Root, BaseLocale))
	if err != nil {
		return nil, fmt.Errorf("English messages: %w", err)
	}
	rules, order, err := LoadRules(rulesPath(opts.Root))
	if err != nil {
		return nil, fmt.Errorf("English rules: %w", err)
	}
	lock, err := LoadLock(lockPath(opts.Root))
	if err != nil {
		return nil, fmt.Errorf("lock file: %w", err)
	}
	glossary, err := LoadGlossary()
	if err != nil {
		return nil, err
	}
	return &Runner{
		opts:      opts,
		locales:   locales,
		messages:  messages,
		rules:     rules,
		order:     order,
		lock:      lock,
		glossary:  glossary,
		translate: translator,
	}, nil
}

// restrict narrows the locale list to the ones -languages named, rejecting a
// tag that is not in settings.json rather than silently doing nothing.
func restrict(all, want []string) ([]string, error) {
	have := setOf(all)
	var out []string
	for _, w := range want {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if !have[w] {
			return nil, fmt.Errorf("-languages: %q is not in web/project.inlang/settings.json (have %s)", w, strings.Join(all, ", "))
		}
		out = append(out, w)
	}
	if len(out) == 0 {
		return nil, errors.New("-languages: no languages given")
	}
	return out, nil
}

// kinds is the catalogs this run covers.
func (r *Runner) kinds() []Kind {
	switch {
	case r.opts.OnlyMessages:
		return []Kind{KindMessages}
	case r.opts.OnlyRules:
		return []Kind{KindRules}
	default:
		return []Kind{KindMessages, KindRules}
	}
}

// english returns the source catalog for one kind.
func (r *Runner) english(kind Kind) Catalog {
	if kind == KindRules {
		return r.rules
	}
	return r.messages
}

// targetPath is where a language's file for one kind lives.
func (r *Runner) targetPath(kind Kind, locale string) string {
	if kind == KindRules {
		return overlayPath(r.opts.Root, locale)
	}
	return messagesPath(r.opts.Root, locale)
}

// relPath is targetPath as the reader would type it, for the plan and the
// report. An absolute path in a summary table is mostly the runner's temp
// directory.
func (r *Runner) relPath(kind Kind, locale string) string {
	abs := r.targetPath(kind, locale)
	if rel, err := filepath.Rel(r.opts.Root, abs); err == nil {
		return rel
	}
	return abs
}

// loadTarget reads a language's current file.
func (r *Runner) loadTarget(kind Kind, locale string) (Catalog, error) {
	if kind == KindRules {
		return LoadOverlay(r.targetPath(kind, locale))
	}
	return LoadMessages(r.targetPath(kind, locale))
}

// Plans is the whole run's work, in a stable order.
func (r *Runner) Plans() ([]Plan, error) {
	var out []Plan
	for _, kind := range r.kinds() {
		for _, locale := range r.locales {
			target, err := r.loadTarget(kind, locale)
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", kind, locale, err)
			}
			out = append(out, BuildPlan(kind, locale, r.relPath(kind, locale),
				r.english(kind), target, r.lock, r.opts.Force))
		}
	}
	return out, nil
}

// DryRun prints the plan and touches nothing.
func (r *Runner) DryRun() error {
	plans, err := r.Plans()
	if err != nil {
		return err
	}
	totalTranslate, totalSkip, totalDelete := 0, 0, 0
	for _, p := range plans {
		fmt.Println(p)
		if r.opts.Verbose {
			for _, k := range p.Translate {
				fmt.Printf("    + %s\n", k)
			}
			for _, k := range p.Delete {
				fmt.Printf("    - %s\n", k)
			}
		}
		totalTranslate += len(p.Translate)
		totalSkip += p.Skip
		totalDelete += len(p.Delete)
	}
	fmt.Printf("\n%d locale(s): %d to translate, %d up to date, %d to delete\n",
		len(r.locales), totalTranslate, totalSkip, totalDelete)
	if totalTranslate > 0 && !r.opts.Verbose {
		fmt.Println("(pass -v to list the keys)")
	}
	return nil
}

// Check validates the English source and every target file that exists,
// without calling the API. This is what CI runs.
func (r *Runner) Check() (*Report, error) {
	rep := &Report{}
	problems := 0

	// The source first. A variant message in en.json whose arms are not
	// English's own categories would be mistranslated into every language, and
	// a rule template that does not parse is a bug nobody would see until a
	// drop landed.
	for _, kind := range r.kinds() {
		v, err := NewValidator(kind, BaseLocale, r.glossary)
		if err != nil {
			return nil, err
		}
		english := r.english(kind)
		for _, key := range english.Keys() {
			ps, ok := v.Check(key, english[key], english[key])
			for _, p := range ps {
				fmt.Println(p)
				if !p.Warning {
					problems++
				}
			}
			_ = ok
		}
	}

	for _, kind := range r.kinds() {
		english := r.english(kind)
		for _, locale := range r.locales {
			path := r.targetPath(kind, locale)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				continue
			}
			target, err := r.loadTarget(kind, locale)
			if err != nil {
				fmt.Printf("error: %s: %v\n", path, err)
				problems++
				continue
			}
			v, err := NewValidator(kind, locale, r.glossary)
			if err != nil {
				return nil, err
			}
			res := Result{Kind: kind, Locale: locale}
			for _, key := range sortedKeys(target) {
				src, ok := english[key]
				if !ok {
					// For rules this is the "an overlay may not invent a rule"
					// check; for messages it is a key the dashboard no longer
					// has, which is dead weight in the catalog.
					p := Problem{Kind: kind, Locale: locale, Key: key,
						Message: "not in the English source (a stale key: delete it, or run the tool to)"}
					res.Problems = append(res.Problems, p)
					res.Failed = append(res.Failed, key)
					continue
				}
				ps, ok := v.Check(key, src, target[key])
				for _, p := range ps {
					if p.Warning {
						res.Warnings = append(res.Warnings, p)
						continue
					}
					res.Problems = append(res.Problems, p)
				}
				if !ok {
					res.Failed = append(res.Failed, key)
				} else {
					res.Skipped++
				}
			}
			sortProblems(res.Problems)
			for _, p := range res.Problems {
				fmt.Println(p)
				problems++
			}
			for _, p := range res.Warnings {
				fmt.Println(p)
			}
			rep.Add(res)
		}
	}

	if problems > 0 {
		return rep, fmt.Errorf("%d problem(s) in the translated catalogs", problems)
	}
	fmt.Println("translations are valid")
	return rep, nil
}

// Run does the whole thing.
func (r *Runner) Run() (*Report, error) {
	if r.translate == nil {
		return nil, errors.New("no translator configured")
	}
	rep := &Report{}
	for _, kind := range r.kinds() {
		english := r.english(kind)
		for _, locale := range r.locales {
			res, err := r.runOne(kind, locale, english, rep)
			if err != nil {
				return rep, err
			}
			rep.Add(res)
		}
	}
	return rep, nil
}

// runOne translates one language's stale keys in one catalog and writes the
// result.
func (r *Runner) runOne(kind Kind, locale string, english Catalog, rep *Report) (Result, error) {
	res := Result{Kind: kind, Locale: locale}

	target, err := r.loadTarget(kind, locale)
	if err != nil {
		return res, fmt.Errorf("%s/%s: %w", kind, locale, err)
	}
	plan := BuildPlan(kind, locale, r.relPath(kind, locale), english, target, r.lock, r.opts.Force)
	res.Skipped = plan.Skip

	cats, err := PluralCategories(locale)
	if err != nil {
		return res, err
	}
	validator, err := NewValidator(kind, locale, r.glossary)
	if err != nil {
		return res, err
	}

	// Deletions first: a key English dropped leaves both the target and the
	// lock, whatever happens to the rest of the run.
	for _, key := range plan.Delete {
		delete(target, key)
		r.lock.Delete(kind, locale, key)
		res.Deleted++
	}

	if len(plan.Translate) > 0 {
		fmt.Printf("%s/%s: translating %d key(s)…\n", kind, locale, len(plan.Translate))
	}

	got := map[string]Value{}
	var failed []string
	for _, batch := range Batches(plan.Translate, batchSize) {
		out, missed := r.translateBatch(kind, locale, cats, english, batch)
		for k, v := range out {
			got[k] = v
		}
		failed = append(failed, missed...)
		// A translator that knows why a key came back empty says so against
		// the key, rather than leaving the report to guess at it.
		for _, key := range missed {
			if why := missingReason(r.translate, kind, locale, key); why != "" {
				res.Problems = append(res.Problems, Problem{Kind: kind, Locale: locale, Key: key, Message: why})
			}
		}
	}

	for _, key := range sortedKeys(got) {
		ps, ok := validator.Check(key, english[key], got[key])
		for _, p := range ps {
			if p.Warning {
				res.Warnings = append(res.Warnings, p)
				continue
			}
			res.Problems = append(res.Problems, p)
		}
		if !ok {
			failed = append(failed, key)
			continue
		}
		target[key] = got[key]
		r.lock.Set(kind, locale, key, english[key].Hash())
		res.Translated++
	}
	sort.Strings(failed)
	res.Failed = failed
	sortProblems(res.Problems)

	if res.Translated > 0 || res.Deleted > 0 {
		if err := r.writeTarget(kind, locale, target); err != nil {
			return res, err
		}
		rep.Changed = true
	}
	return res, nil
}

// translateBatch sends one batch, and on a refusal or a malformed reply
// retries it once in smaller pieces before giving up on the keys it still has
// nothing for.
func (r *Runner) translateBatch(kind Kind, locale string, cats []string, english Catalog, keys []string) (map[string]Value, []string) {
	req := r.request(kind, locale, cats, english, keys)
	out, err := r.translate.Translate(req)
	if err == nil {
		return out, missingKeys(keys, out)
	}
	// Retry once, smaller — always strictly smaller, so a short batch is not
	// simply resent unchanged.
	size := retryBatchSize
	if size >= len(keys) {
		size = (len(keys) + 1) / 2
	}
	fmt.Printf("  %s/%s: batch of %d failed (%v); retrying in %d-key pieces\n",
		kind, locale, len(keys), err, size)

	merged := map[string]Value{}
	for _, piece := range Batches(keys, size) {
		sub, err := r.translate.Translate(r.request(kind, locale, cats, english, piece))
		if err != nil {
			fmt.Printf("  %s/%s: %d key(s) failed: %v\n", kind, locale, len(piece), err)
			continue
		}
		for k, v := range sub {
			merged[k] = v
		}
	}
	return merged, missingKeys(keys, merged)
}

// request builds one BatchRequest.
func (r *Runner) request(kind Kind, locale string, cats []string, english Catalog, keys []string) BatchRequest {
	req := BatchRequest{
		Kind:         kind,
		Locale:       locale,
		LanguageName: LanguageName(locale),
		Categories:   cats,
	}
	for _, key := range keys {
		v := english[key]
		it := Item{Key: key, Hint: HintFor(kind, key), English: v}
		if v.IsVariant() {
			it.MatchKeys = MatchKeys(v.Variant.Selectors, cats)
		}
		req.Items = append(req.Items, it)
	}
	return req
}

// AllItems is every key of one catalog, as the Item the model would have been
// handed for it. Offline mode decodes a reply file against this rather than
// against one batch's items, because a file may answer keys from any batch.
func (r *Runner) AllItems(kind Kind, locale string) (map[string]Item, error) {
	cats, err := PluralCategories(locale)
	if err != nil {
		return nil, err
	}
	english := r.english(kind)
	req := r.request(kind, locale, cats, english, english.Keys())
	return ItemsByKey(req.Items), nil
}

// writeTarget puts a language's catalog back on disk.
func (r *Runner) writeTarget(kind Kind, locale string, c Catalog) error {
	if kind == KindRules {
		return WriteOverlay(r.targetPath(kind, locale), LanguageName(locale), c, r.order)
	}
	return WriteMessages(r.targetPath(kind, locale), c)
}

// SaveLock records what the English said, for the keys that were written.
func (r *Runner) SaveLock() error { return r.lock.Save(lockPath(r.opts.Root)) }

// missingKeys is the keys a reply did not answer.
func missingKeys(want []string, got map[string]Value) []string {
	var out []string
	for _, k := range want {
		if _, ok := got[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// sortedKeys is the sorted keys of a value map.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
