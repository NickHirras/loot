// Package mysteries watches Loot's own history for days that do not fit, and
// turns each one into an open question.
//
// A mystery is not an alert. Nothing is on fire, nothing needs acknowledging,
// and ignoring one costs nothing — it is an *invitation to be curious* about
// your own numbers: why did installs triple on the twelfth? Why did that
// Tuesday's revenue fall through the floor? The only opinionated one is
// `silence`, because a source that reported every day and then stopped is
// almost always a broken credential rather than a quiet week.
//
// Solving a mystery means writing down what you think happened. That is the
// real feature: after a few months the resolved list is a lab notebook of what
// actually moves your numbers, in your own words. It pays a drop, because
// writing it down deserves one.
package mysteries

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// The events Loot mints for itself when a mystery is solved.
const (
	eventSource      = "loot"
	KindSolved       = "mystery_solved"
	solvedDedupePfx  = "loot:mystery_solved:"
	defaultResolved  = 20
	sweepInterval    = time.Hour
	defaultDays      = 14
	defaultBaseline  = 28
	minBaselineDays  = 10
	minRecordHistory = 21
)

// Ingester is the slice of the pipeline this package needs.
type Ingester interface {
	Ingest(ctx context.Context, ev core.Event) (*core.Drop, error)
}

// Publisher is the slice of the bus this package needs.
type Publisher interface {
	Publish(msg bus.Message)
}

// Casebook is the whole of GET /api/mysteries.
type Casebook struct {
	// Open is everything still unexplained, newest day first.
	Open []core.Mystery `json:"open"`
	// Resolved is the last few solved or dismissed, newest first — the lab
	// notebook.
	Resolved []core.Mystery `json:"resolved"`
}

// Service reads and resolves mysteries. Detection lives in Detector.
type Service struct {
	Store  *store.Store
	Ingest Ingester
	Bus    Publisher
	Logger *slog.Logger
	Now    func() time.Time
}

// NewService returns a service over st.
func NewService(st *store.Store, ingest Ingester, b Publisher, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{Store: st, Ingest: ingest, Bus: b, Logger: log,
		Now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

// List returns the open mysteries and the recently resolved ones.
func (s *Service) List(ctx context.Context) (Casebook, error) {
	book := Casebook{Open: []core.Mystery{}, Resolved: []core.Mystery{}}

	open, err := s.Store.ListMysteries(ctx, store.MysteryQuery{Statuses: []string{core.MysteryOpen}})
	if err != nil {
		return book, err
	}
	book.Open = open

	resolved, err := s.Store.ListMysteries(ctx, store.MysteryQuery{
		Statuses: []string{core.MysterySolved, core.MysteryDismissed},
		Limit:    defaultResolved,
	})
	if err != nil {
		return book, err
	}
	book.Resolved = resolved
	return book, nil
}

// Solve closes a mystery with your explanation and pays a drop for it. Solving
// an already-resolved mystery is a no-op that returns the stored row, so a
// double click cannot mint two drops.
func (s *Service) Solve(ctx context.Context, id, note string) (core.Mystery, error) {
	note = strings.TrimSpace(note)
	m, err := s.Store.GetMystery(ctx, id)
	if err != nil {
		return core.Mystery{}, err
	}

	changed, err := s.Store.ResolveMystery(ctx, id, core.MysterySolved, note, s.now())
	if err != nil {
		return core.Mystery{}, err
	}
	if !changed {
		return s.Store.GetMystery(ctx, id)
	}
	m.Status = core.MysterySolved
	m.Note = note
	resolved := s.now().UTC()
	m.ResolvedAt = &resolved

	if err := s.award(ctx, m); err != nil {
		s.log().Error("mystery reward failed", "error", err, "mystery", id)
	}
	s.log().Info("mystery solved", "title", m.Title, "note", note)
	s.publish()
	return m, nil
}

// Dismiss closes a mystery quietly: no drop, no sound, no XP. Some days do not
// need an explanation.
func (s *Service) Dismiss(ctx context.Context, id string) (core.Mystery, error) {
	if _, err := s.Store.GetMystery(ctx, id); err != nil {
		return core.Mystery{}, err
	}
	if _, err := s.Store.ResolveMystery(ctx, id, core.MysteryDismissed, "", s.now()); err != nil {
		return core.Mystery{}, err
	}
	s.publish()
	return s.Store.GetMystery(ctx, id)
}

// solvedPayload is what the reward event carries.
type solvedPayload struct {
	MysteryID string `json:"mystery_id"`
	Title     string `json:"title"`
	Note      string `json:"note"`
}

func (s *Service) award(ctx context.Context, m core.Mystery) error {
	if s.Ingest == nil {
		return nil
	}
	payload, err := json.Marshal(solvedPayload{MysteryID: m.ID, Title: m.Title, Note: m.Note})
	if err != nil {
		return fmt.Errorf("mystery payload: %w", err)
	}
	now := s.now().UTC()
	_, err = s.Ingest.Ingest(ctx, core.Event{
		Source:     eventSource,
		Kind:       KindSolved,
		App:        m.App,
		OccurredAt: now,
		ObservedAt: now,
		Day:        core.DayOf(now),
		DedupeKey:  solvedDedupePfx + m.ID,
		Payload:    payload,
	})
	return err
}

// publish nudges connected browsers to refetch, mirroring the `chest` message.
func (s *Service) publish() {
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(bus.Message{Type: "mysteries"})
}
