package mysteries

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/bus"
	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/store"
)

// How a day gets flagged.
//
// Every series is measured against its own recent past with a *robust*
// baseline: the median of the trailing 28 days, and the median absolute
// deviation around it. Robust matters here because the thing being looked for
// — one enormous day — is exactly the thing that would poison a mean and a
// standard deviation into never noticing it again.
//
// The score is the modified z-score, 0.6745 * (x - median) / MAD, and a day
// has to clear |z| >= 3.5 *and* move by an amount worth mentioning (ten
// installs, five units, ten of your currency). Both halves are needed: a
// series that idles at 1 and jumps to 4 is statistically enormous and
// practically nothing.
const (
	// ZThreshold is how far from the baseline a day must sit to be flagged.
	ZThreshold = 3.5
	// zScale is the constant that puts the median absolute deviation on the
	// same footing as a standard deviation for normal data.
	zScale = 0.6745
	// madToStd converts a mean absolute deviation into a standard-deviation
	// estimate, used when the MAD is zero because the baseline barely moves.
	madToStd = 1.253314
	// zUnbounded stands in for an infinite score: a baseline with no spread at
	// all, broken by a day that is meaningfully different.
	zUnbounded = 99
	// refundMultiple is how many times its usual refunds a day needs.
	refundMultiple = 3
	// minRefunds is the floor below which a refund day is not worth a question.
	minRefunds = 3
	// recordMultiple is how far past the previous best a record has to go.
	recordMultiple = 1.25
	// clusterSize is how many countries settled on one day makes a cluster.
	clusterSize = 3
	// silenceDays is how many completed days a source must miss to count as
	// quiet *before its own settlement lag is added*, and silenceDensity how
	// many of the seven days before that it must have reported on for the
	// silence to mean anything.
	silenceDays    = 2
	silenceDensity = 5
	// resumedNote is written on a silence mystery that answered itself.
	resumedNote = "resumed"
	// seriesPoints is how many days of context a mystery carries for its
	// sparkline.
	seriesPoints = 28
)

// minDelta is the smallest absolute move worth a question, per series.
var minDelta = map[store.SeriesMetric]float64{
	store.SeriesInstalls:      10,
	store.SeriesUnits:         5,
	store.SeriesRevenue:       10,
	store.SeriesRefunds:       minRefunds,
	store.SeriesCancellations: 3,
}

// scanOrder is the order series are examined in. It matters: at most one
// mystery is raised per (source, app, day), so a day whose refunds exploded is
// reported as a refund story rather than as three overlapping ones.
var scanOrder = []store.SeriesMetric{
	store.SeriesRefunds,
	store.SeriesInstalls,
	store.SeriesRevenue,
	store.SeriesUnits,
	store.SeriesCancellations,
}

// Detector re-reads recent history and flags the days that do not fit.
//
// It is idempotent: every mystery it raises is keyed on (kind, source, app,
// metric, day), so running it hourly over the same fortnight costs a few
// queries and creates nothing new.
type Detector struct {
	Store  *store.Store
	Bus    Publisher
	Logger *slog.Logger
	// DisplayCurrency is what a money figure in a title is written in.
	DisplayCurrency string
	// Days is how many completed days back the detector looks for flags
	// (default 14). Baseline is how many trailing days each flag is measured
	// against (default 28).
	Days     int
	Baseline int
	// Now is the clock, in UTC.
	Now func() time.Time
}

// NewDetector returns a detector over st with the default windows.
func NewDetector(st *store.Store, b Publisher, displayCurrency string, log *slog.Logger) *Detector {
	if log == nil {
		log = slog.Default()
	}
	return &Detector{
		Store:           st,
		Bus:             b,
		Logger:          log,
		DisplayCurrency: displayCurrency,
		Days:            defaultDays,
		Baseline:        defaultBaseline,
		Now:             func() time.Time { return time.Now().UTC() },
	}
}

func (d *Detector) now() time.Time {
	if d.Now == nil {
		return time.Now().UTC()
	}
	return d.Now().UTC()
}

func (d *Detector) log() *slog.Logger {
	if d.Logger == nil {
		return slog.Default()
	}
	return d.Logger
}

func (d *Detector) days() int {
	if d.Days <= 0 {
		return defaultDays
	}
	return d.Days
}

func (d *Detector) baseline() int {
	if d.Baseline <= 0 {
		return defaultBaseline
	}
	return d.Baseline
}

// RunLoop detects at startup and then once an hour until ctx is cancelled. It
// blocks, so `loot serve` starts it in a goroutine.
func (d *Detector) RunLoop(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		found, err := d.Run(ctx)
		if err != nil {
			d.log().Error("mystery detection failed", "error", err)
		} else if len(found) > 0 {
			d.log().Info("mysteries found", "count", len(found))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Run examines the recent past and stores whatever it finds that is not
// already on the board. Only *completed* days are examined: today is still
// accumulating, and a half-finished day looks exactly like a collapse.
func (d *Detector) Run(ctx context.Context) ([]core.Mystery, error) {
	now := d.now()
	lastDay := core.DayOf(now.AddDate(0, 0, -1))
	detectFrom := core.DayOf(now.AddDate(0, 0, -d.days()))
	from := core.DayOf(now.AddDate(0, 0, -(d.days() + d.baseline() + 1)))

	var found []core.Mystery
	// flagged holds (source|app|day) triples already spoken for in this pass.
	flagged := map[string]bool{}

	for _, metric := range scanOrder {
		series, err := d.Store.DailySeries(ctx, metric, from, lastDay)
		if err != nil {
			return found, err
		}
		for _, key := range sortedKeys(series) {
			points := densify(series[key], lastDay)
			found = append(found, d.scan(metric, key, points, detectFrom, flagged)...)
		}
	}

	cluster, err := d.scanSettlements(ctx, from, lastDay, detectFrom)
	if err != nil {
		return found, err
	}
	found = append(found, cluster...)

	quiet, err := d.scanSilence(ctx, from, lastDay)
	if err != nil {
		return found, err
	}
	found = append(found, quiet...)

	created := make([]core.Mystery, 0, len(found))
	for _, m := range found {
		isNew, err := d.Store.InsertMystery(ctx, m)
		if err != nil {
			return created, err
		}
		if isNew {
			created = append(created, m)
			d.log().Debug("mystery", "kind", m.Kind, "title", m.Title, "day", m.Day)
		}
	}
	if len(created) > 0 && d.Bus != nil {
		d.Bus.Publish(bus.Message{Type: "mysteries"})
	}
	return created, nil
}

// reading is what one day of a series looks like against its own baseline.
type reading struct {
	kind   string
	med    float64
	spread float64
	z      float64
}

// scan walks one dense series and flags whatever stands out, at most one
// mystery per (source, app, day).
//
// A run of bad days is one story, not seven. A week where refunds stayed high
// should ask its question once, so a day is only flagged when the day before
// it was *not* flagged the same way — and that is decided from the data, not
// from where the detection window happens to start, so tomorrow's run reaches
// exactly the same conclusion about the same week.
func (d *Detector) scan(metric store.SeriesMetric, key store.SeriesKey, points []point, detectFrom string, flagged map[string]bool) []core.Mystery {
	readings := make([]reading, len(points))
	for i := range points {
		readings[i] = d.read(metric, points, i)
	}

	var out []core.Mystery
	for i, p := range points {
		if p.day < detectFrom || readings[i].kind == "" {
			continue
		}
		if i > 0 && readings[i-1].kind == readings[i].kind {
			continue // the same story, one day later
		}
		mark := key.Source + "|" + key.App + "|" + p.day
		if flagged[mark] {
			continue
		}
		flagged[mark] = true
		r := readings[i]
		out = append(out, *d.mystery(r.kind, metric, key, points, i, r.med, r.spread, r.z))
	}
	return out
}

// read measures one day against the trailing baseline and decides which kind
// of mystery, if any, it is. An empty kind means "this day fits".
func (d *Detector) read(metric store.SeriesMetric, points []point, i int) reading {
	start := i - d.baseline()
	if start < 0 {
		start = 0
	}
	base := values(points[start:i])
	if len(base) < minBaselineDays {
		return reading{}
	}

	med := median(base)
	spread := mad(base, med)
	value := points[i].value
	r := reading{med: med, spread: spread, z: modifiedZ(value, med, spread, base)}
	threshold := minDelta[metric]
	delta := value - med

	switch {
	case metric == store.SeriesRefunds:
		// Refunds get their own rule: a multiple of the usual, with a floor,
		// because "three times as many" only matters once there are a few.
		if value >= minRefunds && value >= refundMultiple*med {
			r.kind = core.MysteryRefundSpike
		}
	case r.z >= ZThreshold && delta >= threshold:
		r.kind = core.MysterySpike
	case r.z <= -ZThreshold && -delta >= threshold:
		r.kind = core.MysteryDip
	case isRecord(points[start:i], value, threshold):
		r.kind = core.MysteryRecord
	}
	return r
}

// isRecord reports whether value beats every day of the baseline by enough to
// be worth remarking on. It needs real history behind it: the first good day
// of a new app is not a record, it is a beginning.
func isRecord(base []point, value, threshold float64) bool {
	if len(base) < minRecordHistory || value <= 0 {
		return false
	}
	best := 0.0
	for _, p := range base {
		if p.value > best {
			best = p.value
		}
	}
	return best > 0 && value >= best*recordMultiple && value-best >= threshold
}

// mystery builds one flagged day, with the trailing series for its sparkline.
func (d *Detector) mystery(kind string, metric store.SeriesMetric, key store.SeriesKey, points []point, i int, med, spread, z float64) *core.Mystery {
	from := i - (seriesPoints - 1)
	if from < 0 {
		from = 0
	}
	window := points[from : i+1]

	series := make([]core.MysteryPoint, 0, len(window))
	for _, p := range window {
		series = append(series, core.MysteryPoint{Day: p.day, Value: p.value})
	}

	observed := points[i].value
	ratio := 0.0
	if med != 0 {
		ratio = roundN(observed/med, 3)
	}
	unit := "count"
	if metric == store.SeriesRevenue {
		unit = "money"
	}

	detail := core.MysteryDetail{
		Series:    series,
		Baseline:  roundN(med, 2),
		Deviation: roundN(spread, 2),
		Ratio:     ratio,
		Unit:      unit,
		Why:       d.why(kind, metric, observed, med, z),
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		d.log().Error("mystery detail encode failed", "error", err)
		raw = nil
	}

	m := core.Mystery{
		ID:        core.NewID(),
		Kind:      kind,
		Source:    key.Source,
		App:       key.App,
		Metric:    string(metric),
		Day:       points[i].day,
		Observed:  roundN(observed, 2),
		Expected:  roundN(med, 2),
		Z:         roundN(z, 2),
		Title:     d.title(kind, metric, key, points[i], med),
		Detail:    raw,
		Status:    core.MysteryOpen,
		CreatedAt: d.now(),
	}
	m.DedupeKey = mysteryKey(kind, key.Source, key.App, string(metric), m.Day)
	return &m
}

// scanSettlements flags a day that founded several countries at once. Three
// first-ever countries in one day is either a feature launch, a review, or
// somebody's newsletter — and it is worth knowing which.
func (d *Detector) scanSettlements(ctx context.Context, from, to, detectFrom string) ([]core.Mystery, error) {
	byDay, err := d.Store.SettlementsByDay(ctx, from, to)
	if err != nil {
		return nil, err
	}
	points := densify(byDay, to)

	var out []core.Mystery
	for _, p := range points {
		if p.day < detectFrom || p.value < clusterSize {
			continue
		}
		series := make([]core.MysteryPoint, 0, len(points))
		for _, q := range points {
			series = append(series, core.MysteryPoint{Day: q.day, Value: q.value})
		}
		if len(series) > seriesPoints {
			series = series[len(series)-seriesPoints:]
		}
		detail, err := json.Marshal(core.MysteryDetail{
			Series: series,
			Unit:   "count",
			Why:    fmt.Sprintf("%s countries bought for the first time on the same day", core.FormatCount(p.value)),
		})
		if err != nil {
			return out, fmt.Errorf("cluster detail: %w", err)
		}

		m := core.Mystery{
			ID:       core.NewID(),
			Kind:     core.MysteryNewCountryCluster,
			Metric:   "settlements",
			Day:      p.day,
			Observed: p.value,
			Title: fmt.Sprintf("%s new countries settled on %s — what happened?",
				core.FormatCount(p.value), humanDay(p.day)),
			Detail:    detail,
			Status:    core.MysteryOpen,
			CreatedAt: d.now(),
		}
		m.DedupeKey = mysteryKey(m.Kind, "", "", m.Metric, m.Day)
		out = append(out, m)
	}
	return out, nil
}

// settlementLagDays is how many days behind the calendar each source's newest
// event day normally sits, because that is how long its store takes to settle
// a day and publish it.
//
// These are not guesses: they are the settlement horizons the sources
// themselves poll on. The Microsoft Store reads days three behind (Partner
// Center revises acquisitions for a day or two), Google Play summarizes a day
// once the report has stopped moving under it, and App Store Connect publishes
// yesterday's report during the following morning. Without this, every one of
// them looked "quiet" against a flat two-day rule on the day it was working
// perfectly — a false alarm every single day, which is the fastest way to
// teach somebody to ignore the board.
//
// A source not listed here reports in realtime and has no lag.
var settlementLagDays = map[string]int{
	"microsoftstore": 3,
	"googleplay":     2,
	"appstore":       1,
	// Play's vitals API is the slowest of the lot: its freshness window is a
	// couple of days, so the newest day it will ever hand back sits about
	// three behind the calendar. Without this the crash source that keeps the
	// boss board honest was itself reported broken every single morning.
	"playvitals": 3,
	// The Snap Store's metrics API publishes a day once it has stopped moving,
	// and Flathub's stats are yesterday's, written overnight.
	"snapcraft": 2,
	"flathub":   1,
}

// pushOnlySources never poll: they emit when something happens and say nothing
// otherwise. Silence from one of them is not evidence of anything — a week
// with no Sentry issues and a week with a revoked Sentry token look exactly
// alike from here — so they are skipped rather than measured against a
// threshold that could only ever cry wolf. GitHub is deliberately absent: it
// polls on a timer as well as receiving pushes, so its silence does mean
// something.
var pushOnlySources = map[string]bool{
	"sentry":     true,
	"crash":      true,
	"webhook":    true,
	"revenuecat": true,
}

// silenceThreshold is how many completed days a source has to miss before the
// gap is worth a question: the flat minimum, plus whatever its store's own
// settlement lag makes normal.
func silenceThreshold(source string) int {
	return silenceDays + settlementLagDays[source]
}

// silenceScope is the (source, app) pair a silence mystery is asked about.
// Silence is measured per source today — an app cannot lose its credentials
// on its own — but the key carries the app so the dedupe generalizes if that
// ever changes.
func silenceScope(source, app string) string { return source + "|" + app }

// scanSilence flags a source that reported reliably and then stopped. This is
// the one mystery with a usual answer: an expired key, a rotated credential, a
// bucket permission that lapsed. Loot cannot tell you which, but it can notice
// that App Store Connect has not said anything since Tuesday.
//
// Two things keep it from crying wolf. Each source is measured against its own
// settlement lag rather than a flat two days, so a store that is three days
// behind by design is not reported broken every morning. And at most one
// silence mystery is open per (source, app) at a time: a source that has been
// quiet for a week is one question, not seven. When it starts reporting again
// the open question answers itself and is dismissed with a note.
func (d *Detector) scanSilence(ctx context.Context, from, to string) ([]core.Mystery, error) {
	bySource, err := d.Store.EventsBySourceDay(ctx, from, to)
	if err != nil {
		return nil, err
	}

	openSilence, err := d.Store.OpenMysteriesOfKind(ctx, core.MysterySilence)
	if err != nil {
		return nil, err
	}
	alreadyAsked := make(map[string]core.Mystery, len(openSilence))
	for _, m := range openSilence {
		alreadyAsked[silenceScope(m.Source, m.App)] = m
	}

	sources := make([]string, 0, len(bySource))
	for source := range bySource {
		sources = append(sources, source)
	}
	sort.Strings(sources)

	end, err := time.Parse(core.DayLayout, to)
	if err != nil {
		return nil, fmt.Errorf("silence window: %w", err)
	}

	var out []core.Mystery
	for _, source := range sources {
		if pushOnlySources[source] {
			// Nothing to ask about, and nothing to resume: a push-only source
			// has no schedule to fall behind.
			continue
		}
		days := bySource[source]
		last := ""
		for day := range days {
			if day > last {
				last = day
			}
		}
		if last == "" {
			continue
		}
		lastTime, err := time.Parse(core.DayLayout, last)
		if err != nil {
			continue
		}
		scope := silenceScope(source, "")
		missing := int(end.Sub(lastTime).Hours() / 24)
		threshold := silenceThreshold(source)
		if missing < threshold {
			// Reporting normally. If a question was open about this source,
			// it has just been answered.
			d.resumeSilence(ctx, alreadyAsked, scope)
			continue
		}
		// Only a source that was *reliable* can go quiet: one that reports
		// twice a month is not broken, it is just occasional.
		reported := 0
		for i := 0; i < 7; i++ {
			if days[core.DayOf(lastTime.AddDate(0, 0, -i))] > 0 {
				reported++
			}
		}
		if reported < silenceDensity {
			continue
		}
		if _, asked := alreadyAsked[scope]; asked {
			// Still quiet, and already on the board. One question is enough.
			continue
		}

		firstSilent := core.DayOf(lastTime.AddDate(0, 0, 1))
		series := make([]core.MysteryPoint, 0, seriesPoints)
		for i := seriesPoints - 1; i >= 0; i-- {
			day := core.DayOf(end.AddDate(0, 0, -i))
			series = append(series, core.MysteryPoint{Day: day, Value: float64(days[day])})
		}
		why := fmt.Sprintf("%d completed days with no rows at all, after reporting on %d of the previous 7",
			missing, reported)
		if lag := settlementLagDays[source]; lag > 0 {
			why += fmt.Sprintf(" — and that is already allowing for this store's %d day settlement lag", lag)
		}
		detail, err := json.Marshal(core.MysteryDetail{
			Series: series,
			Unit:   "count",
			Why:    why,
		})
		if err != nil {
			return out, fmt.Errorf("silence detail: %w", err)
		}

		m := core.Mystery{
			ID:       core.NewID(),
			Kind:     core.MysterySilence,
			Source:   source,
			Metric:   "events",
			Day:      firstSilent,
			Observed: 0,
			Expected: median(values(densify(toFloat(days), last))),
			Title: fmt.Sprintf("%s has gone quiet since %s",
				SourceLabel(source), humanDay(firstSilent)),
			Detail:    detail,
			Status:    core.MysteryOpen,
			CreatedAt: d.now(),
		}
		m.DedupeKey = mysteryKey(m.Kind, source, "", m.Metric, m.Day)
		out = append(out, m)
	}
	return out, nil
}

// resumeSilence closes an open silence question about a source that has
// started reporting again. It is dismissed rather than solved: nobody wrote
// anything down, so there is nothing to pay a drop for, and a board full of
// "it came back" entries is not a lab notebook.
func (d *Detector) resumeSilence(ctx context.Context, asked map[string]core.Mystery, scope string) {
	m, ok := asked[scope]
	if !ok {
		return
	}
	delete(asked, scope)
	changed, err := d.Store.ResolveMystery(ctx, m.ID, core.MysteryDismissed, resumedNote, d.now())
	if err != nil {
		d.log().Error("could not close a resumed silence mystery", "error", err, "mystery", m.ID)
		return
	}
	if changed {
		d.log().Info("source resumed; silence question closed", "source", m.Source, "app", m.App)
		if d.Bus != nil {
			d.Bus.Publish(bus.Message{Type: "mysteries"})
		}
	}
}

// mysteryKey is the idempotence key: one mystery per kind, source, app, metric
// and day, no matter how often the detector runs.
func mysteryKey(kind, source, app, metric, day string) string {
	return strings.Join([]string{kind, source, app, metric, day}, ":")
}

// --------------------------------------------------------------------- prose

// title writes the headline. It is phrased as a question wherever a question
// is what it really is.
func (d *Detector) title(kind string, metric store.SeriesMetric, key store.SeriesKey, p point, med float64) string {
	source := SourceLabel(key.Source)
	label := seriesLabel(metric)
	day := humanDay(p.day)

	switch kind {
	case core.MysterySpike:
		return fmt.Sprintf("%s %s %s on %s — why?", source, label, phrase(p.value, med), day)
	case core.MysteryDip:
		return fmt.Sprintf("%s %s %s on %s", source, label, phrase(p.value, med), day)
	case core.MysteryRefundSpike:
		return fmt.Sprintf("%s refunds on %s on %s — usually %s",
			core.FormatCount(p.value), source, day, core.FormatCount(med))
	case core.MysteryRecord:
		return fmt.Sprintf("Record day: %s %s on %s, %s",
			d.formatValue(metric, p.value), label, source, day)
	}
	return fmt.Sprintf("%s %s on %s", source, label, day)
}

// why is the one-line explanation of what tripped the detector, kept with the
// mystery so the card can say *why it is asking* and not only what it saw.
func (d *Detector) why(kind string, metric store.SeriesMetric, observed, med, z float64) string {
	switch kind {
	case core.MysteryRefundSpike:
		return fmt.Sprintf("%s refunds against a usual %s",
			core.FormatCount(observed), core.FormatCount(med))
	case core.MysteryRecord:
		return fmt.Sprintf("higher than any of the previous %d days", d.baseline())
	default:
		return fmt.Sprintf("%s against a 28-day median of %s (z %.1f)",
			d.formatValue(metric, observed), d.formatValue(metric, med), z)
	}
}

func (d *Detector) formatValue(metric store.SeriesMetric, v float64) string {
	if metric == store.SeriesRevenue {
		return core.FormatMoney(v, d.DisplayCurrency)
	}
	return core.FormatCount(v)
}

// phrase describes a move in words a person would use: tripled, doubled,
// jumped 60%, dropped 70%.
func phrase(observed, expected float64) string {
	if expected <= 0 {
		if observed > 0 {
			return "appeared out of nothing"
		}
		return "changed"
	}
	ratio := observed / expected
	switch {
	case ratio >= 4:
		return "quadrupled"
	case ratio >= 3:
		return "tripled"
	case ratio >= 2:
		return "doubled"
	case ratio > 1:
		return fmt.Sprintf("jumped %.0f%%", (ratio-1)*100)
	case ratio == 1:
		return "held still"
	default:
		return fmt.Sprintf("dropped %.0f%%", (1-ratio)*100)
	}
}

// SourceLabel is a source's display name, falling back to its own id.
//
// The names live in core, in the one map the rules templates read as well, so
// a source added there is spelled the same in a mystery title as it is in the
// drop beside it — a second private list here drifted, and left the crash
// sources with no name at all.
func SourceLabel(source string) string {
	if source == "" {
		return "Loot"
	}
	return core.SourceDisplayName(source)
}

// seriesLabel is how a series reads inside a sentence.
func seriesLabel(metric store.SeriesMetric) string {
	switch metric {
	case store.SeriesInstalls:
		return "installs"
	case store.SeriesUnits:
		return "units sold"
	case store.SeriesRevenue:
		return "revenue"
	case store.SeriesRefunds:
		return "refunds"
	case store.SeriesCancellations:
		return "cancellations"
	}
	return string(metric)
}

// humanDay renders a day as "Tue Aug 12".
func humanDay(day string) string {
	t, err := time.Parse(core.DayLayout, day)
	if err != nil {
		return day
	}
	return t.Format("Mon Jan 2")
}

// ------------------------------------------------------------------ series

// point is one day of a dense series.
type point struct {
	day   string
	value float64
}

// densify turns a sparse day → value map into a dense, ordered series running
// from its earliest day through `to`. Zero-filling is the whole point: a day
// with no rows is a day on which nothing happened, and "nothing happened" is
// exactly the observation a dip and a silence are made of.
func densify(values map[string]float64, to string) []point {
	if len(values) == 0 {
		return nil
	}
	first := ""
	for day := range values {
		if first == "" || day < first {
			first = day
		}
	}
	start, err := time.Parse(core.DayLayout, first)
	if err != nil {
		return nil
	}
	end, err := time.Parse(core.DayLayout, to)
	if err != nil {
		return nil
	}

	var out []point
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		key := core.DayOf(day)
		out = append(out, point{day: key, value: values[key]})
	}
	return out
}

// toFloat widens an int-valued day map so it can share the series helpers.
func toFloat(in map[string]int) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = float64(v)
	}
	return out
}

func values(points []point) []float64 {
	out := make([]float64, 0, len(points))
	for _, p := range points {
		out = append(out, p.value)
	}
	return out
}

func sortedKeys(series map[store.SeriesKey]map[string]float64) []store.SeriesKey {
	out := make([]store.SeriesKey, 0, len(series))
	for key := range series {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].App < out[j].App
	})
	return out
}

// ------------------------------------------------------------------ statistics

// median returns the middle value of a copy of vals, or 0 for an empty slice.
func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// mad is the median absolute deviation around med: the robust spread that a
// single enormous day cannot inflate.
func mad(vals []float64, med float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	dev := make([]float64, 0, len(vals))
	for _, v := range vals {
		dev = append(dev, math.Abs(v-med))
	}
	return median(dev)
}

// meanAD is the mean absolute deviation, the fallback spread for a series so
// steady that more than half its days sit on the median exactly.
func meanAD(vals []float64, med float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += math.Abs(v - med)
	}
	return sum / float64(len(vals))
}

// modifiedZ scores x against a robust baseline. With no spread at all it
// returns ±zUnbounded for anything off the median, which the absolute-delta
// threshold then decides whether to take seriously.
func modifiedZ(x, med, spread float64, base []float64) float64 {
	if spread <= 0 {
		// More than half the baseline sits exactly on the median (a series of
		// steady zeros, say). The mean absolute deviation still has something
		// to say, converted into the units a MAD would have been in: for a
		// normal baseline sigma ≈ 1.2533 × meanAD and MAD ≈ 0.6745 × sigma.
		if alt := meanAD(base, med) * madToStd * zScale; alt > 0 {
			return zScale * (x - med) / alt
		}
		switch {
		case x > med:
			return zUnbounded
		case x < med:
			return -zUnbounded
		default:
			return 0
		}
	}
	return zScale * (x - med) / spread
}

func roundN(v float64, places int) float64 {
	pow := math.Pow(10, float64(places))
	return math.Round(v*pow) / pow
}
