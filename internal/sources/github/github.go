// Package github is the "github" source. STUB: replaced by the Quest 7
// implementation.
package github

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier.
const Name = "github"

// Source implements core.Source.
type Source struct {
	cfg config.GitHub
	log *slog.Logger
	// Since, when set (from --since), overrides the backfill start.
	Since string
}

// New builds the source from its config.
func New(cfg config.GitHub, log *slog.Logger) (*Source, error) {
	return &Source{cfg: cfg, log: log}, nil
}

func (s *Source) Name() string { return Name }

// PollInterval is how often the scheduler calls Poll; 0 = webhook-only.
func (s *Source) PollInterval() time.Duration { return time.Hour }

// Poll is not implemented yet.
func (s *Source) Poll(ctx context.Context, state []byte) ([]core.Event, []byte, error) {
	return nil, state, nil
}

// Check reports whether the configuration works.
func (s *Source) Check(ctx context.Context) error { return errors.New("not implemented") }
