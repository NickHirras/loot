package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/pipeline"
	"github.com/nickhirras/loot/internal/sources/appstore"
	"github.com/nickhirras/loot/internal/sources/googleplay"
)

// This file invents the past: for each day of the window it decides what each
// app sold, in which countries, and hands the result to the real pipeline as
// events shaped exactly like the ones App Store Connect, Google Play, Flathub
// and RevenueCat produce. Nothing here writes to the database directly — the
// drops, the settlements, the chests and the XP all come out of the same code
// path a real install uses.

// grossNote mirrors the caveat the real ledger sources attach to a day's
// figures, so demo payloads read like the real thing in the drop inspector.
const grossNote = "estimated developer proceeds; the ledger is the store's own report"

// dayPlan is one generated day: which day it is, where it sits in the window,
// and the random number generator that belongs to it. The generator is keyed
// by (seed, day) rather than carried across days, so the same day always
// produces the same world whether it was seeded in a batch of 120 or caught up
// one day at a time the next morning.
type dayPlan struct {
	date  string
	day   time.Time
	index int
	span  int
	rng   *rand.Rand
}

// progress is how far through the window this day sits, 0 at the start and 1
// at the end.
func (p dayPlan) progress() float64 {
	if p.span <= 1 {
		return 1
	}
	return float64(p.index) / float64(p.span-1)
}

// weekend reports whether this is a Saturday or Sunday.
func (p dayPlan) weekend() bool {
	wd := p.day.Weekday()
	return wd == time.Saturday || wd == time.Sunday
}

// badWeek is the week where everything went wrong: a bad release, a payment
// provider outage, a review pile-on. Sales dip, refunds multiply and
// subscribers churn.
//
// It sits near the end of the window on purpose: recent enough for the mystery
// detector's fortnight to see it (a demo should open with a real question in
// its casebook, not only a good one), but clear of the record day a few days
// later, so the last two weeks read as a slump, a recovery, and then the best
// day the business ever had.
func (p dayPlan) badWeek() bool {
	start := int(0.88 * float64(p.span))
	return p.index >= start && p.index < start+7
}

// bestDay is the record breaker near the end of the window: the day a big
// newsletter linked to the apps. It is deliberately recent, so a new demo
// opens on a feed with a fresh epic in it.
func (p dayPlan) bestDay() bool { return p.span > 10 && p.index == p.span-5 }

// storefronts returns the countries that have discovered the apps by this
// day. Countries unlock over the window, which is what makes settlements
// appear one at a time across the history rather than all at once on day one.
func (p dayPlan) storefronts() []storefront {
	out := make([]storefront, 0, len(storefronts))
	prog := p.progress()
	for _, s := range storefronts {
		if s.Unlock <= prog {
			out = append(out, s)
		}
	}
	return out
}

// scale is the multiplier applied to an app's baseline for this day: growth
// over the window, weekly seasonality, a launch spike, the bad week, the
// record day, and a little noise on top so no two days are alike.
func (p dayPlan) scale(a app) float64 {
	f := 1 + (a.Growth-1)*p.progress()

	if p.weekend() {
		f *= a.Weekend
	} else if p.day.Weekday() == time.Monday {
		f *= 1.06
	}

	if a.LaunchAt > 0 {
		if delta := p.index - int(a.LaunchAt*float64(p.span)); delta >= 0 && delta < 14 {
			f *= 1 + 2.4*math.Exp(-float64(delta)/4.5)
		}
	}

	if p.badWeek() {
		f *= 0.72
	}

	noise := 1 + 0.17*p.rng.NormFloat64()
	if p.bestDay() {
		// The record has to actually be a record, so the day that is meant to
		// be the best one is never allowed to roll a bad die.
		f *= 3.2
		noise = math.Max(noise, 1)
	}
	return f * math.Max(0.55, math.Min(1.5, noise))
}

// refundRate is the share of a day's units that come back. The bad week is
// six times worse, which is what makes its chests full of cursed drops.
func (p dayPlan) refundRate() float64 {
	if p.badWeek() {
		return 0.16
	}
	return 0.025
}

// count turns a fractional expectation into a whole number of things, keeping
// the fraction as a probability so a rate of 0.4 really does mean "four days
// in ten".
func (p dayPlan) count(expected float64) int {
	if expected <= 0 {
		return 0
	}
	n := int(expected)
	if p.rng.Float64() < expected-float64(n) {
		n++
	}
	return n
}

// at returns a timestamp inside the generated day, weighted towards the
// afternoon and evening — the hours people actually buy things.
func (p dayPlan) at() time.Time {
	return p.day.Add(time.Duration(activeHour(p.rng))*time.Hour +
		time.Duration(p.rng.IntN(60))*time.Minute +
		time.Duration(p.rng.IntN(60))*time.Second)
}

// activeHour samples an hour of the day from a curve that peaks over the
// afternoon: the apps sell across a dozen timezones, so the aggregate never
// really sleeps, it just goes quiet.
func activeHour(rng *rand.Rand) int {
	weights := [24]float64{
		3, 2, 2, 2, 2, 3, 4, 6, 8, 10, 11, 12,
		12, 13, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4,
	}
	total := 0.0
	for _, w := range weights {
		total += w
	}
	r := rng.Float64() * total
	for h, w := range weights {
		if r -= w; r < 0 {
			return h
		}
	}
	return 12
}

// saleLine is one row of a store's report: some number of units of one
// product, bought in one country, for a signed amount of money. A refund is a
// line with negative units and negative proceeds, exactly as the real reports
// have it.
type saleLine struct {
	store    storefront
	prod     product
	units    int
	proceeds float64
}

// refund reports whether this line gives money back.
func (l saleLine) refund() bool { return l.units < 0 }

// kind is the Loot event kind for this line.
func (l saleLine) kind() string {
	if l.refund() {
		return "refund"
	}
	return l.prod.Kind
}

// buildLines invents one store's day for one app: `base` units of business,
// spread over the countries unlocked so far and the app's product mix, plus
// the refunds that came back the same day.
func (p dayPlan) buildLines(a app, base float64) []saleLine {
	units := p.count(base * p.scale(a))
	open := p.storefronts()

	var (
		order []string
		lines = map[string]*saleLine{}
	)
	add := func(s storefront, pr product, n int) {
		key := s.Code + "|" + pr.SKU
		if n < 0 {
			key += "|refund"
		}
		line, ok := lines[key]
		if !ok {
			line = &saleLine{store: s, prod: pr}
			lines[key] = line
			order = append(order, key)
		}
		line.units += n
		line.proceeds = round2(float64(line.units) * pr.PriceUSD * pr.Share * s.Rate)
	}

	for i := 0; i < units; i++ {
		add(pickStorefront(p.rng, open), pickProduct(p.rng, a.Products), 1)
	}
	for i := 0; i < p.count(float64(units)*p.refundRate()); i++ {
		pr := pickProduct(p.rng, a.Products)
		if pr.PriceUSD == 0 {
			// Nobody asks for their money back on a free download.
			continue
		}
		add(pickStorefront(p.rng, open), pr, -1)
	}

	sort.Strings(order)
	out := make([]saleLine, 0, len(order))
	for _, key := range order {
		if lines[key].units == 0 {
			continue
		}
		out = append(out, *lines[key])
	}
	return out
}

// pickStorefront draws one country from the unlocked set, by weight.
func pickStorefront(rng *rand.Rand, open []storefront) storefront {
	total := 0.0
	for _, s := range open {
		total += s.Weight
	}
	r := rng.Float64() * total
	for _, s := range open {
		if r -= s.Weight; r < 0 {
			return s
		}
	}
	return open[len(open)-1]
}

// pickProduct draws one product from an app's catalogue, by weight.
func pickProduct(rng *rand.Rand, products []product) product {
	total := 0.0
	for _, pr := range products {
		total += pr.Weight
	}
	r := rng.Float64() * total
	for _, pr := range products {
		if r -= pr.Weight; r < 0 {
			return pr
		}
	}
	return products[len(products)-1]
}

// generateDay ingests everything that happened on one day, app by app and
// source by source, in the order a real day would have produced it.
func (d *Demo) generateDay(ctx context.Context, pipe *pipeline.Pipeline, p dayPlan) error {
	for _, a := range apps {
		if a.AppStore {
			if err := d.appStoreDay(ctx, pipe, p, a); err != nil {
				return err
			}
		}
		if a.Play {
			if err := d.playDay(ctx, pipe, p, a); err != nil {
				return err
			}
		}
		if a.Flathub {
			if err := d.flathubDay(ctx, pipe, p, a); err != nil {
				return err
			}
		}
		if a.RevenueCat {
			if err := d.revenueCatDay(ctx, pipe, p, a); err != nil {
				return err
			}
		}
		if a.CrashBase > 0 {
			if err := d.crashDay(ctx, pipe, p, a); err != nil {
				return err
			}
		}
	}
	return nil
}

// appStoreDay emits one app's App Store Connect day: a silent ledger row per
// (country, product), the chest-bound sales_day summary, and the daily
// subscriber snapshot.
func (d *Demo) appStoreDay(ctx context.Context, pipe *pipeline.Pipeline, p dayPlan, a app) error {
	lines := p.buildLines(a, a.AppStoreBase)
	seq := map[string]int{}
	totals := newDayTotals()

	for _, l := range lines {
		row := appstore.SalesRow{
			Provider:         "APPLE",
			ProviderCountry:  "US",
			SKU:              l.prod.SKU,
			Developer:        "Lumen Labs",
			Title:            a.Name,
			Version:          "3.2",
			ProductType:      l.prod.ProductType,
			Units:            l.units,
			ProceedsPerUnit:  round2(l.prod.PriceUSD * l.prod.Share * l.store.Rate),
			BeginDate:        p.date,
			EndDate:          p.date,
			CustomerCurrency: l.store.Currency,
			Country:          l.store.Code,
			ProceedsCurrency: l.store.Currency,
			AppleID:          a.AppleID,
			CustomerPrice:    round2(l.prod.PriceUSD * l.store.Rate),
			Period:           l.prod.Period,
		}
		payload, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("demo: encode appstore row: %w", err)
		}

		// The real source's key, down to the trailing occurrence counter that
		// separates two otherwise identical rows of the same report.
		key := strings.Join([]string{
			"asc:sales", p.date, a.AppleID, l.prod.SKU, l.store.Code, l.prod.ProductType, l.store.Currency,
		}, ":")
		i := seq[key]
		seq[key]++

		totals.add(l)
		if err := d.ingest(ctx, pipe, core.Event{
			ID:         core.NewIDAt(p.day),
			Source:     appstore.Name,
			Kind:       l.kind(),
			App:        a.Name,
			OccurredAt: p.day,
			ObservedAt: p.day,
			Day:        p.date,
			Country:    l.store.Code,
			Amount:     l.proceeds,
			Currency:   l.store.Currency,
			Quantity:   l.units,
			DedupeKey:  fmt.Sprintf("%s:%d", key, i),
			IsLedger:   true,
			Silent:     true,
			Payload:    payload,
		}); err != nil {
			return err
		}
	}

	if err := d.salesDay(ctx, pipe, p, a, appstore.Name, totals,
		fmt.Sprintf("%s:%s:%s:%s", appstore.Name, appstore.KindSalesDay, a.AppleID, p.date)); err != nil {
		return err
	}

	return d.subscriptionSnapshot(ctx, pipe, p, a, totals)
}

// playDay emits one app's Google Play day: an order line per (country,
// product), the sales_day summary, and the install reports.
func (d *Demo) playDay(ctx context.Context, pipe *pipeline.Pipeline, p dayPlan, a app) error {
	lines := p.buildLines(a, a.PlayBase)
	totals := newDayTotals()

	for i, l := range lines {
		status := "Charged"
		if l.refund() {
			status = "Refund"
		}
		order := fmt.Sprintf("GPA.%04d-%04d-%04d-%05d",
			p.rng.IntN(10000), p.rng.IntN(10000), p.rng.IntN(10000), p.rng.IntN(100000))
		occurred := p.at()

		payload, err := json.Marshal(map[string]any{
			"order_number":     order,
			"package":          a.Package,
			"product_title":    a.Name,
			"product_type":     l.prod.PlayType,
			"sku":              l.prod.SKU,
			"financial_status": status,
			"device_model":     playDevices[(p.index+i)%len(playDevices)],
			"currency":         l.store.Currency,
			"item_price":       round2(l.prod.PriceUSD * l.store.Rate),
			"taxes_collected":  0,
			"charged_amount":   l.proceeds,
			"country":          l.store.Code,
			"day":              p.date,
			"gross":            true,
			"note":             grossNote,
		})
		if err != nil {
			return fmt.Errorf("demo: encode play row: %w", err)
		}

		totals.add(l)
		if err := d.ingest(ctx, pipe, core.Event{
			ID:         core.NewIDAt(occurred),
			Source:     googleplay.Name,
			Kind:       l.kind(),
			App:        a.Name,
			OccurredAt: occurred,
			ObservedAt: p.day,
			Day:        p.date,
			Country:    l.store.Code,
			Amount:     l.proceeds,
			Currency:   l.store.Currency,
			Quantity:   l.units,
			DedupeKey: fmt.Sprintf("play:sale:%s:%s:%s",
				order, l.prod.SKU, strings.ToLower(status)),
			IsLedger: true,
			Silent:   true,
			Payload:  payload,
		}); err != nil {
			return err
		}
	}

	if err := d.salesDay(ctx, pipe, p, a, googleplay.Name, totals,
		fmt.Sprintf("play:sales_day:%s:%s", a.Package, p.date)); err != nil {
		return err
	}

	return d.playInstalls(ctx, pipe, p, a)
}

// playDevices is a handful of plausible device models for order payloads.
var playDevices = []string{"Pixel 8", "SM-S921B", "Pixel 7a", "SM-A546B", "moto g84", "OnePlus 12"}

// dayTotals accumulates a store's day while its rows are generated, so the
// summary event can report exactly what the rows said.
type dayTotals struct {
	units      int
	refunds    int
	byCurrency map[string]float64
	bySKU      map[string]map[string]core.SKUTotals
	countries  map[string]int
	rows       int
	iapUnits   int
	subUnits   int
	subCountry map[string]int
}

func newDayTotals() *dayTotals {
	return &dayTotals{
		byCurrency: map[string]float64{},
		bySKU:      map[string]map[string]core.SKUTotals{},
		countries:  map[string]int{},
		subCountry: map[string]int{},
	}
}

func (t *dayTotals) add(l saleLine) {
	t.rows++
	if l.refund() {
		t.refunds += -l.units
	} else {
		switch l.prod.Kind {
		case "download":
			// A free download is not a sale; it still founds settlements.
		default:
			t.units += l.units
			t.countries[l.store.Code] += l.units
			if l.prod.Kind == "iap" {
				t.iapUnits += l.units
			}
			if l.prod.Kind == "subscription" {
				t.subUnits += l.units
				t.subCountry[l.store.Code] += l.units
			}
		}
	}
	if l.proceeds != 0 {
		cur := l.store.Currency
		t.byCurrency[cur] = round2(t.byCurrency[cur] + l.proceeds)
		if t.bySKU[cur] == nil {
			t.bySKU[cur] = map[string]core.SKUTotals{}
		}
		s := t.bySKU[cur][l.prod.SKU]
		if !l.refund() {
			s.Units += l.units
		}
		s.Proceeds = round2(s.Proceeds + l.proceeds)
		t.bySKU[cur][l.prod.SKU] = s
	}
}

// dominant picks the currency carrying the most money, which is the currency
// the day's headline reads in. Ties break alphabetically so a re-run is
// identical.
func (t *dayTotals) dominant() (string, float64) {
	var (
		best    string
		bestAbs float64
	)
	currencies := make([]string, 0, len(t.byCurrency))
	for cur := range t.byCurrency {
		currencies = append(currencies, cur)
	}
	sort.Strings(currencies)
	for _, cur := range currencies {
		if abs := math.Abs(t.byCurrency[cur]); abs > bestAbs {
			best, bestAbs = cur, abs
		}
	}
	return best, round2(t.byCurrency[best])
}

// topCountry is the country that bought the most, for the summary payload.
func (t *dayTotals) topCountry() string {
	best, bestN := "", 0
	keys := make([]string, 0, len(t.countries))
	for c := range t.countries {
		keys = append(keys, c)
	}
	sort.Strings(keys)
	for _, c := range keys {
		if t.countries[c] > bestN {
			best, bestN = c, t.countries[c]
		}
	}
	return best
}

// salesDay emits the one non-silent ledger event of an app's day: the summary
// whose drop waits in that day's chest.
func (d *Demo) salesDay(ctx context.Context, pipe *pipeline.Pipeline, p dayPlan, a app, source string, t *dayTotals, dedupe string) error {
	if t.units == 0 && t.refunds == 0 {
		return nil
	}
	currency, proceeds := t.dominant()

	summary := core.SalesDaySummary{
		Units:      t.units,
		Refunds:    t.refunds,
		Proceeds:   proceeds,
		Currency:   currency,
		Countries:  len(t.countries),
		TopCountry: t.topCountry(),
		BySKU:      t.bySKU[currency],
	}

	var (
		payload []byte
		err     error
	)
	if source == googleplay.Name {
		byCurrency := make(map[string]float64, len(t.byCurrency))
		for cur, total := range t.byCurrency {
			byCurrency[cur] = round2(total)
		}
		payload, err = json.Marshal(googleplay.SalesDayPayload{
			SalesDaySummary: summary,
			Package:         a.Package,
			Date:            p.date,
			Rows:            t.rows,
			IAPUnits:        t.iapUnits,
			SubUnits:        t.subUnits,
			ProceedsMixed:   len(t.byCurrency) > 1,
			ByCurrency:      byCurrency,
			ByCountry:       t.countries,
			Gross:           true,
			Note:            grossNote,
		})
	} else {
		payload, err = json.Marshal(summary)
	}
	if err != nil {
		return fmt.Errorf("demo: encode sales_day payload: %w", err)
	}

	return d.ingest(ctx, pipe, core.Event{
		ID:         core.NewIDAt(p.day),
		Source:     source,
		Kind:       appstore.KindSalesDay,
		App:        a.Name,
		OccurredAt: p.day,
		ObservedAt: p.day,
		Day:        p.date,
		Amount:     proceeds,
		Currency:   currency,
		Quantity:   t.units,
		DedupeKey:  dedupe,
		IsLedger:   true,
		Chest:      true,
		Payload:    payload,
	})
}

// subscriptionSnapshot reports how many people are subscribed right now. It is
// a count, not money: silent, and never a ledger row.
func (d *Demo) subscriptionSnapshot(ctx context.Context, pipe *pipeline.Pipeline, p dayPlan, a app, t *dayTotals) error {
	if a.Subscribers == 0 {
		return nil
	}
	active := int(float64(a.Subscribers) * (1 + (a.Growth-1)*p.progress()))
	if p.badWeek() {
		active = int(float64(active) * 0.97)
	}

	byCountry := map[string]int{}
	for c, n := range t.subCountry {
		byCountry[c] = n
	}
	payload, err := json.Marshal(appstore.SubscriptionSnapshot{
		AppleID:   a.AppleID,
		App:       a.Name,
		Date:      p.date,
		Active:    active,
		Rows:      len(byCountry),
		ByCountry: byCountry,
		ByState:   map[string]int{"Active": active},
	})
	if err != nil {
		return fmt.Errorf("demo: encode subscription snapshot: %w", err)
	}

	return d.ingest(ctx, pipe, core.Event{
		ID:         core.NewIDAt(p.day),
		Source:     appstore.Name,
		Kind:       appstore.KindSubscriptionSnapshot,
		App:        a.Name,
		OccurredAt: p.day,
		ObservedAt: p.day,
		Day:        p.date,
		Quantity:   active,
		DedupeKey:  fmt.Sprintf("asc:subs:%s:%s", p.date, a.AppleID),
		Silent:     true,
		Payload:    payload,
	})
}

// playInstalls emits Google Play's install reports: a silent row per country,
// the active-device count, and the chest-bound daily total.
func (d *Demo) playInstalls(ctx context.Context, pipe *pipeline.Pipeline, p dayPlan, a app) error {
	if a.InstallBase == 0 {
		return nil
	}
	total := p.count(a.InstallBase * p.scale(a))
	if total <= 0 {
		return nil
	}

	open := p.storefronts()
	byCountry := map[string]int{}
	for i := 0; i < total; i++ {
		byCountry[pickStorefront(p.rng, open).Code]++
	}

	countries := make([]string, 0, len(byCountry))
	for c := range byCountry {
		countries = append(countries, c)
	}
	sort.Strings(countries)

	for _, c := range countries {
		payload, err := json.Marshal(map[string]any{
			"package":               a.Package,
			"date":                  p.date,
			"installs":              byCountry[c],
			"daily_device_installs": byCountry[c],
			"daily_user_installs":   byCountry[c],
			"country":               c,
		})
		if err != nil {
			return fmt.Errorf("demo: encode install row: %w", err)
		}
		if err := d.ingest(ctx, pipe, core.Event{
			ID:         core.NewIDAt(p.day),
			Source:     googleplay.Name,
			Kind:       "install",
			App:        a.Name,
			OccurredAt: p.day,
			ObservedAt: p.day,
			Day:        p.date,
			Country:    c,
			Quantity:   byCountry[c],
			DedupeKey:  fmt.Sprintf("play:installs:%s:%s:%s", a.Package, p.date, c),
			Silent:     true,
			Payload:    payload,
		}); err != nil {
			return err
		}
	}

	activeDevices := int(float64(total) * (28 + 14*p.progress()))
	dayPayload, err := json.Marshal(map[string]any{
		"package":                a.Package,
		"date":                   p.date,
		"installs":               total,
		"daily_device_installs":  total,
		"daily_user_installs":    total,
		"active_device_installs": activeDevices,
	})
	if err != nil {
		return fmt.Errorf("demo: encode installs_day: %w", err)
	}

	if err := d.ingest(ctx, pipe, core.Event{
		ID:         core.NewIDAt(p.day),
		Source:     googleplay.Name,
		Kind:       "active_devices",
		App:        a.Name,
		OccurredAt: p.day,
		ObservedAt: p.day,
		Day:        p.date,
		Quantity:   activeDevices,
		DedupeKey:  fmt.Sprintf("play:active:%s:%s", a.Package, p.date),
		Silent:     true,
		Payload:    dayPayload,
	}); err != nil {
		return err
	}

	return d.ingest(ctx, pipe, core.Event{
		ID:         core.NewIDAt(p.day),
		Source:     googleplay.Name,
		Kind:       "installs_day",
		App:        a.Name,
		OccurredAt: p.day,
		ObservedAt: p.day,
		Day:        p.date,
		Quantity:   total,
		DedupeKey:  fmt.Sprintf("play:installs_day:%s:%s", a.Package, p.date),
		Chest:      true,
		Payload:    dayPayload,
	})
}

// flathubDay emits what Flathub reports per app per completed day, in the same
// two-event shape the real source uses: a silent `install` row carrying the
// count, and a chest-bound `installs_day` headline.
func (d *Demo) flathubDay(ctx context.Context, pipe *pipeline.Pipeline, p dayPlan, a app) error {
	if a.Flatpak == "" || a.FlathubBase == 0 {
		return nil
	}
	installs := p.count(a.FlathubBase * p.scale(a))
	if installs <= 0 {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"app": a.Flatpak, "date": p.date, "installs": installs})
	if err != nil {
		return fmt.Errorf("demo: encode flathub payload: %w", err)
	}

	base := core.Event{
		Source:     "flathub",
		App:        a.Flatpak,
		OccurredAt: p.day,
		ObservedAt: p.day,
		Day:        p.date,
		Quantity:   installs,
		Payload:    payload,
	}

	row := base
	row.ID = core.NewIDAt(p.day)
	row.Kind = "install"
	row.Silent = true
	row.DedupeKey = fmt.Sprintf("flathub:%s:%s", a.Flatpak, p.date)
	if err := d.ingest(ctx, pipe, row); err != nil {
		return err
	}

	headline := base
	headline.ID = core.NewIDAt(p.day)
	headline.Kind = "installs_day"
	headline.Chest = true
	headline.DedupeKey = fmt.Sprintf("flathub:installs_day:%s:%s", a.Flatpak, p.date)
	return d.ingest(ctx, pipe, headline)
}

// revenueCatDay replays the day's real-time subscription traffic: the
// purchases, renewals and cancellations that would have arrived by webhook
// while the day was happening.
func (d *Demo) revenueCatDay(ctx context.Context, pipe *pipeline.Pipeline, p dayPlan, a app) error {
	events := p.count((3 + 5*p.progress()) * p.scale(a) / 2)
	open := p.storefronts()

	for i := 0; i < events; i++ {
		s := pickStorefront(p.rng, open)
		occurred := p.at()

		rcType := "RENEWAL"
		switch r := p.rng.Float64(); {
		case p.badWeek() && r < 0.34:
			rcType = "CANCELLATION"
		case r < 0.10:
			rcType = "CANCELLATION"
		case r < 0.40:
			rcType = "INITIAL_PURCHASE"
		}

		pr := pickSubscription(p.rng, a)
		ev, err := revenueCatEvent(a, pr, s, rcType, occurred, p.rng)
		if err != nil {
			return err
		}
		if err := d.ingest(ctx, pipe, ev); err != nil {
			return err
		}
	}
	return nil
}

// pickSubscription draws one of an app's subscription products, falling back
// to any product for an app that does not sell one.
func pickSubscription(rng *rand.Rand, a app) product {
	var subs []product
	for _, pr := range a.Products {
		if pr.Kind == "subscription" {
			subs = append(subs, pr)
		}
	}
	if len(subs) == 0 {
		return pickProduct(rng, a.Products)
	}
	return pickProduct(rng, subs)
}

// revenueCatEvent builds an event in the shape the RevenueCat webhook source
// produces, payload included, so the rules that read `event.period_type` and
// the drop inspector both see what they would see in production.
func revenueCatEvent(a app, pr product, s storefront, rcType string, occurred time.Time, rng *rand.Rand) (core.Event, error) {
	id := uuidLike(rng)
	price := round2(pr.PriceUSD * s.Rate)
	period := pr.Period
	if period == "" {
		period = "NORMAL"
	}

	body := map[string]any{
		"api_version": "1.0",
		"event": map[string]any{
			"id":                          id,
			"type":                        rcType,
			"app_id":                      a.Name,
			"app_user_id":                 "user_" + uuidLike(rng)[:8],
			"product_id":                  pr.SKU,
			"period_type":                 period,
			"price":                       price,
			"price_in_purchased_currency": price,
			"currency":                    s.Currency,
			"country_code":                s.Code,
			"store":                       storeFor(a),
			"environment":                 "PRODUCTION",
			"event_timestamp_ms":          occurred.UTC().UnixMilli(),
			"purchased_at_ms":             occurred.UTC().UnixMilli(),
			"transaction_id":              fmt.Sprintf("%d", 2000000000000+rng.Int64N(999999999999)),
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return core.Event{}, fmt.Errorf("demo: encode revenuecat payload: %w", err)
	}

	amount := price
	quantity := 1
	if rcType == "CANCELLATION" || rcType == "EXPIRATION" {
		// A cancellation is news, not money: RevenueCat still sends the price
		// of the plan being lost, but Loot must not count it as a purchase.
		amount = 0
		quantity = 0
	}

	kinds := map[string]string{
		"INITIAL_PURCHASE": "purchase",
		"RENEWAL":          "renewal",
		"CANCELLATION":     "cancellation",
		"UNCANCELLATION":   "uncancellation",
		"EXPIRATION":       "expiration",
	}

	return core.Event{
		ID:         core.NewIDAt(occurred),
		Source:     "revenuecat",
		Kind:       kinds[rcType],
		App:        a.Name,
		OccurredAt: occurred,
		ObservedAt: occurred,
		Country:    s.Code,
		Amount:     amount,
		Currency:   s.Currency,
		Quantity:   quantity,
		DedupeKey:  "rc:" + id,
		Payload:    payload,
	}, nil
}

// storeFor names the store a RevenueCat event came through.
func storeFor(a app) string {
	if a.AppStore {
		return "APP_STORE"
	}
	return "PLAY_STORE"
}

// uuidLike renders a deterministic UUID-shaped string from the day's own
// generator, so RevenueCat dedupe keys look like RevenueCat's without needing
// real randomness.
func uuidLike(rng *rand.Rand) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < 32; i++ {
		if i == 8 || i == 12 || i == 16 || i == 20 {
			b.WriteByte('-')
		}
		b.WriteByte(hex[rng.IntN(16)])
	}
	return b.String()
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
