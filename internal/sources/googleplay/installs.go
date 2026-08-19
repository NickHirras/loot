package googleplay

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
)

// installsPrefix is the bucket folder holding the statistics reports.
const installsPrefix = "stats/installs/"

// installsDimensions are the two report dimensions Loot reads. Play publishes
// a dozen more (device, os_version, carrier, language, app_version, tablets…);
// none carries a fact Loot has a use for, and every one is a lot of bytes.
var installsDimensions = []string{"overview", "country"}

// installsObjectRe splits stats/installs/installs_<package>_YYYYMM_<dimension>.csv.
// Package names contain dots and sometimes underscores, so the six-digit month
// is what anchors the split rather than the first underscore.
var installsObjectRe = regexp.MustCompile(`^stats/installs/installs_(.+)_(\d{6})_([a-z_]+)\.csv$`)

// InstallRow is one line of an install statistics report. Country is empty for
// the overview dimension and set for the country dimension.
type InstallRow struct {
	Date                 string
	Package              string
	Country              string
	DailyDeviceInstalls  int
	DailyUserInstalls    int
	ActiveDeviceInstalls int
	TotalUserInstalls    int
	// HasUserInstalls records whether the report actually had a "Daily User
	// Installs" column, so the fall back to device installs is a decision
	// rather than a silent zero.
	HasUserInstalls bool
}

// Installs is the figure Loot counts: user installs when the report has them,
// device installs otherwise. User installs count people; device installs count
// devices, and a person with a tablet and a phone is still one install.
func (r InstallRow) Installs() int {
	if r.HasUserInstalls {
		return r.DailyUserInstalls
	}
	return r.DailyDeviceInstalls
}

// ParseInstallsCSV reads one statistics CSV. These files are UTF-16LE with a
// BOM in every export Play has produced, but the encoding is sniffed rather
// than assumed.
func ParseInstallsCSV(raw []byte) ([]InstallRow, error) {
	h, records, err := readCSV(raw)
	if err != nil {
		return nil, fmt.Errorf("googleplay: installs report: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	if !h.has("date") {
		return nil, fmt.Errorf("googleplay: installs report has no \"Date\" column (got %d columns)", len(h))
	}
	hasUser := h.has("daily user installs")

	rows := make([]InstallRow, 0, len(records))
	for _, rec := range records {
		if len(rec) == 0 || strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		day, ok := parseReportDate(h.get(rec, "date"))
		if !ok {
			continue
		}
		rows = append(rows, InstallRow{
			Date:                day,
			Package:             h.get(rec, "package name", "package id", "product id"),
			Country:             validCountry(h.get(rec, "country", "country/region", "region")),
			DailyDeviceInstalls: parseInt(h.get(rec, "daily device installs")),
			DailyUserInstalls:   parseInt(h.get(rec, "daily user installs")),
			// "Active Device Installs" is the historical name; newer exports
			// say "Installs on Active Devices" and some say "Current Device
			// Installs" for the same column.
			ActiveDeviceInstalls: parseInt(h.get(rec,
				"active device installs", "installs on active devices", "current device installs")),
			TotalUserInstalls: parseInt(h.get(rec, "total user installs", "current user installs")),
			HasUserInstalls:   hasUser,
		})
	}
	return rows, nil
}

// pollInstalls reads the overview and country statistics for every package in
// the window and emits events for days past each package's cursor.
//
// Statistics dates are UTC and today's row is still accumulating, so only
// completed days are emitted — the same rule the Flathub source follows.
func (s *Source) pollInstalls(ctx context.Context, st *state, months []string, now time.Time) ([]core.Event, error) {
	objects, err := s.List(ctx, installsPrefix, 0)
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]bool, len(months))
	for _, m := range months {
		wanted[m] = true
	}
	today := now.UTC().Format(core.DayLayout)

	// The cursor is read from a snapshot and written once at the end, and it is
	// keyed per (package, dimension): the overview and country files for a
	// month are separate objects with separate md5s, so one of them can be
	// skipped as unchanged while the other is re-read, and a shared cursor
	// would then hide the skipped file's rows forever.
	start := make(map[string]string, len(st.InstallsCursor))
	for k, v := range st.InstallsCursor {
		start[k] = v
	}
	advanced := map[string]string{}

	// Objects are processed in name order so a poll is reproducible: overview
	// before country for a given month, oldest month first.
	sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })

	var (
		events []core.Event
		seen   = map[string]bool{}
	)

	for _, obj := range objects {
		m := installsObjectRe.FindStringSubmatch(obj.Name)
		if m == nil {
			continue
		}
		pkg, month, dimension := m[1], m[2], m[3]
		if !slices.Contains(installsDimensions, dimension) {
			continue
		}
		if !wanted[month] || !s.wantPackage(pkg) {
			continue
		}
		seen[obj.Name] = true
		if prev, ok := st.InstallsFiles[obj.Name]; ok && prev != "" && prev == obj.MD5Hash {
			s.Log.Debug("googleplay: installs report unchanged", "object", obj.Name)
			continue
		}

		raw, err := s.Download(ctx, obj.Name)
		if err != nil {
			return events, err
		}
		rows, err := ParseInstallsCSV(raw)
		if err != nil {
			return events, err
		}
		s.Log.Debug("googleplay: installs report read",
			"object", obj.Name, "rows", len(rows), "dimension", dimension)

		for _, r := range rows {
			rowPkg := r.Package
			if rowPkg == "" {
				rowPkg = pkg
			}
			if !s.wantPackage(rowPkg) {
				continue
			}
			cursor := installsCursorKey(rowPkg, dimension)
			if r.Date >= today || r.Date <= start[cursor] {
				continue
			}
			if r.Date > advanced[cursor] {
				advanced[cursor] = r.Date
			}
			if dimension == "country" {
				events = append(events, s.countryInstallEvents(r, rowPkg, now)...)
			} else {
				events = append(events, s.overviewInstallEvents(r, rowPkg, now)...)
			}
		}
		st.InstallsFiles[obj.Name] = obj.MD5Hash
	}

	for name := range st.InstallsFiles {
		if !seen[name] {
			delete(st.InstallsFiles, name)
		}
	}
	for cursor, day := range advanced {
		if day > st.InstallsCursor[cursor] {
			st.InstallsCursor[cursor] = day
		}
	}
	return events, nil
}

// overviewInstallEvents turns one day of the overview report into the silent
// counters plus the one drop-worthy event.
func (s *Source) overviewInstallEvents(r InstallRow, pkg string, observed time.Time) []core.Event {
	occurred, err := time.ParseInLocation(core.DayLayout, r.Date, time.UTC)
	if err != nil {
		return nil
	}
	installs := r.Installs()

	var events []core.Event
	if installs != 0 {
		events = append(events, core.Event{
			ID:         core.NewIDAt(occurred),
			Source:     Name,
			Kind:       "install",
			App:        pkg,
			OccurredAt: occurred,
			ObservedAt: observed,
			Day:        r.Date,
			Quantity:   installs,
			DedupeKey:  fmt.Sprintf("play:installs:%s:%s", pkg, r.Date),
			Silent:     true,
			Payload:    installsPayload(r, pkg, installs),
		})
	}
	if r.ActiveDeviceInstalls > 0 {
		events = append(events, core.Event{
			ID:         core.NewIDAt(occurred),
			Source:     Name,
			Kind:       "active_devices",
			App:        pkg,
			OccurredAt: occurred,
			ObservedAt: observed,
			Day:        r.Date,
			Quantity:   r.ActiveDeviceInstalls,
			DedupeKey:  fmt.Sprintf("play:active:%s:%s", pkg, r.Date),
			Silent:     true,
			Payload:    installsPayload(r, pkg, installs),
		})
	}
	// The day's headline. Chest-bound like a sales day, so a backfill fills
	// chests instead of spraying a fortnight of installs across the live feed.
	if installs > 0 {
		events = append(events, core.Event{
			ID:         core.NewIDAt(occurred),
			Source:     Name,
			Kind:       "installs_day",
			App:        pkg,
			OccurredAt: occurred,
			ObservedAt: observed,
			Day:        r.Date,
			Quantity:   installs,
			DedupeKey:  fmt.Sprintf("play:installs_day:%s:%s", pkg, r.Date),
			Chest:      true,
			Payload:    installsPayload(r, pkg, installs),
		})
	}
	return events
}

// countryInstallEvents turns one (day, country) row into a silent event. These
// are what found settlements — the first user in a country is news — and what
// the map will read later.
func (s *Source) countryInstallEvents(r InstallRow, pkg string, observed time.Time) []core.Event {
	if r.Country == "" {
		return nil
	}
	installs := r.Installs()
	if installs <= 0 {
		return nil
	}
	occurred, err := time.ParseInLocation(core.DayLayout, r.Date, time.UTC)
	if err != nil {
		return nil
	}
	return []core.Event{{
		ID:         core.NewIDAt(occurred),
		Source:     Name,
		Kind:       "install",
		App:        pkg,
		OccurredAt: occurred,
		ObservedAt: observed,
		Day:        r.Date,
		Country:    r.Country,
		Quantity:   installs,
		DedupeKey:  fmt.Sprintf("play:installs:%s:%s:%s", pkg, r.Date, r.Country),
		Silent:     true,
		Payload:    installsPayload(r, pkg, installs),
	}}
}

func installsPayload(r InstallRow, pkg string, installs int) json.RawMessage {
	m := map[string]any{
		"package":                pkg,
		"date":                   r.Date,
		"installs":               installs,
		"daily_device_installs":  r.DailyDeviceInstalls,
		"daily_user_installs":    r.DailyUserInstalls,
		"active_device_installs": r.ActiveDeviceInstalls,
		"total_user_installs":    r.TotalUserInstalls,
	}
	if r.Country != "" {
		m["country"] = r.Country
	}
	b, _ := json.Marshal(m)
	return b
}
