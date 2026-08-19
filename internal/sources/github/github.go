// Package github turns a repository's public life — stars, forks, issues,
// pull requests and releases — into Loot events.
//
// It works two ways at once, and they are designed to agree:
//
//   - Polling. Every 10 minutes it asks the REST API what changed. This needs
//     no setup beyond a repo name, works behind a firewall, and survives the
//     server being down.
//   - Webhooks. When a GitHub webhook is pointed at POST /hooks/github, the
//     same Source receives the push and emits the same events instantly.
//
// Both paths mint identical dedupe keys ("github:star:<repo>:<login>" and
// friends), so running them together gives real-time drops without doubles:
// whichever path sees an event first wins, and the other one collapses in the
// pipeline's dedupe step.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier, also the URL segment: /hooks/github.
const Name = "github"

// DefaultBaseURL is the public REST API root.
const DefaultBaseURL = "https://api.github.com"

// DefaultBackfillDays is how far the first poll looks back when the config
// does not say.
const DefaultBackfillDays = 30

// pollEvery is how often the scheduler calls Poll. GitHub's unauthenticated
// limit is 60 requests an hour and each repo costs ~5 requests a poll, so 10
// minutes is about as fast as a token-less setup can go with a repo or two.
const pollEvery = 10 * time.Minute

const (
	perPage = 100
	// maxStarPages caps how far back one poll walks the stargazer list: 20
	// pages is the 2000 most recent stars, far more than a poll interval can
	// realistically produce.
	maxStarPages = 20
	// maxReleases is how many releases each poll inspects.
	maxReleases = 20
	// maxListPages caps how many pages of issues or forks one poll walks. A
	// thousand items in ten minutes is not a repository, it is an incident, and
	// a poller that would page through it forever is worse than one that stops
	// and says where it got to.
	maxListPages = 10
	// dateLayout is the format of the --since flag.
	dateLayout = "2006-01-02"
)

// starMilestones are the star counts worth a drop of their own.
var starMilestones = []int{10, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

// Source implements core.Source, core.WebhookHandler and core.Checker.
type Source struct {
	cfg config.GitHub
	log *slog.Logger
	// Since, when set (from --since), overrides the backfill start.
	Since string

	// BaseURL is the API root; tests point it at an httptest server.
	BaseURL string
	// Client is the HTTP client used for every call.
	Client *http.Client
	// Now is swappable for tests.
	Now func() time.Time

	mu sync.Mutex
	// resetAt is when the exhausted rate limit frees up. Zero means "not
	// limited". Guarded by mu because the webhook handler and the poller run
	// on different goroutines.
	resetAt time.Time
	// remaining is the last X-RateLimit-Remaining seen, or -1 if unknown.
	remaining int

	// openHookWarned makes the "no webhook secret" warning fire once, not once
	// per delivery.
	openHookWarned sync.Once
}

// New builds the source from its config. It validates the repo list up front
// so a typo shows up at startup rather than ten minutes later in a log line.
func New(cfg config.GitHub, log *slog.Logger) (*Source, error) {
	if log == nil {
		log = slog.Default()
	}
	for _, repo := range cfg.Repos {
		if _, err := parseRepo(repo); err != nil {
			return nil, err
		}
	}
	return &Source{
		cfg:       cfg,
		log:       log,
		BaseURL:   DefaultBaseURL,
		Client:    &http.Client{Timeout: 30 * time.Second},
		Now:       time.Now,
		remaining: -1,
	}, nil
}

// Name implements core.Source.
func (s *Source) Name() string { return Name }

// PollInterval implements core.Source.
func (s *Source) PollInterval() time.Duration { return pollEvery }

// repoRef is an "owner/name" pair.
type repoRef struct{ Owner, Name string }

func (r repoRef) String() string { return r.Owner + "/" + r.Name }

func parseRepo(s string) (repoRef, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(strings.TrimSuffix(s, "/")), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return repoRef{}, fmt.Errorf("github: %q is not an owner/name repository", s)
	}
	return repoRef{Owner: owner, Name: name}, nil
}

// ---------------------------------------------------------------- state

// state is the persisted cursor blob, keyed by "owner/name".
type state struct {
	Repos map[string]*repoState `json:"repos"`
}

// repoState is one repository's set of cursors. An empty cursor means "this
// section has never run", which is what triggers the backfill window — kept
// per section so a repo that failed halfway through a poll resumes cleanly.
type repoState struct {
	StarsCount int `json:"stars_count"`
	// LastStarAt is the newest starred_at seen, RFC3339.
	LastStarAt string `json:"last_star_at"`
	// LastMilestone is the biggest star milestone already announced.
	LastMilestone int `json:"last_milestone"`
	// LastIssuePoll is the `since` value for the next issues query, RFC3339.
	LastIssuePoll string `json:"last_issue_poll"`
	// LastReleaseAt is the newest published_at seen, RFC3339.
	LastReleaseAt  string `json:"last_release_at"`
	LastReleaseTag string `json:"last_release_tag"`
	LastReleaseID  int64  `json:"last_release_id"`
	// LastForkAt is the newest fork created_at seen, RFC3339.
	LastForkAt string `json:"last_fork_at"`
}

func decodeState(raw []byte) *state {
	st := &state{Repos: map[string]*repoState{}}
	if len(raw) == 0 {
		return st
	}
	if err := json.Unmarshal(raw, st); err != nil || st.Repos == nil {
		return &state{Repos: map[string]*repoState{}}
	}
	return st
}

func (st *state) repo(key string) *repoState {
	rs, ok := st.Repos[key]
	if !ok || rs == nil {
		rs = &repoState{}
		st.Repos[key] = rs
	}
	return rs
}

// parseTime reads an RFC3339 cursor, returning the zero time for an empty or
// broken value (which reads as "no cursor" everywhere it is used).
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// firstRunFloor is the exclusive lower bound for a section's first ever run.
// --since pins it to a date; otherwise it is BackfillDays ago.
func (s *Source) firstRunFloor(now time.Time) time.Time {
	if s.Since != "" {
		if t, err := time.Parse(dateLayout, s.Since); err == nil {
			return t.UTC()
		}
		s.log.Warn("github: ignoring unparsable --since", "since", s.Since)
	}
	days := s.cfg.BackfillDays
	if days == 0 {
		days = DefaultBackfillDays
	}
	if days < 0 {
		days = 0
	}
	return now.AddDate(0, 0, -days)
}

// ---------------------------------------------------------------- polling

// Poll walks every configured repository and returns the events that appeared
// since the stored cursors.
func (s *Source) Poll(ctx context.Context, raw []byte) ([]core.Event, []byte, error) {
	st := decodeState(raw)
	now := s.now().UTC()

	if until, limited := s.rateLimitedUntil(now); limited {
		// Backing off is normal operation, not a failure: return the state
		// untouched and try again after the reset.
		s.log.Info("github: rate limit exhausted, skipping poll", "until", until.Format(time.RFC3339))
		return nil, raw, nil
	}

	var (
		events   []core.Event
		firstErr error
	)

	for _, name := range s.cfg.Repos {
		r, err := parseRepo(name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		rs := st.repo(r.String())

		evs, err := s.pollRepo(ctx, r, rs, now)
		events = append(events, evs...)
		if err != nil {
			if errors.Is(err, errRateLimited) {
				s.log.Info("github: rate limit hit mid-poll, stopping early", "repo", r.String())
				break
			}
			s.log.Warn("github poll failed", "repo", r.String(), "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	sort.SliceStable(events, func(i, j int) bool { return events[i].OccurredAt.Before(events[j].OccurredAt) })

	out, err := json.Marshal(st)
	if err != nil {
		return events, raw, fmt.Errorf("github: encode state: %w", err)
	}
	return events, out, firstErr
}

// pollRepo runs each section in turn. A section that fails leaves its own
// cursor untouched and the others still advance, so one 404 on forks cannot
// cost you the stars.
func (s *Source) pollRepo(ctx context.Context, r repoRef, rs *repoState, now time.Time) ([]core.Event, error) {
	var (
		events   []core.Event
		firstErr error
	)

	sections := []func(context.Context, repoRef, *repoState, time.Time) ([]core.Event, error){
		s.pollStars,
		s.pollIssues,
		s.pollReleases,
		s.pollForks,
	}
	for _, section := range sections {
		evs, err := section(ctx, r, rs, now)
		events = append(events, evs...)
		if err != nil {
			if errors.Is(err, errRateLimited) {
				return events, err
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return events, firstErr
}

// repoMeta is the slice of GET /repos/{owner}/{repo} that Loot reads.
type repoMeta struct {
	FullName        string `json:"full_name"`
	StargazersCount int    `json:"stargazers_count"`
	ForksCount      int    `json:"forks_count"`
	Private         bool   `json:"private"`
}

// stargazer is one entry of the star+json stargazers list.
type stargazer struct {
	StarredAt time.Time `json:"starred_at"`
	User      struct {
		Login   string `json:"login"`
		HTMLURL string `json:"html_url"`
	} `json:"user"`
}

// pollStars emits one "star" event per new stargazer, plus a "stars_milestone"
// whenever the running total crosses a round number.
//
// The stargazers endpoint returns oldest first and offers no `since`, so the
// newest stars live on the *last* page. Poll reads the repo's total to work
// out where that is and walks backwards, stopping at the first page whose
// oldest entry predates the cursor.
func (s *Source) pollStars(ctx context.Context, r repoRef, rs *repoState, now time.Time) ([]core.Event, error) {
	var meta repoMeta
	if err := s.get(ctx, fmt.Sprintf("/repos/%s/%s", r.Owner, r.Name), "", &meta); err != nil {
		return nil, err
	}
	total := meta.StargazersCount

	firstRun := rs.LastStarAt == ""
	floor := parseTime(rs.LastStarAt)
	if firstRun {
		floor = s.firstRunFloor(now)
	}

	var stars []stargazer
	if total > 0 {
		pages := (total + perPage - 1) / perPage
		for page, walked := pages, 0; page >= 1 && walked < maxStarPages; page, walked = page-1, walked+1 {
			var batch []stargazer
			path := fmt.Sprintf("/repos/%s/%s/stargazers?per_page=%d&page=%d", r.Owner, r.Name, perPage, page)
			if err := s.get(ctx, path, "application/vnd.github.star+json", &batch); err != nil {
				return nil, err
			}
			if len(batch) == 0 {
				continue
			}
			stars = append(stars, batch...)
			// batch[0] is the oldest entry on the page: once it is at or
			// before the cursor, every earlier page is older still.
			if !batch[0].StarredAt.After(floor) {
				break
			}
		}
	}

	var (
		events []core.Event
		newest time.Time
	)
	for _, sg := range stars {
		if sg.StarredAt.After(newest) {
			newest = sg.StarredAt
		}
		if !sg.StarredAt.After(floor) || sg.User.Login == "" {
			continue
		}
		events = append(events, s.starEvent(r.String(), sg.User.Login, sg.StarredAt, now))
	}
	sort.Slice(events, func(i, j int) bool { return events[i].OccurredAt.Before(events[j].OccurredAt) })

	// Milestones. The first run only records where the repo already stands:
	// installing Loot on a repo with 3000 stars should not fire five drops.
	if firstRun {
		rs.LastMilestone = highestMilestone(total)
	} else {
		for _, m := range starMilestones {
			if m > rs.LastMilestone && total >= m {
				events = append(events, s.milestoneEvent(r.String(), m, now))
				rs.LastMilestone = m
			}
		}
	}

	// A silent snapshot of where the repo actually stands. Star *events* only
	// exist for stars Loot watched arrive, so a repo that had three thousand
	// stars before Loot was installed would forever count as having none —
	// and the Codex's star achievements would sit locked at 0/1,000 next to a
	// repo that had passed the target years ago. One row a day, keyed on the
	// day, so re-polling costs nothing.
	events = append(events, s.starsTotalEvent(r.String(), total, now))

	rs.StarsCount = total
	switch {
	case newest.After(floor):
		rs.LastStarAt = formatTime(newest)
	case rs.LastStarAt == "":
		// No stars at all yet: seed the cursor so the backfill window is not
		// re-evaluated on every poll.
		rs.LastStarAt = formatTime(floor)
	}
	return events, nil
}

func highestMilestone(total int) int {
	best := 0
	for _, m := range starMilestones {
		if total >= m {
			best = m
		}
	}
	return best
}

func (s *Source) starEvent(repo, login string, starredAt, observed time.Time) core.Event {
	payload, _ := json.Marshal(map[string]any{"user": login, "repo": repo})
	return core.Event{
		ID:         core.NewIDAt(starredAt),
		Source:     Name,
		Kind:       "star",
		App:        repo,
		OccurredAt: starredAt.UTC(),
		ObservedAt: observed,
		Quantity:   1,
		DedupeKey:  fmt.Sprintf("github:star:%s:%s", repo, login),
		IsLedger:   false,
		Payload:    payload,
	}
}

// starsTotalEvent is the daily "this repo has N stars" level. It is silent:
// the number is context for the Codex, not news, and a drop a day saying the
// star count is unchanged is exactly the dashboard Loot is not.
func (s *Source) starsTotalEvent(repo string, total int, now time.Time) core.Event {
	payload, _ := json.Marshal(map[string]any{"repo": repo, "stars": total})
	return core.Event{
		ID:         core.NewIDAt(now),
		Source:     Name,
		Kind:       "stars_total",
		App:        repo,
		OccurredAt: now,
		ObservedAt: now,
		Day:        core.DayOf(now),
		Quantity:   total,
		DedupeKey:  fmt.Sprintf("github:stars_total:%s:%s", repo, core.DayOf(now)),
		IsLedger:   false,
		Silent:     true,
		Payload:    payload,
	}
}

func (s *Source) milestoneEvent(repo string, milestone int, now time.Time) core.Event {
	payload, _ := json.Marshal(map[string]any{"repo": repo, "stars": milestone})
	return core.Event{
		ID:         core.NewIDAt(now),
		Source:     Name,
		Kind:       "stars_milestone",
		App:        repo,
		OccurredAt: now,
		ObservedAt: now,
		Quantity:   milestone,
		DedupeKey:  fmt.Sprintf("github:stars_milestone:%s:%d", repo, milestone),
		IsLedger:   false,
		Payload:    payload,
	}
}

// issueItem is one entry of the issues list. GitHub's issues endpoint returns
// pull requests too, distinguished only by the presence of `pull_request`.
type issueItem struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	HTMLURL   string     `json:"html_url"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	PullRequest *prRef `json:"pull_request"`
}

// prRef is the `pull_request` stub the issues API attaches to items that are
// really pull requests. Its presence is the only way to tell the two apart.
type prRef struct {
	URL      string     `json:"url"`
	HTMLURL  string     `json:"html_url"`
	MergedAt *time.Time `json:"merged_at"`
}

// pullRequest is the fallback fetch for a closed PR whose list entry did not
// carry merged_at.
type pullRequest struct {
	Number   int        `json:"number"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
}

// pollIssues emits issue_opened / issue_closed / pr_opened / pr_merged.
//
// The list is paged. A busy repository (or a first run backfilling a month)
// has more than one page of issues touched since the cursor, and reading only
// the first while advancing the cursor to *now* threw the rest away silently —
// the drops for them could never arrive, because the next poll would never ask
// about them again. So every page is walked until a short one ends the list,
// and if the page cap stops the walk early the cursor is left at the oldest
// item actually seen rather than moved past what was never read.
func (s *Source) pollIssues(ctx context.Context, r repoRef, rs *repoState, now time.Time) ([]core.Event, error) {
	floor := parseTime(rs.LastIssuePoll)
	if rs.LastIssuePoll == "" {
		floor = s.firstRunFloor(now)
	}

	repo := r.String()
	var (
		events []core.Event
		oldest time.Time
		capped bool
	)
	for page := 1; ; page++ {
		if page > maxListPages {
			capped = true
			s.log.Warn("github: issue page cap reached; cursor held back",
				"repo", repo, "pages", maxListPages)
			break
		}
		path := fmt.Sprintf("/repos/%s/%s/issues?state=all&sort=updated&direction=desc&since=%s&per_page=%d&page=%d",
			r.Owner, r.Name, url.QueryEscape(formatTime(floor)), perPage, page)

		var items []issueItem
		if err := s.get(ctx, path, "", &items); err != nil {
			// Leave the cursor untouched: what was not read must still be
			// asked for next time.
			return events, err
		}
		for _, it := range items {
			if touched := issueTouchedAt(it); !touched.IsZero() && (oldest.IsZero() || touched.Before(oldest)) {
				oldest = touched
			}
			evs, err := s.issueEvents(ctx, r, repo, it, floor, now)
			if err != nil {
				return events, err
			}
			events = append(events, evs...)
		}
		if len(items) < perPage {
			break
		}
	}

	// Sorted by updated descending, so the walk runs out of relevant items
	// rather than out of pages; the cap is the exception, and it is the one
	// case where advancing to `now` would lose data.
	if capped && !oldest.IsZero() {
		rs.LastIssuePoll = formatTime(oldest)
	} else {
		rs.LastIssuePoll = formatTime(now)
	}
	return events, nil
}

// issueTouchedAt is when an item last moved, which is what `sort=updated`
// orders by. Older API responses omit updated_at, in which case the creation
// time is the most conservative stand-in.
func issueTouchedAt(it issueItem) time.Time {
	if !it.UpdatedAt.IsZero() {
		return it.UpdatedAt
	}
	if it.ClosedAt != nil && it.ClosedAt.After(it.CreatedAt) {
		return *it.ClosedAt
	}
	return it.CreatedAt
}

// issueEvents maps one list entry onto whichever of the four kinds it earned.
func (s *Source) issueEvents(ctx context.Context, r repoRef, repo string, it issueItem,
	floor, now time.Time,
) ([]core.Event, error) {
	var events []core.Event
	if it.PullRequest == nil {
		if it.CreatedAt.After(floor) {
			events = append(events, s.issueEvent("issue_opened", repo, it, it.CreatedAt, now))
		}
		if it.ClosedAt != nil && it.ClosedAt.After(floor) {
			events = append(events, s.issueEvent("issue_closed", repo, it, *it.ClosedAt, now))
		}
		return events, nil
	}

	if it.CreatedAt.After(floor) {
		events = append(events, s.issueEvent("pr_opened", repo, it, it.CreatedAt, now))
	}
	merged := it.PullRequest.MergedAt
	if merged == nil && strings.EqualFold(it.State, "closed") && it.ClosedAt != nil && it.ClosedAt.After(floor) {
		// The issues API is documented to carry merged_at inside
		// pull_request, but it is null on some responses and absent on
		// older API versions; a closed PR is worth one extra call to find
		// out whether it was merged or just abandoned.
		merged = s.fetchMergedAt(ctx, r, it.Number)
	}
	if merged != nil && merged.After(floor) {
		events = append(events, s.issueEvent("pr_merged", repo, it, *merged, now))
	}
	return events, nil
}

// fetchMergedAt asks the pulls endpoint whether a PR was merged. A failure is
// not fatal: it just means no pr_merged drop this time round.
func (s *Source) fetchMergedAt(ctx context.Context, r repoRef, number int) *time.Time {
	var pr pullRequest
	if err := s.get(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d", r.Owner, r.Name, number), "", &pr); err != nil {
		s.log.Debug("github: could not read pull request", "repo", r.String(), "number", number, "error", err)
		return nil
	}
	if pr.MergedAt != nil {
		return pr.MergedAt
	}
	return nil
}

func (s *Source) issueEvent(kind, repo string, it issueItem, occurred, observed time.Time) core.Event {
	link := it.HTMLURL
	if it.PullRequest != nil && it.PullRequest.HTMLURL != "" {
		link = it.PullRequest.HTMLURL
	}
	payload, _ := json.Marshal(map[string]any{
		"number": it.Number,
		"title":  it.Title,
		"user":   it.User.Login,
		"url":    link,
		"repo":   repo,
	})
	return core.Event{
		ID:         core.NewIDAt(occurred),
		Source:     Name,
		Kind:       kind,
		App:        repo,
		OccurredAt: occurred.UTC(),
		ObservedAt: observed,
		Quantity:   1,
		DedupeKey:  fmt.Sprintf("github:%s:%s:%d", kind, repo, it.Number),
		IsLedger:   false,
		Payload:    payload,
	}
}

// release is one entry of the releases list.
type release struct {
	ID          int64      `json:"id"`
	TagName     string     `json:"tag_name"`
	Name        string     `json:"name"`
	HTMLURL     string     `json:"html_url"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt *time.Time `json:"published_at"`
}

// pollReleases emits a "release" event per newly published release. Drafts are
// skipped (they are not public yet); prereleases are kept, flagged in the
// payload so a rule can treat a beta differently if you want it to.
func (s *Source) pollReleases(ctx context.Context, r repoRef, rs *repoState, now time.Time) ([]core.Event, error) {
	floor := parseTime(rs.LastReleaseAt)
	if rs.LastReleaseAt == "" {
		floor = s.firstRunFloor(now)
	}

	var list []release
	path := fmt.Sprintf("/repos/%s/%s/releases?per_page=%d", r.Owner, r.Name, maxReleases)
	if err := s.get(ctx, path, "", &list); err != nil {
		return nil, err
	}

	repo := r.String()
	var (
		events []core.Event
		newest time.Time
		newRel release
	)
	for _, rel := range list {
		if rel.Draft || rel.PublishedAt == nil || rel.TagName == "" {
			continue
		}
		if rel.PublishedAt.After(newest) {
			newest, newRel = *rel.PublishedAt, rel
		}
		if !rel.PublishedAt.After(floor) {
			continue
		}
		payload, _ := json.Marshal(map[string]any{
			"tag":        rel.TagName,
			"name":       rel.Name,
			"url":        rel.HTMLURL,
			"prerelease": rel.Prerelease,
			"repo":       repo,
		})
		events = append(events, core.Event{
			ID:         core.NewIDAt(*rel.PublishedAt),
			Source:     Name,
			Kind:       "release",
			App:        repo,
			OccurredAt: rel.PublishedAt.UTC(),
			ObservedAt: now,
			Quantity:   1,
			DedupeKey:  fmt.Sprintf("github:release:%s:%s", repo, rel.TagName),
			IsLedger:   false,
			Payload:    payload,
		})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].OccurredAt.Before(events[j].OccurredAt) })

	switch {
	case newest.After(floor):
		rs.LastReleaseAt = formatTime(newest)
		rs.LastReleaseTag = newRel.TagName
		rs.LastReleaseID = newRel.ID
	case rs.LastReleaseAt == "":
		rs.LastReleaseAt = formatTime(floor)
	}
	return events, nil
}

// fork is one entry of the forks list.
type fork struct {
	FullName  string    `json:"full_name"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	Owner     struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// pollForks emits a "fork" event per new fork, newest first from the API.
//
// Paged for the same reason issues are: a single page held the hundred newest
// forks, and the cursor then jumped to the newest of them — so anything past
// the hundredth was skipped over and could never be asked for again. The walk
// stops as soon as a page ends at or before the cursor, which on an ordinary
// poll is the first page.
func (s *Source) pollForks(ctx context.Context, r repoRef, rs *repoState, now time.Time) ([]core.Event, error) {
	floor := parseTime(rs.LastForkAt)
	if rs.LastForkAt == "" {
		floor = s.firstRunFloor(now)
	}

	repo := r.String()
	var (
		events []core.Event
		newest time.Time
		oldest time.Time
		capped bool
	)
	for page := 1; ; page++ {
		if page > maxListPages {
			capped = true
			s.log.Warn("github: fork page cap reached; cursor held back",
				"repo", repo, "pages", maxListPages)
			break
		}
		var list []fork
		path := fmt.Sprintf("/repos/%s/%s/forks?sort=newest&per_page=%d&page=%d",
			r.Owner, r.Name, perPage, page)
		if err := s.get(ctx, path, "", &list); err != nil {
			return events, err
		}
		if len(list) == 0 {
			break
		}
		for _, f := range list {
			if f.CreatedAt.After(newest) {
				newest = f.CreatedAt
			}
			if oldest.IsZero() || f.CreatedAt.Before(oldest) {
				oldest = f.CreatedAt
			}
			if !f.CreatedAt.After(floor) || f.Owner.Login == "" {
				continue
			}
			events = append(events, s.forkEvent(repo, f.Owner.Login, f.HTMLURL, f.CreatedAt, now))
		}
		if len(list) < perPage {
			break
		}
		// Newest first, so the last entry is the page's oldest: once it is at
		// or before the cursor, every later page is older still.
		if !list[len(list)-1].CreatedAt.After(floor) {
			break
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].OccurredAt.Before(events[j].OccurredAt) })

	switch {
	case capped && !oldest.IsZero():
		// Do not step over forks that were never read. Re-reading the ones
		// already emitted costs nothing: their dedupe keys collapse them.
		rs.LastForkAt = formatTime(oldest)
	case newest.After(floor):
		rs.LastForkAt = formatTime(newest)
	case rs.LastForkAt == "":
		rs.LastForkAt = formatTime(floor)
	}
	return events, nil
}

func (s *Source) forkEvent(repo, login, link string, createdAt, observed time.Time) core.Event {
	payload, _ := json.Marshal(map[string]any{"user": login, "url": link, "repo": repo})
	return core.Event{
		ID:         core.NewIDAt(createdAt),
		Source:     Name,
		Kind:       "fork",
		App:        repo,
		OccurredAt: createdAt.UTC(),
		ObservedAt: observed,
		Quantity:   1,
		DedupeKey:  fmt.Sprintf("github:fork:%s:%s", repo, login),
		IsLedger:   false,
		Payload:    payload,
	}
}

// ---------------------------------------------------------------- check

// Check implements core.Checker: it reads each repository's metadata, which
// exercises the token, the network and the repo names in one call apiece.
func (s *Source) Check(ctx context.Context) error {
	if len(s.cfg.Repos) == 0 {
		return errors.New("github: no repos configured")
	}
	for _, name := range s.cfg.Repos {
		r, err := parseRepo(name)
		if err != nil {
			return err
		}
		var meta repoMeta
		err = s.get(ctx, fmt.Sprintf("/repos/%s/%s", r.Owner, r.Name), "", &meta)
		if err == nil {
			continue
		}
		var ae *apiError
		if errors.As(err, &ae) {
			switch ae.Status {
			case http.StatusNotFound:
				return fmt.Errorf("github: %s: repo not found or token lacks access", r)
			case http.StatusUnauthorized:
				return fmt.Errorf("github: %s: bad token (401)", r)
			case http.StatusForbidden:
				return fmt.Errorf("github: %s: forbidden (%s)", r, s.rateNote())
			}
		}
		return fmt.Errorf("github: %s: %w", r, err)
	}

	remaining, resetAt := s.rateState()
	if remaining == 0 {
		return fmt.Errorf("github: rate limit exhausted, resets at %s", resetAt.Format(time.RFC3339))
	}
	s.log.Info("github check ok", "repos", len(s.cfg.Repos), "rate_limit_remaining", remaining)
	return nil
}

// rateNote renders the remaining rate limit for an error message.
func (s *Source) rateNote() string {
	remaining, resetAt := s.rateState()
	if remaining < 0 {
		return "rate limit unknown"
	}
	if remaining == 0 && !resetAt.IsZero() {
		return fmt.Sprintf("rate limit %d remaining, resets at %s", remaining, resetAt.Format(time.RFC3339))
	}
	return fmt.Sprintf("rate limit %d remaining", remaining)
}

// ---------------------------------------------------------------- http

// errRateLimited signals an exhausted rate limit. It is handled by backing off
// rather than by shouting: hitting the limit is a normal Tuesday for an
// unauthenticated poller.
var errRateLimited = errors.New("github: rate limit exhausted")

// apiError is a non-2xx response, kept structured so Check can turn a status
// code into advice.
type apiError struct {
	Status int
	Path   string
	Body   string
}

func (e *apiError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body != "" {
		return fmt.Sprintf("github: GET %s returned %d: %s", e.Path, e.Status, body)
	}
	return fmt.Sprintf("github: GET %s returned %d", e.Path, e.Status)
}

// get performs one API call and decodes the JSON body into out.
func (s *Source) get(ctx context.Context, path, accept string, out any) error {
	if until, limited := s.rateLimitedUntil(s.now()); limited {
		return fmt.Errorf("%w until %s", errRateLimited, until.Format(time.RFC3339))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL()+path, nil)
	if err != nil {
		return fmt.Errorf("github: build request: %w", err)
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "loot")
	if s.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.Token)
	}

	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("github: GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	exhausted := s.noteRateLimit(resp.Header)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if exhausted && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) {
			return fmt.Errorf("%w (GET %s)", errRateLimited, path)
		}
		return &apiError{Status: resp.StatusCode, Path: path, Body: string(body)}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("github: read %s: %w", path, err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("github: decode %s: %w", path, err)
	}
	return nil
}

// noteRateLimit records the rate limit headers and reports whether the budget
// is now spent.
func (s *Source) noteRateLimit(h http.Header) bool {
	remainingHdr := h.Get("X-RateLimit-Remaining")
	if remainingHdr == "" {
		return false
	}
	remaining, err := strconv.Atoi(remainingHdr)
	if err != nil {
		return false
	}

	var resetAt time.Time
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
			resetAt = time.Unix(secs, 0).UTC()
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.remaining = remaining
	if remaining > 0 {
		s.resetAt = time.Time{}
		return false
	}
	if resetAt.IsZero() {
		// No reset header: wait out a conservative minute rather than
		// hammering an endpoint that just said no.
		resetAt = s.now().UTC().Add(time.Minute)
	}
	s.resetAt = resetAt
	return true
}

// rateLimitedUntil reports whether calls should be held back, and until when.
func (s *Source) rateLimitedUntil(now time.Time) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resetAt.IsZero() {
		return time.Time{}, false
	}
	if !now.UTC().Before(s.resetAt) {
		s.resetAt = time.Time{}
		s.remaining = -1
		return time.Time{}, false
	}
	return s.resetAt, true
}

func (s *Source) rateState() (int, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remaining, s.resetAt
}

func (s *Source) baseURL() string {
	if s.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimSuffix(s.BaseURL, "/")
}

func (s *Source) client() *http.Client {
	if s.Client == nil {
		return http.DefaultClient
	}
	return s.Client
}

func (s *Source) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

var (
	_ core.Source         = (*Source)(nil)
	_ core.WebhookHandler = (*Source)(nil)
	_ core.Checker        = (*Source)(nil)
)
