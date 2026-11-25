package main

import (
	"fmt"
	"log/slog"

	"github.com/nickhirras/loot/internal/config"
	"github.com/nickhirras/loot/internal/sources/cloudflareanalytics"
	"github.com/nickhirras/loot/internal/sources/metaads"
)

// The two funnel sources are built here, once, because `loot serve` and
// `loot check` must construct them identically: a check that passes with one
// set of options and a server that runs with another is worse than no check.

func newMetaAds(c config.MetaAds, log *slog.Logger) (*metaads.Source, error) {
	token, err := config.Secret(c.AccessToken, c.AccessTokenPath)
	if err != nil {
		return nil, fmt.Errorf("access token: %w", err)
	}
	return metaads.New(metaads.Options{
		AccessToken:       token,
		AdAccountID:       c.AdAccountID,
		App:               c.App,
		PixelID:           c.PixelID,
		ConversionActions: c.ConversionActions,
		APIVersion:        c.APIVersion,
		PollInterval:      c.PollInterval,
		BackfillDays:      c.BackfillDays,
		LookbackDays:      c.LookbackDays,
	}, log)
}

func newCloudflareAnalytics(c config.CloudflareAnalytics, log *slog.Logger) (*cloudflareanalytics.Source, error) {
	token, err := config.Secret(c.APIToken, c.APITokenPath)
	if err != nil {
		return nil, fmt.Errorf("api token: %w", err)
	}
	return cloudflareanalytics.New(cloudflareanalytics.Options{
		APIToken:     token,
		AccountID:    c.AccountID,
		App:          c.App,
		SQL:          c.SQL,
		PollInterval: c.PollInterval,
	}, log)
}
