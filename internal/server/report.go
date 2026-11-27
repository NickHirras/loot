package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/funnel"
	"github.com/nickhirras/loot/internal/fx"
	"github.com/nickhirras/loot/internal/store"
)

// Report is the whole of GET /api/report: a single machine-readable snapshot
// combining stats, vault revenue, per-app audience/downloads, ratings/reviews,
// acquisition funnel (when configured), and items needing attention.
type Report struct {
	GeneratedAt     time.Time               `json:"generated_at"`
	Version         string                  `json:"version"`
	Demo            bool                    `json:"demo"`
	App             string                  `json:"app"`
	Apps            []string                `json:"apps"`
	DisplayCurrency string                  `json:"display_currency"`
	Stats           ReportStats             `json:"stats"`
	Vault           store.VaultSummary      `json:"vault"`
	Audience        []store.ProductAudience `json:"audience"`
	Reviews         ReportReviews           `json:"reviews"`
	Funnel          *core.FunnelReport      `json:"funnel,omitempty"`
	Attention       ReportAttention         `json:"attention"`
	Sources         []sourceInfo            `json:"sources"`
}

// ReportStats holds the headline account/scope counters.
type ReportStats struct {
	TotalDrops      int              `json:"total_drops"`
	TotalEvents     int              `json:"total_events"`
	TotalXP         int              `json:"total_xp"`
	Level           int              `json:"level"`
	Era             core.EraProgress `json:"era"`
	CountriesCount  int              `json:"countries_count"`
	Population      int              `json:"population"`
	FleetPopulation int              `json:"fleet_population"`
	UnrevealedCount int              `json:"unrevealed_count"`
	ChestDates      []string         `json:"chest_dates"`
	OpenMysteries   int              `json:"open_mysteries"`
	ActiveQuests    int              `json:"active_quests"`
	BossesAlive     int              `json:"bosses_alive"`
}

// ReportReviews is the ratings and recent-reviews slice inside GET /api/report.
type ReportReviews struct {
	Range         string                   `json:"range"`
	From          string                   `json:"from"`
	To            string                   `json:"to"`
	AverageRating float64                  `json:"average_rating"`
	TotalRatings  int                      `json:"total_ratings"`
	TotalReviews  int                      `json:"total_reviews"`
	LowReviews    int                      `json:"low_reviews"`
	FiveStarShare float64                  `json:"five_star_share"`
	Distribution  [5]int                   `json:"distribution"`
	ByApp         []store.AppRatingSummary `json:"by_app"`
	RecentReviews []store.ReviewItem       `json:"recent_reviews"`
}

// ReportAttention surfaces active items an operator or agent should notice.
type ReportAttention struct {
	ChestsWaiting []core.ChestSummary `json:"chests_waiting"`
	BossesAlive   []core.Boss         `json:"bosses_alive"`
	OpenMysteries []core.Mystery      `json:"open_mysteries"`
	ActiveQuests  []core.Quest        `json:"active_quests"`
	SourceErrors  []sourceInfo        `json:"source_errors"`
}

// handleReport serves GET /api/report?range=7d|30d|90d|365d&app=<product>&reviews_range=30d|90d|1y|all&funnel_days=14&include_series=0.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	qs := r.URL.Query()

	rangeKey := strings.ToLower(strings.TrimSpace(qs.Get("range")))
	if rangeKey == "" {
		rangeKey = defaultRange
	}
	vaultDays, ok := ranges[rangeKey]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "unknown range " + rangeKey + " (use 7d, 30d, 90d or 365d)",
		})
		return
	}

	reviewsRange := strings.ToLower(strings.TrimSpace(qs.Get("reviews_range")))
	if reviewsRange == "" {
		reviewsRange = "90d"
	}

	funnelDays := defaultFunnelDays
	if v := strings.TrimSpace(qs.Get("funnel_days")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "funnel_days must be a whole number"})
			return
		}
		funnelDays = min(max(n, 1), maxFunnelDays)
	}

	includeSeries := truthy(qs.Get("include_series"))

	now := time.Now().UTC()
	today := core.DayOf(now)
	vaultFrom := core.DayOf(now.AddDate(0, 0, -(vaultDays - 1)))
	display := s.displayCurrency()
	scope := scopeOf(r)
	scoped := s.scoped(r)

	st, err := scoped.Stats(ctx)
	if err != nil {
		s.fail(w, "report stats", err)
		return
	}
	pairs, err := s.Store.ProductPairs(ctx)
	if err != nil {
		s.fail(w, "report apps", err)
		return
	}
	vault, err := scoped.VaultSummary(ctx, vaultFrom, today, display, today)
	if err != nil {
		s.fail(w, "report vault", err)
		return
	}
	if !includeSeries {
		vault.Series = nil
	}
	hearth, err := scoped.HearthAt(ctx, s.Cfg.HomeCountry, display, today)
	if err != nil {
		s.fail(w, "report hearth", err)
		return
	}
	reviews, err := scoped.Reviews(ctx, store.ReviewsQuery{
		Range: reviewsRange,
		Limit: 20,
	}, today)
	if err != nil {
		if strings.HasPrefix(err.Error(), "unknown reviews range") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		s.fail(w, "report reviews", err)
		return
	}

	var funnelPtr *core.FunnelReport
	conv := fx.New(fx.Options{
		Base:   display,
		Store:  s.Store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	_ = conv.LoadCached(ctx)
	funnelFrom := core.DayOf(now.AddDate(0, 0, -(funnelDays - 1)))
	frep, err := scoped.Funnel(ctx, funnelFrom, today, store.FunnelOptions{
		DisplayCurrency: display,
		Convert:         conv.ToBase,
		ConversionLabel: s.Cfg.Funnel.ConversionLabel,
		SiteEvent:       s.Cfg.Funnel.SiteEvent,
	})
	if err != nil {
		s.fail(w, "report funnel", err)
		return
	}
	if frep.Enabled {
		frep.Notes = funnel.Notes(frep, s.Cfg.Funnel.Notes, today)
		if !includeSeries {
			frep.Days = nil
		}
		funnelPtr = &frep
	}

	chests, err := s.Store.ChestSummaries(ctx)
	if err != nil {
		s.fail(w, "report chests", err)
		return
	}
	if chests == nil {
		chests = []core.ChestSummary{}
	}

	var activeQuests []core.Quest
	if s.Quests != nil {
		if board, err := s.Quests.ListScoped(ctx, scope); err == nil {
			activeQuests = board.Active
		}
	}
	if activeQuests == nil {
		activeQuests = []core.Quest{}
	}

	var openMysteries []core.Mystery
	if s.Mysteries != nil {
		if book, err := s.Mysteries.List(ctx); err == nil {
			openMysteries = book.Open
			if scope != "" {
				openMysteries = s.mysteriesInScope(scope, openMysteries)
			}
		}
	}
	if openMysteries == nil {
		openMysteries = []core.Mystery{}
	}

	var aliveBosses []core.Boss
	if s.Bosses != nil {
		if board, err := s.Bosses.List(ctx); err == nil {
			aliveBosses = s.bossesInScope(scope, board.Alive)
		}
	}
	if aliveBosses == nil {
		aliveBosses = []core.Boss{}
	}

	sources, err := s.collectSourceInfos(ctx)
	if err != nil {
		s.fail(w, "report sources", err)
		return
	}
	sourceErrors := make([]sourceInfo, 0)
	for _, src := range sources {
		if strings.TrimSpace(src.LastError) != "" {
			sourceErrors = append(sourceErrors, src)
		}
	}

	audience := hearth.Audience
	if audience == nil {
		audience = []store.ProductAudience{}
	}
	byApp := reviews.ByApp
	if byApp == nil {
		byApp = []store.AppRatingSummary{}
	}
	recentReviews := reviews.Reviews
	if recentReviews == nil {
		recentReviews = []store.ReviewItem{}
	}
	chestDates := st.ChestDates
	if chestDates == nil {
		chestDates = []string{}
	}

	rep := Report{
		GeneratedAt:     now,
		Version:         s.Version,
		Demo:            s.Cfg.Demo.Enabled,
		App:             scope,
		Apps:            s.knownProducts(pairs),
		DisplayCurrency: display,
		Stats: ReportStats{
			TotalDrops:      st.TotalDrops,
			TotalEvents:     st.TotalEvents,
			TotalXP:         st.TotalXP,
			Level:           core.LevelFor(st.TotalXP),
			Era:             hearth.Era,
			CountriesCount:  st.CountriesCount,
			Population:      hearth.Population,
			FleetPopulation: hearth.Unknown.Population,
			UnrevealedCount: st.UnrevealedCount,
			ChestDates:      chestDates,
			OpenMysteries:   len(openMysteries),
			ActiveQuests:    len(activeQuests),
			BossesAlive:     len(aliveBosses),
		},
		Vault:    vault,
		Audience: audience,
		Reviews: ReportReviews{
			Range:         reviews.Range,
			From:          reviews.From,
			To:            reviews.To,
			AverageRating: reviews.AverageRating,
			TotalRatings:  reviews.TotalRatings,
			TotalReviews:  reviews.TotalReviews,
			LowReviews:    reviews.LowReviews,
			FiveStarShare: reviews.FiveStarShare,
			Distribution:  reviews.Distribution,
			ByApp:         byApp,
			RecentReviews: recentReviews,
		},
		Funnel: funnelPtr,
		Attention: ReportAttention{
			ChestsWaiting: chests,
			BossesAlive:   aliveBosses,
			OpenMysteries: openMysteries,
			ActiveQuests:  activeQuests,
			SourceErrors:  sourceErrors,
		},
		Sources: sources,
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) collectSourceInfos(ctx context.Context) ([]sourceInfo, error) {
	states, err := s.Store.SourceStates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]sourceInfo, 0, len(s.Sources))
	for _, src := range s.Sources {
		info := sourceInfo{Name: src.Name(), Mode: "webhook"}
		if d := src.PollInterval(); d > 0 {
			info.Mode = "poll"
			info.PollInterval = d.String()
		}
		if st, ok := states[src.Name()]; ok {
			info.LastPollAt = st.LastPollAt
			info.LastError = st.LastError
		}
		if n, err := s.Store.EventCount(ctx, src.Name()); err == nil {
			info.Events = n
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
