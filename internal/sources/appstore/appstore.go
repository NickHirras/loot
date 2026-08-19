package appstore

import (
	"errors"
	"log/slog"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier, and the source name every event carries.
const Name = "appstore"

// ErrNotImplemented is returned until the real source lands.
var ErrNotImplemented = errors.New("appstore source is not implemented yet")

// New returns the App Store Connect source for cfg. The caller checks
// cfg.Configured() first; an unconfigured section means "the user did not ask
// for this source", not an error.
func New(cfg config.AppStore, log *slog.Logger) (core.Source, error) {
	if log == nil {
		log = slog.Default()
	}
	if !cfg.Configured() {
		return nil, errors.New("appstore: key_id, issuer_id, private_key_path and vendor_number are all required")
	}
	return nil, ErrNotImplemented
}
