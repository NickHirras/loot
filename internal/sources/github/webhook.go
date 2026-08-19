package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// maxHookBody caps a webhook delivery. GitHub's own limit is 25 MB, but the
// events Loot reads are a few KB; anything larger is a mistake or an attack.
const maxHookBody = 4 << 20

// hookPayload is the union of the fields Loot reads across the handful of
// GitHub event types it understands. GitHub sends one JSON object per
// delivery, with the event type in the X-GitHub-Event header, so a single
// permissive struct is enough.
type hookPayload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`

	// star events carry the moment as a top-level starred_at.
	StarredAt *time.Time `json:"starred_at"`

	Issue *struct {
		Number    int        `json:"number"`
		Title     string     `json:"title"`
		HTMLURL   string     `json:"html_url"`
		CreatedAt time.Time  `json:"created_at"`
		ClosedAt  *time.Time `json:"closed_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		PullRequest *struct{} `json:"pull_request"`
	} `json:"issue"`

	PullRequest *struct {
		Number    int        `json:"number"`
		Title     string     `json:"title"`
		HTMLURL   string     `json:"html_url"`
		CreatedAt time.Time  `json:"created_at"`
		ClosedAt  *time.Time `json:"closed_at"`
		Merged    bool       `json:"merged"`
		MergedAt  *time.Time `json:"merged_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`

	Release *struct {
		ID          int64      `json:"id"`
		TagName     string     `json:"tag_name"`
		Name        string     `json:"name"`
		HTMLURL     string     `json:"html_url"`
		Draft       bool       `json:"draft"`
		Prerelease  bool       `json:"prerelease"`
		PublishedAt *time.Time `json:"published_at"`
	} `json:"release"`

	Forkee *struct {
		FullName  string    `json:"full_name"`
		HTMLURL   string    `json:"html_url"`
		CreatedAt time.Time `json:"created_at"`
		Owner     struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"forkee"`
}

// repoName is the "owner/name" the events should be attributed to.
func (p hookPayload) repoName() string {
	if p.Repository.FullName != "" {
		return p.Repository.FullName
	}
	if p.Repository.Owner.Login != "" && p.Repository.Name != "" {
		return p.Repository.Owner.Login + "/" + p.Repository.Name
	}
	return ""
}

// HandleWebhook implements core.WebhookHandler. It verifies the signature,
// maps the delivery onto the same events and dedupe keys the poller produces,
// and answers immediately — GitHub gives a webhook 10 seconds before it counts
// the delivery as failed, so nothing slow belongs here.
func (s *Source) HandleWebhook(w http.ResponseWriter, r *http.Request, emit func(core.Event)) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	if !s.verifySignature(r.Header.Get("X-Hub-Signature-256"), body) {
		s.log.Warn("github webhook rejected: bad or missing X-Hub-Signature-256")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	event := strings.ToLower(strings.TrimSpace(r.Header.Get("X-GitHub-Event")))
	if event == "ping" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "pong")
		return
	}

	var p hookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "cannot decode body: "+err.Error(), http.StatusBadRequest)
		return
	}

	events := s.EventsFromHook(event, p, s.now().UTC())
	for _, ev := range events {
		emit(ev)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"event":   event,
		"emitted": len(events),
	})
}

// verifySignature checks GitHub's HMAC. With no secret configured every
// delivery is accepted — which is fine on a private network and a bad idea
// anywhere else, so it says so once.
func (s *Source) verifySignature(header string, body []byte) bool {
	if s.cfg.WebhookSecret == "" {
		s.openHookWarned.Do(func() {
			s.log.Warn("github webhook has no secret set; anyone who can reach /hooks/github can inject drops " +
				"(set sources.github.webhook_secret or LOOT_GITHUB_WEBHOOK_SECRET)")
		})
		return true
	}

	sig, ok := strings.CutPrefix(strings.TrimSpace(header), "sha256=")
	if !ok {
		return false
	}
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.WebhookSecret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// Sign returns the value GitHub would send in X-Hub-Signature-256 for a body.
// Exported so tests — and anyone writing a relay — can produce a valid header.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// EventsFromHook maps one delivery onto Loot events. It is the webhook twin of
// the pollers above and deliberately mints the same dedupe keys, so a repo
// that is both polled and pushed produces one drop, not two. Unknown event
// types and uninteresting actions map to nothing at all.
func (s *Source) EventsFromHook(event string, p hookPayload, now time.Time) []core.Event {
	repo := p.repoName()
	if repo == "" {
		return nil
	}
	action := strings.ToLower(strings.TrimSpace(p.Action))

	switch event {
	case "star":
		// "watch" is the legacy name for the same thing; both arrive with the
		// starrer in `sender`.
		if action != "" && action != "created" {
			return nil
		}
		return s.starFromHook(repo, p, now)

	case "watch":
		if action != "" && action != "started" {
			return nil
		}
		return s.starFromHook(repo, p, now)

	case "fork":
		if p.Forkee == nil || p.Forkee.Owner.Login == "" {
			return nil
		}
		created := p.Forkee.CreatedAt
		if created.IsZero() {
			created = now
		}
		return []core.Event{s.forkEvent(repo, p.Forkee.Owner.Login, p.Forkee.HTMLURL, created, now)}

	case "issues":
		if p.Issue == nil {
			return nil
		}
		it := issueItem{
			Number:  p.Issue.Number,
			Title:   p.Issue.Title,
			HTMLURL: p.Issue.HTMLURL,
		}
		it.User.Login = p.Issue.User.Login
		switch action {
		case "opened", "reopened":
			at := p.Issue.CreatedAt
			if at.IsZero() {
				at = now
			}
			return []core.Event{s.issueEvent("issue_opened", repo, it, at, now)}
		case "closed":
			at := now
			if p.Issue.ClosedAt != nil {
				at = *p.Issue.ClosedAt
			}
			return []core.Event{s.issueEvent("issue_closed", repo, it, at, now)}
		}
		return nil

	case "pull_request":
		if p.PullRequest == nil {
			return nil
		}
		pr := p.PullRequest
		it := issueItem{
			Number:      pr.Number,
			Title:       pr.Title,
			HTMLURL:     pr.HTMLURL,
			PullRequest: &prRef{HTMLURL: pr.HTMLURL, MergedAt: pr.MergedAt},
		}
		it.User.Login = pr.User.Login
		switch action {
		case "opened", "reopened":
			at := pr.CreatedAt
			if at.IsZero() {
				at = now
			}
			return []core.Event{s.issueEvent("pr_opened", repo, it, at, now)}
		case "closed":
			// A closed-but-not-merged PR is a non-event: it is neither a win
			// worth a drop nor news the feed can act on.
			if !pr.Merged && pr.MergedAt == nil {
				return nil
			}
			at := now
			if pr.MergedAt != nil {
				at = *pr.MergedAt
			} else if pr.ClosedAt != nil {
				at = *pr.ClosedAt
			}
			return []core.Event{s.issueEvent("pr_merged", repo, it, at, now)}
		}
		return nil

	case "release":
		if p.Release == nil || action != "published" || p.Release.Draft || p.Release.TagName == "" {
			return nil
		}
		rel := p.Release
		at := now
		if rel.PublishedAt != nil {
			at = *rel.PublishedAt
		}
		payload, _ := json.Marshal(map[string]any{
			"tag":        rel.TagName,
			"name":       rel.Name,
			"url":        rel.HTMLURL,
			"prerelease": rel.Prerelease,
			"repo":       repo,
		})
		return []core.Event{{
			ID:         core.NewIDAt(at),
			Source:     Name,
			Kind:       "release",
			App:        repo,
			OccurredAt: at.UTC(),
			ObservedAt: now,
			Quantity:   1,
			DedupeKey:  fmt.Sprintf("github:release:%s:%s", repo, rel.TagName),
			Payload:    payload,
		}}
	}

	return nil
}

func (s *Source) starFromHook(repo string, p hookPayload, now time.Time) []core.Event {
	login := p.Sender.Login
	if login == "" {
		return nil
	}
	at := now
	if p.StarredAt != nil && !p.StarredAt.IsZero() {
		at = *p.StarredAt
	}
	return []core.Event{s.starEvent(repo, login, at, now)}
}
