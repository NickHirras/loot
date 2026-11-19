package core

// # Things that happened, and how things stand
//
// Almost every event Loot stores is something that *happened*: a sale, a first
// download, a release. Adding those up forever is exactly right — it is what
// the vault, the quests and XP are made of.
//
// An audience event is the other kind of number: how things *stand*. It is a
// store's own answer to "how many devices have this app right now", it is
// re-stated every day, and yesterday's reading is simply out of date rather
// than something to add today's to. Only the newest one per (source, app,
// country) is ever shown.
//
// The distinction is the whole reason this file exists. Loot once tried to get
// the second kind of number by summing the first: Snapcraft's daily "new"
// devices, added up, as a population. But the store's "new" means "seen today
// and not yesterday" — a laptop that was switched off on Sunday is new again
// on Monday — so a base of ~190 devices read as ~1,700 people within three
// months. The store had the real figure all along; Loot was not asking for it.
const (
	// KindAudience is a snapshot of a store's current installed base.
	// Quantity is the base; Country is set on the per-country rows and empty on
	// the one row per (source, app, day) that carries the whole total. The
	// total is authoritative: it includes devices the store could not place.
	KindAudience = "audience"

	// KindAudienceMilestone is minted by the pipeline the first time an app's
	// audience total on one source reaches a rung of AudienceMilestones.
	// Quantity is the rung, not the reading that crossed it.
	KindAudienceMilestone = "audience_milestone"

	// KindAssetDownload counts downloads of a release file (a .deb, a .flatpak
	// bundle) as reported by the host's own counter. Downloads are not people:
	// a bot is a download, and so is every update a user fetches by hand. They
	// are shown as downloads and never added to an audience.
	KindAssetDownload = "asset_download"
)

// AudienceBasis says what a source's audience figure actually measures, so the
// UI can label a number with what it is instead of calling everything "users".
type AudienceBasis string

const (
	// BasisWeeklyDevices: distinct devices seen at least once in the trailing
	// seven days (Snapcraft's weekly installed base).
	BasisWeeklyDevices AudienceBasis = "weekly_devices"
	// BasisActiveDevices: devices that have the app installed and were active
	// in the store's own trailing window (Google Play's active device installs).
	BasisActiveDevices AudienceBasis = "active_devices"
	// BasisArrivals: the store reports no current base at all, so the figure
	// is every arrival it has ever reported — downloads, installs, purchases —
	// and it can only grow. An honest fallback, labelled as such.
	BasisArrivals AudienceBasis = "arrivals"
)

// audienceBasis maps a source to what its `audience` events measure. A source
// missing from it that emits KindAudience anyway is read as active devices,
// which is the most generic true description.
var audienceBasis = map[string]AudienceBasis{
	"snapcraft":  BasisWeeklyDevices,
	"googleplay": BasisActiveDevices,
}

// AudienceBasisFor returns what source's audience snapshots measure.
func AudienceBasisFor(source string) AudienceBasis {
	if b, ok := audienceBasis[source]; ok {
		return b
	}
	return BasisActiveDevices
}

// AudienceMilestones are the audience sizes worth a drop. The ladder is dense
// at the bottom on purpose: for a small app, going from 10 devices to 25 is
// bigger news than a large one going from 5,000 to 5,010.
var AudienceMilestones = []int{
	5, 10, 25, 50, 75, 100, 150, 200, 250, 300, 400, 500, 750,
	1000, 1500, 2000, 2500, 5000, 7500, 10000, 25000, 50000, 100000,
}

// AudienceMilestone returns the highest rung of AudienceMilestones that
// audience has reached, or 0 when it has not reached the first.
func AudienceMilestone(audience int) int {
	best := 0
	for _, m := range AudienceMilestones {
		if audience >= m {
			best = m
		}
	}
	return best
}
