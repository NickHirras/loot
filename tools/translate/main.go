// Command translate generates Loot's translations from its English source.
//
// English is the only thing anybody writes by hand: web/messages/en.json for
// the dashboard and internal/rules/default.yaml for drop headlines. Every
// other language is generated from those by Claude, incrementally — only the
// keys whose English actually changed — validated before it is written, and
// delivered as a pull request by .github/workflows/translate.yml.
//
//	go -C tools/translate run . -dry-run          # what would be translated
//	go -C tools/translate run . -check            # validate what is there (no API)
//	go -C tools/translate run . -languages de,fr  # translate two languages
//	go -C tools/translate run . -force            # retranslate everything
//
// A translation somebody corrected by hand is left alone until the English it
// translates changes: what the tool compares is a hash of the English source,
// recorded per key per language in i18n.lock.json, never the translation
// itself.
//
// It lives in its own Go module so that the Anthropic SDK is not a dependency
// of the loot binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "translate: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		root      = flag.String("root", "", "repository root (default: found by walking up from the working directory)")
		languages = flag.String("languages", "", "comma-separated locales to translate, e.g. de,fr (default: every locale in project.inlang/settings.json)")
		force     = flag.Bool("force", false, "retranslate every key, ignoring the lock")
		onlyMsgs  = flag.Bool("only-messages", false, "only web/messages/<locale>.json")
		onlyRules = flag.Bool("only-rules", false, "only internal/rules/locales/default.<lang>.yaml")
		dryRun    = flag.Bool("dry-run", false, "print the plan and exit without calling the API")
		check     = flag.Bool("check", false, "validate the existing translations and exit non-zero on any problem; never calls the API")
		exportDir = flag.String("export", "", "write the plan to this directory as request files instead of calling the API (offline mode)")
		importDir = flag.String("import", "", "answer the plan from the reply files in this directory instead of calling the API (offline mode)")
		summary   = flag.String("summary", "", "also write the Markdown summary to this file")
		verbose   = flag.Bool("v", false, "list every key in the plan, and print token usage per batch")
	)
	flag.Parse()

	if *onlyMsgs && *onlyRules {
		return fmt.Errorf("-only-messages and -only-rules are mutually exclusive")
	}
	// The four modes each replace the API with something else; asking for two
	// of them at once has no sensible reading.
	var modes []string
	for _, m := range []struct {
		name string
		on   bool
	}{
		{"-dry-run", *dryRun},
		{"-check", *check},
		{"-export", *exportDir != ""},
		{"-import", *importDir != ""},
	} {
		if m.on {
			modes = append(modes, m.name)
		}
	}
	if len(modes) > 1 {
		return fmt.Errorf("%s are mutually exclusive", strings.Join(modes, " and "))
	}

	dir := *root
	if dir == "" {
		found, err := findRoot()
		if err != nil {
			return err
		}
		dir = found
	}

	opts := Options{
		Root:         dir,
		Force:        *force,
		OnlyMessages: *onlyMsgs,
		OnlyRules:    *onlyRules,
		DryRun:       *dryRun,
		Check:        *check,
		SummaryPath:  *summary,
		Verbose:      *verbose,
	}
	if *languages != "" {
		opts.Languages = strings.Split(*languages, ",")
	}

	// -dry-run and -check must never reach the network, so they are given no
	// translator at all rather than being trusted not to use one.
	if *dryRun {
		r, err := NewRunner(opts, nil)
		if err != nil {
			return err
		}
		return r.DryRun()
	}
	if *check {
		r, err := NewRunner(opts, nil)
		if err != nil {
			return err
		}
		rep, err := r.Check()
		if rep != nil && opts.SummaryPath != "" {
			_ = rep.WriteSummary(opts.SummaryPath)
		}
		return err
	}

	// -export builds the same plan and writes it out for a subagent to answer.
	// It calls nothing and writes nothing else.
	if *exportDir != "" {
		r, err := NewRunner(opts, nil)
		if err != nil {
			return err
		}
		return r.Export(*exportDir)
	}

	// -import runs the whole pipeline against a directory of replies, so it
	// needs no key either.
	var files *FileTranslator
	var translator Translator
	if *importDir != "" {
		if info, err := os.Stat(*importDir); err != nil || !info.IsDir() {
			return fmt.Errorf("-import: %q is not a directory (run -export first)", *importDir)
		}
		files = NewFileTranslator(*importDir)
		translator = files
	} else {
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return fmt.Errorf("ANTHROPIC_API_KEY is not set (use -dry-run to see the plan, -check to validate what is there, or -export/-import to translate offline)")
		}

		// Ctrl-C stops after the request in flight rather than half-writing a
		// catalog: everything is written per language, after validation.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		glossary, err := LoadGlossary()
		if err != nil {
			return err
		}
		translator = NewAPITranslator(ctx, key, glossary, *verbose)
	}

	r, err := NewRunner(opts, translator)
	if err != nil {
		return err
	}
	if files != nil {
		files.Items = r.AllItems
	}

	rep, runErr := r.Run()
	if rep != nil {
		if files != nil {
			rep.Notes = files.Notes()
		}
		// Only when something actually moved: a run that changed nothing has
		// nothing new to remember, and writing the lock anyway would create one
		// out of a no-op.
		if rep.Changed {
			if err := r.SaveLock(); err != nil {
				return err
			}
		}
		fmt.Print(rep.Text())
		if err := rep.WriteSummary(opts.SummaryPath); err != nil {
			return err
		}
		if err := rep.WriteGitHubOutput(); err != nil {
			return err
		}
	}
	if runErr != nil {
		return runErr
	}
	// A key that would not translate is reported, not fatal: the other
	// languages are still better off written than not.
	if rep.FailedCount() > 0 && rep.TranslatedCount() == 0 {
		return fmt.Errorf("every key failed (%d)", rep.FailedCount())
	}
	return nil
}

// findRoot walks up from the working directory looking for the checkout, so
// the tool runs the same from the repository root (`go -C tools/translate run
// .`) and from inside its own module (`cd tools/translate && go run .`).
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if isRoot(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find the Loot checkout above %q; pass -root", mustGetwd())
		}
		dir = parent
	}
}

// isRoot recognises the checkout by the two files this tool translates.
func isRoot(dir string) bool {
	for _, p := range []string{
		filepath.Join(dir, "web", "messages", BaseLocale+".json"),
		filepath.Join(dir, "internal", "rules", "default.yaml"),
	} {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

func mustGetwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}
