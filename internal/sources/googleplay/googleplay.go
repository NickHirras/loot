package googleplay

import (
	"errors"
	"log/slog"

	"golang.org/x/oauth2"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/core"
)

// Name is the source identifier, and the source name every event carries.
const Name = "googleplay"

// StorageScope is the OAuth2 scope needed to read the reporting bucket.
const StorageScope = "https://www.googleapis.com/auth/devstorage.read_only"

// ErrNotImplemented is returned until the real source lands.
var ErrNotImplemented = errors.New("googleplay source is not implemented yet")

// tokenSource is the credential the finished source will authenticate with;
// declaring it here keeps golang.org/x/oauth2 in go.mod for that work.
var tokenSource oauth2.TokenSource

// New returns the Google Play source for cfg. The caller checks
// cfg.Configured() first; an unconfigured section means "the user did not ask
// for this source", not an error.
func New(cfg config.GooglePlay, log *slog.Logger) (core.Source, error) {
	if log == nil {
		log = slog.Default()
	}
	if !cfg.Configured() {
		return nil, errors.New("googleplay: service_account_json_path and bucket are both required")
	}
	_ = tokenSource
	return nil, ErrNotImplemented
}
