package demo

import "github.com/nickhirras/loot/internal/core"

// The cast: three fictional apps, the storefronts they sell in, and the
// products they sell. Everything the seeder and the live emitter invent is
// built out of this file, so tuning the demo world means editing data here
// rather than logic elsewhere.

// storefront is one country the fictional apps sell in.
type storefront struct {
	// Code is the ISO 3166-1 alpha-2 country code.
	Code string
	// Currency is what a store pays out in for that country. Several
	// storefronts really are paid in USD, which is why some non-US rows carry
	// it.
	Currency string
	// Rate is roughly how many units of Currency one US dollar buys. It only
	// has to be close: it exists so a Japanese sale reads as ¥600 rather than
	// ¥4, and the vault converts it back with real rates anyway.
	Rate float64
	// Weight is this storefront's pull in the sales lottery, relative to the
	// others unlocked so far.
	Weight float64
	// Unlock is how far into the seeded window the first customer from here
	// arrives, as a fraction of it. Countries appearing over time is what
	// makes settlements happen chronologically instead of all on day one.
	Unlock float64
}

// storefronts is the long tail: a handful of dominant markets and thirty-odd
// smaller ones that arrive over the months.
var storefronts = []storefront{
	{Code: "US", Currency: "USD", Rate: 1, Weight: 100, Unlock: 0},
	{Code: "JP", Currency: "JPY", Rate: 159.7, Weight: 34, Unlock: 0},
	{Code: "DE", Currency: "EUR", Rate: 0.864, Weight: 30, Unlock: 0},
	{Code: "GB", Currency: "GBP", Rate: 0.739, Weight: 26, Unlock: 0},
	{Code: "FR", Currency: "EUR", Rate: 0.864, Weight: 18, Unlock: 0},
	{Code: "CA", Currency: "CAD", Rate: 1.387, Weight: 14, Unlock: 0},
	{Code: "AU", Currency: "AUD", Rate: 1.406, Weight: 12, Unlock: 0},
	{Code: "BR", Currency: "BRL", Rate: 5.207, Weight: 11, Unlock: 0},
	{Code: "IN", Currency: "INR", Rate: 95.69, Weight: 10, Unlock: 0},

	{Code: "NL", Currency: "EUR", Rate: 0.864, Weight: 8, Unlock: 0.02},
	{Code: "IT", Currency: "EUR", Rate: 0.864, Weight: 7, Unlock: 0.03},
	{Code: "ES", Currency: "EUR", Rate: 0.864, Weight: 6, Unlock: 0.05},
	{Code: "SE", Currency: "SEK", Rate: 9.525, Weight: 6, Unlock: 0.06},
	{Code: "KR", Currency: "KRW", Rate: 1410, Weight: 6, Unlock: 0.08},
	{Code: "MX", Currency: "MXN", Rate: 17.05, Weight: 6, Unlock: 0.10},
	{Code: "CH", Currency: "CHF", Rate: 0.813, Weight: 4, Unlock: 0.12},
	{Code: "PL", Currency: "PLN", Rate: 3.731, Weight: 4, Unlock: 0.15},
	{Code: "NO", Currency: "NOK", Rate: 9.418, Weight: 3, Unlock: 0.18},
	{Code: "DK", Currency: "DKK", Rate: 6.458, Weight: 3, Unlock: 0.21},
	{Code: "AT", Currency: "EUR", Rate: 0.864, Weight: 3, Unlock: 0.24},
	{Code: "BE", Currency: "EUR", Rate: 0.864, Weight: 3, Unlock: 0.27},
	{Code: "SG", Currency: "SGD", Rate: 1.278, Weight: 3, Unlock: 0.31},
	{Code: "IE", Currency: "EUR", Rate: 0.864, Weight: 2.5, Unlock: 0.35},
	{Code: "FI", Currency: "EUR", Rate: 0.864, Weight: 2.5, Unlock: 0.39},
	{Code: "PT", Currency: "EUR", Rate: 0.864, Weight: 2.5, Unlock: 0.43},
	{Code: "CZ", Currency: "CZK", Rate: 20.89, Weight: 2, Unlock: 0.47},
	{Code: "HK", Currency: "HKD", Rate: 7.844, Weight: 2, Unlock: 0.51},
	{Code: "NZ", Currency: "NZD", Rate: 1.699, Weight: 2, Unlock: 0.55},
	{Code: "ZA", Currency: "ZAR", Rate: 16.22, Weight: 2, Unlock: 0.59},
	{Code: "TR", Currency: "TRY", Rate: 47.91, Weight: 2, Unlock: 0.63},
	{Code: "IL", Currency: "ILS", Rate: 2.992, Weight: 2, Unlock: 0.67},
	{Code: "TH", Currency: "THB", Rate: 33.06, Weight: 1.8, Unlock: 0.71},
	{Code: "AE", Currency: "USD", Rate: 1, Weight: 1.8, Unlock: 0.75},
	{Code: "MY", Currency: "MYR", Rate: 4.059, Weight: 1.5, Unlock: 0.79},
	{Code: "PH", Currency: "PHP", Rate: 61.78, Weight: 1.5, Unlock: 0.83},
	{Code: "ID", Currency: "IDR", Rate: 17862, Weight: 1.5, Unlock: 0.86},
	{Code: "RO", Currency: "RON", Rate: 4.530, Weight: 1.2, Unlock: 0.89},
	{Code: "HU", Currency: "HUF", Rate: 314.7, Weight: 1.2, Unlock: 0.92},
	{Code: "SA", Currency: "USD", Rate: 1, Weight: 1, Unlock: 0.94},
	{Code: "VN", Currency: "USD", Rate: 1, Weight: 1, Unlock: 0.96},
	{Code: "IS", Currency: "ISK", Rate: 122.8, Weight: 0.8, Unlock: 0.98},
}

// frontier is the set of countries deliberately left out of the seeded
// history, so the live emitter still has somewhere new to sell: founding a
// settlement is the best drop demo mode has, and it should be able to happen
// while someone is watching.
var frontier = []storefront{
	{Code: "CL", Currency: "USD", Rate: 1, Weight: 1},
	{Code: "CO", Currency: "USD", Rate: 1, Weight: 1},
	{Code: "PE", Currency: "USD", Rate: 1, Weight: 1},
	{Code: "KE", Currency: "USD", Rate: 1, Weight: 1},
	{Code: "MA", Currency: "USD", Rate: 1, Weight: 1},
	{Code: "GR", Currency: "EUR", Rate: 0.864, Weight: 1},
	{Code: "EE", Currency: "EUR", Rate: 0.864, Weight: 1},
	{Code: "UY", Currency: "USD", Rate: 1, Weight: 1},
}

// product is one thing an app sells.
type product struct {
	// SKU is the developer's own product id, as it appears in both stores'
	// reports.
	SKU string
	// ProductType is App Store Connect's Product Type Identifier: "1" a paid
	// app, "1F" a free download, "IA1" an in-app purchase, "IA9" a monthly
	// subscription, "IAY" an annual one.
	ProductType string
	// PlayType is Google Play's Product Type column.
	PlayType string
	// Kind is the Loot event kind the row becomes.
	Kind string
	// PriceUSD is what the customer pays, before the store's cut.
	PriceUSD float64
	// Share is the developer's share of the price (0.85 for a subscription
	// past its first year, 0.7 otherwise).
	Share float64
	// Weight is how often this product is the one that sold.
	Weight float64
	// Period labels subscriptions in payloads and RevenueCat events.
	Period string
}

// app is one fictional app, and the shape of its business.
type app struct {
	Name string
	// AppleID is the numeric App Store id, used in dedupe keys exactly as the
	// real source uses it.
	AppleID string
	// Package is the Google Play package name.
	Package string
	// Flatpak is the Flathub app id, empty for apps that are not on Flathub.
	Flatpak string
	// AppStore, Play and Flathub say which sources report this app at all.
	AppStore bool
	Play     bool
	Flathub  bool
	// RevenueCat says whether subscriptions also arrive in real time.
	RevenueCat bool
	// AppStoreBase and PlayBase are paid units on the first day of the
	// window; growth and seasonality are applied on top.
	AppStoreBase float64
	PlayBase     float64
	// InstallBase is daily installs on Google Play, FlathubBase on Flathub.
	InstallBase float64
	FlathubBase float64
	// Weekend multiplies weekend days: above 1 for an app people use on their
	// days off, below 1 for one they use at work.
	Weekend float64
	// Growth is how much bigger the app is at the end of the window than at
	// the start.
	Growth float64
	// LaunchAt, when non-zero, is the fraction of the window at which a
	// version launch spikes sales for a fortnight.
	LaunchAt float64
	// Subscribers is the active subscriber count at the start of the window,
	// reported daily as a subscription_snapshot.
	Subscribers int
	Products    []product
	// CrashBase is the app's ordinary daily crash count in Android vitals, and
	// CrashVersion the shipping version they are attributed to. Zero means the
	// app reports no vitals at all, which is what a Flathub-only app looks
	// like.
	CrashBase    float64
	CrashVersion string
	// Fights are the scripted boss battles in this app's history. Everything
	// else about a boss — its name, its HP, when it died — is worked out by
	// the real detector from the crash numbers below, exactly as it would be
	// for a real app.
	Fights []fight
}

// fight scripts one crash spike: a bad version shipping, and a fix rolling out
// behind it. The shape is deliberately the shape a real one has — one terrible
// day, then a geometric decay as the fixed build reaches more of the install
// base — because a demo that invents a shape the detector was not built for is
// a demo of nothing.
type fight struct {
	// Version is the build that broke, and Title the crash's own name.
	Version string
	Title   string
	// Kind is "crash" or "anr".
	Kind string
	// SpikeBefore is how many days before the end of the seeded window the
	// terrible day falls.
	SpikeBefore int
	// Peak is that day's crash count and Users how many people it hit.
	Peak  float64
	Users float64
	// Decay is the daily multiplier as the fix rolls out: 0.85 is a slow
	// staged rollout, 0.35 a hotfix that went out to everyone at once.
	Decay float64
	// Days is how long the fight's own series runs before the version stops
	// being reported at all.
	Days int
}

// apps is the demo catalogue: a subscription notes app, a weather app that
// spikes at weekends and shipped a big update, and a Linux-first tide clock
// that lives on Flathub.
var apps = []app{
	{
		Name:         "Lumen Notes",
		AppleID:      "1642305517",
		Package:      "com.lumenlabs.notes",
		AppStore:     true,
		Play:         true,
		RevenueCat:   true,
		AppStoreBase: 21,
		PlayBase:     12,
		InstallBase:  240,
		Weekend:      0.78,
		Growth:       2.1,
		Subscribers:  1480,
		CrashBase:    6,
		CrashVersion: "4.1.3",
		// The fight the demo opens on: a bad build six days ago, a staged
		// rollout draining it since. It is deliberately still standing —
		// the whole point of the Quests tab is the one you have not won yet.
		Fights: []fight{{
			Version:     "4.2.0",
			Title:       "NullPointerException in SyncWorker.onRun",
			SpikeBefore: 6,
			Peak:        312,
			Users:       91,
			Decay:       0.85,
			Days:        14,
		}},
		Products: []product{
			{SKU: "notes.pro.monthly", ProductType: "IA9", PlayType: "Subscription", Kind: "subscription", PriceUSD: 4.99, Share: 0.7, Weight: 52, Period: "MONTHLY"},
			{SKU: "notes.pro.annual", ProductType: "IAY", PlayType: "Subscription", Kind: "subscription", PriceUSD: 39.99, Share: 0.7, Weight: 16, Period: "ANNUAL"},
			{SKU: "notes.themes.pack", ProductType: "IA1", PlayType: "InApp Product", Kind: "iap", PriceUSD: 2.99, Share: 0.7, Weight: 18},
			{SKU: "com.lumenlabs.notes", ProductType: "1F", PlayType: "Free", Kind: "download", PriceUSD: 0, Share: 0, Weight: 14},
		},
	},
	{
		Name:         "Orbit Weather",
		AppleID:      "1708844390",
		Package:      "app.orbitweather.android",
		AppStore:     true,
		Play:         true,
		RevenueCat:   true,
		AppStoreBase: 14,
		PlayBase:     9,
		InstallBase:  310,
		Weekend:      1.32,
		Growth:       1.7,
		LaunchAt:     0.66,
		Subscribers:  610,
		CrashBase:    5,
		CrashVersion: "3.0.4",
		// The one on the shelf: a much worse crash last month, hotfixed in a
		// day, dead in five. It is what a won fight looks like afterwards.
		Fights: []fight{{
			Version:     "3.1.0",
			Title:       "ANR in RadarTileRenderer.draw",
			Kind:        core.BossKindANR,
			SpikeBefore: 34,
			Peak:        620,
			Users:       210,
			Decay:       0.35,
			Days:        9,
		}},
		Products: []product{
			{SKU: "app.orbitweather.pro", ProductType: "1", PlayType: "Paid App", Kind: "sale", PriceUSD: 2.99, Share: 0.7, Weight: 44},
			{SKU: "orbit.radar.monthly", ProductType: "IA9", PlayType: "Subscription", Kind: "subscription", PriceUSD: 1.99, Share: 0.7, Weight: 24, Period: "MONTHLY"},
			{SKU: "orbit.radar.annual", ProductType: "IAY", PlayType: "Subscription", Kind: "subscription", PriceUSD: 14.99, Share: 0.7, Weight: 9, Period: "ANNUAL"},
			{SKU: "orbit.storm.alerts", ProductType: "IA1", PlayType: "InApp Product", Kind: "iap", PriceUSD: 0.99, Share: 0.7, Weight: 23},
		},
	},
	{
		Name:        "Tidewatch",
		AppleID:     "",
		Package:     "net.tidewatch.mobile",
		Flatpak:     "net.tidewatch.Tidewatch",
		Play:        true,
		Flathub:     true,
		PlayBase:    6,
		InstallBase: 190,
		FlathubBase: 320,
		Weekend:     1.18,
		Growth:      2.6,
		Products: []product{
			{SKU: "net.tidewatch.mobile", PlayType: "Paid App", Kind: "sale", PriceUSD: 3.99, Share: 0.7, Weight: 60},
			{SKU: "tide.charts.pack", PlayType: "InApp Product", Kind: "iap", PriceUSD: 1.99, Share: 0.7, Weight: 40},
		},
	},
}
