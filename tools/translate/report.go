package main

// What the run says afterwards: a summary on stdout, the same thing as
// Markdown for the pull request body and the job summary, and a `changed`
// output so the workflow can skip opening a pull request that would be empty.

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Result is one language's outcome in one catalog.
type Result struct {
	Kind       Kind
	Locale     string
	Translated int
	Skipped    int
	Deleted    int
	Failed     []string
	Warnings   []Problem
	Problems   []Problem
}

// Report is the whole run.
type Report struct {
	Results []Result
	// Changed is true when any file on disk was written.
	Changed bool
	// DryRun notes that nothing was written because nothing was meant to be.
	DryRun bool
}

// Add records one language's outcome.
func (r *Report) Add(res Result) { r.Results = append(r.Results, res) }

// FailedCount is how many keys could not be translated or did not validate.
func (r *Report) FailedCount() int {
	n := 0
	for _, res := range r.Results {
		n += len(res.Failed)
	}
	return n
}

// TranslatedCount is how many keys were written.
func (r *Report) TranslatedCount() int {
	n := 0
	for _, res := range r.Results {
		n += res.Translated
	}
	return n
}

// Text renders the summary for stdout.
func (r *Report) Text() string {
	var b strings.Builder
	for _, res := range r.Results {
		fmt.Fprintf(&b, "%-8s %-8s translated %3d · skipped %3d · deleted %3d · failed %3d\n",
			res.Kind, res.Locale, res.Translated, res.Skipped, res.Deleted, len(res.Failed))
	}
	for _, res := range r.Results {
		for _, p := range res.Problems {
			fmt.Fprintf(&b, "  %s\n", p)
		}
	}
	fmt.Fprintf(&b, "\ntranslated %d · failed %d · changed %v\n",
		r.TranslatedCount(), r.FailedCount(), r.Changed)
	return b.String()
}

// Markdown renders the summary for a job summary and a pull request body.
func (r *Report) Markdown() string {
	var b strings.Builder
	b.WriteString("## Generated translations\n\n")
	if len(r.Results) == 0 {
		b.WriteString("Nothing to do: every translation is already up to date with the English source.\n")
		return b.String()
	}
	b.WriteString("| catalog | locale | translated | skipped | deleted | failed |\n")
	b.WriteString("|---|---|---:|---:|---:|---:|\n")
	for _, res := range r.Results {
		fmt.Fprintf(&b, "| %s | `%s` | %d | %d | %d | %d |\n",
			res.Kind, res.Locale, res.Translated, res.Skipped, res.Deleted, len(res.Failed))
	}

	if n := r.FailedCount(); n > 0 {
		fmt.Fprintf(&b, "\n### %d key(s) were left untranslated\n\n", n)
		b.WriteString("These did not come back, or came back failing validation. ")
		b.WriteString("They keep whatever the target file already said (English, if nothing).\n\n")
		for _, res := range r.Results {
			if len(res.Failed) == 0 {
				continue
			}
			fmt.Fprintf(&b, "**%s / `%s`**\n\n", res.Kind, res.Locale)
			for _, p := range res.Problems {
				if p.Warning {
					continue
				}
				fmt.Fprintf(&b, "- `%s` — %s\n", p.Key, p.Message)
			}
			// A key that failed with no problem recorded never came back at
			// all; name it anyway so the list is complete.
			named := map[string]bool{}
			for _, p := range res.Problems {
				named[p.Key] = true
			}
			for _, k := range res.Failed {
				if !named[k] {
					fmt.Fprintf(&b, "- `%s` — no translation came back\n", k)
				}
			}
			b.WriteString("\n")
		}
	}

	var warnings []Problem
	for _, res := range r.Results {
		warnings = append(warnings, res.Warnings...)
	}
	if len(warnings) > 0 {
		fmt.Fprintf(&b, "\n### %d warning(s)\n\n", len(warnings))
		b.WriteString("Written anyway — worth a glance.\n\n")
		for _, p := range warnings {
			fmt.Fprintf(&b, "- `%s` / `%s` `%s` — %s\n", p.Kind, p.Locale, p.Key, p.Message)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nEnglish is the only hand-maintained catalog. ")
	b.WriteString("To fix a translation, edit it in the locale file: it survives every later run until the English it translates changes. ")
	b.WriteString("To fix a word everywhere, pin it in `tools/translate/glossary.yaml`.\n")
	return b.String()
}

// WriteSummary puts the Markdown wherever the caller and the CI runner want
// it: a file named by -summary, and $GITHUB_STEP_SUMMARY when GitHub set one.
func (r *Report) WriteSummary(path string) error {
	md := r.Markdown()
	if path != "" {
		if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
			return err
		}
	}
	if step := os.Getenv("GITHUB_STEP_SUMMARY"); step != "" {
		f, err := os.OpenFile(step, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.WriteString(md); err != nil {
			return err
		}
	}
	return nil
}

// WriteGitHubOutput records whether anything changed, so the workflow can skip
// opening a pull request against an unchanged tree.
func (r *Report) WriteGitHubOutput() error {
	out := os.Getenv("GITHUB_OUTPUT")
	if out == "" {
		return nil
	}
	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "changed=%t\nfailed=%d\n", r.Changed, r.FailedCount())
	return err
}

// sortProblems puts a language's problems in a stable order, so two runs over
// the same failures print the same report.
func sortProblems(ps []Problem) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Warning != ps[j].Warning {
			return !ps[i].Warning
		}
		return ps[i].Key < ps[j].Key
	})
}
