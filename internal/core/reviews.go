package core

import "strings"

const (
	// KindReview is a written customer review from an app store (App Store,
	// Google Play, Microsoft Store). Quantity is the star rating (1..5).
	KindReview = "review"

	// KindRatingSnapshot is a silent daily rating aggregate (average rating,
	// rating count, and/or 1-5 star breakdown) from a store's ratings report
	// or lookup endpoint.
	KindRatingSnapshot = "rating_snapshot"
)

// ReviewPayload is the normalized JSON payload stored on KindReview events
// across App Store, Google Play and Microsoft Store.
type ReviewPayload struct {
	ReviewID   string `json:"review_id,omitempty"`
	Rating     int    `json:"rating"`
	Title      string `json:"title,omitempty"`
	Body       string `json:"body,omitempty"`
	Author     string `json:"author,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
	Device     string `json:"device,omitempty"`
	Language   string `json:"language,omitempty"`
	ReplyText  string `json:"reply_text,omitempty"`
	ReplyDate  string `json:"reply_date,omitempty"`
	URL        string `json:"url,omitempty"`
}

// RatingSnapshotPayload is the normalized JSON payload stored on
// KindRatingSnapshot events.
type RatingSnapshotPayload struct {
	// AverageRating is the cumulative / overall average rating (0..5) as of
	// that day (e.g. Google Play's Total Average Rating or Apple's
	// averageUserRating).
	AverageRating float64 `json:"average_rating,omitempty"`
	// DailyAverage is the average rating submitted specifically on that day
	// (e.g. Google Play's Daily Average Rating or Microsoft Store daily rows).
	DailyAverage float64 `json:"daily_average,omitempty"`
	// RatingCount is the cumulative rating count when reported (Apple iTunes
	// Lookup userRatingCount), or that day's rating count when Stars is set.
	RatingCount int `json:"rating_count,omitempty"`
	// CumulativeCount is true when RatingCount is a lifetime running total
	// (Apple iTunes Lookup) rather than a single day's additions (MS Store).
	CumulativeCount bool `json:"cumulative_count,omitempty"`
	// Stars holds [1★, 2★, 3★, 4★, 5★] counts for that day when the store
	// exposes a star histogram (Microsoft Store).
	Stars [5]int `json:"stars,omitempty"`
}

// CountryFromISO3 converts a 3-letter ISO 3166-1 alpha-3 territory code (such
// as Apple's "USA", "GBR", "DEU") to an alpha-2 country code ("US", "GB",
// "DE"). Already-valid 2-letter codes are returned uppercased; unknown codes
// return "".
func CountryFromISO3(code string) string {
	c := strings.ToUpper(strings.TrimSpace(code))
	if len(c) == 2 && c[0] >= 'A' && c[0] <= 'Z' && c[1] >= 'A' && c[1] <= 'Z' {
		return c
	}
	return iso3To2[c]
}

// iso3To2 maps App Store storefront ISO-3 territory codes to ISO-2 codes.
var iso3To2 = map[string]string{
	"AFG": "AF", "ALB": "AL", "DZA": "DZ", "AGO": "AO", "AIA": "AI", "ATG": "AG",
	"ARG": "AR", "ARM": "AM", "AUS": "AU", "AUT": "AT", "AZE": "AZ", "BHS": "BS",
	"BHR": "BH", "BGD": "BD", "BRB": "BB", "BLR": "BY", "BEL": "BE", "BLZ": "BZ",
	"BEN": "BJ", "BMU": "BM", "BTN": "BT", "BOL": "BO", "BIH": "BA", "BWA": "BW",
	"BRA": "BR", "VGB": "VG", "BRN": "BN", "BGR": "BG", "BFA": "BF", "KHM": "KH",
	"CMR": "CM", "CAN": "CA", "CPV": "CV", "CYM": "KY", "TCD": "TD", "CHL": "CL",
	"CHN": "CN", "COL": "CO", "COG": "CG", "COD": "CD", "CRI": "CR", "CIV": "CI",
	"HRV": "HR", "CYP": "CY", "CZE": "CZ", "DNK": "DK", "DMA": "DM", "DOM": "DO",
	"ECU": "EC", "EGY": "EG", "SLV": "SV", "EST": "EE", "SWZ": "SZ", "ETH": "ET",
	"FJI": "FJ", "FIN": "FI", "FRA": "FR", "GAB": "GA", "GMB": "GM", "GEO": "GE",
	"DEU": "DE", "GHA": "GH", "GRC": "GR", "GRD": "GD", "GTM": "GT", "GNB": "GW",
	"GUY": "GY", "HND": "HN", "HKG": "HK", "HUN": "HU", "ISL": "IS", "IND": "IN",
	"IDN": "ID", "IRQ": "IQ", "IRL": "IE", "ISR": "IL", "ITA": "IT", "JAM": "JM",
	"JPN": "JP", "JOR": "JO", "KAZ": "KZ", "KEN": "KE", "KOR": "KR", "XKS": "XK",
	"KWT": "KW", "KGZ": "KG", "LAO": "LA", "LVA": "LV", "LBN": "LB", "LBR": "LR",
	"LBY": "LY", "LTU": "LT", "LUX": "LU", "MAC": "MO", "MDG": "MG", "MWI": "MW",
	"MYS": "MY", "MDV": "MV", "MLI": "ML", "MLT": "MT", "MRT": "MR", "MUS": "MU",
	"MEX": "MX", "FSM": "FM", "MDA": "MD", "MNG": "MN", "MNE": "ME", "MSR": "MS",
	"MAR": "MA", "MOZ": "MZ", "MMR": "MM", "NAM": "NA", "NRU": "NR", "NPL": "NP",
	"NLD": "NL", "NZL": "NZ", "NIC": "NI", "NER": "NE", "NGA": "NG", "MKD": "MK",
	"NOR": "NO", "OMN": "OM", "PAK": "PK", "PLW": "PW", "PAN": "PA", "PNG": "PG",
	"PRY": "PY", "PER": "PE", "PHL": "PH", "POL": "PL", "PRT": "PT", "QAT": "QA",
	"ROU": "RO", "RUS": "RU", "RWA": "RW", "KNA": "KN", "LCA": "LC", "VCT": "VC",
	"WSM": "WS", "STP": "ST", "SAU": "SA", "SEN": "SN", "SRB": "RS", "SYC": "SC",
	"SLE": "SL", "SGP": "SG", "SVK": "SK", "SVN": "SI", "SLB": "SB", "ZAF": "ZA",
	"ESP": "ES", "LKA": "LK", "SUR": "SR", "SWE": "SE", "CHE": "CH", "TWN": "TW",
	"TJK": "TJ", "TZA": "TZ", "THA": "TH", "TON": "TO", "TTO": "TT", "TUN": "TN",
	"TUR": "TR", "TKM": "TM", "TCA": "TC", "UGA": "UG", "UKR": "UA", "ARE": "AE",
	"GBR": "GB", "USA": "US", "URY": "UY", "UZB": "UZ", "VUT": "VU", "VEN": "VE",
	"VNM": "VN", "YEM": "YE", "ZMB": "ZM", "ZWE": "ZW",
}
