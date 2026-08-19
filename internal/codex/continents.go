package codex

import "strings"

// Which continent a country sits on, for exactly one achievement:
// **Cartographer**, a settlement on every inhabited continent.
//
// This is a small table rather than a dependency because the question it
// answers is small: six buckets, no geometry, no coordinates. The Hearth
// already owns the map; this owns the answer to "have you sold on every
// continent yet?", which the map cannot answer without a projection.
//
// Antarctica is deliberately absent. Nobody buys an app from Antarctica, and
// an achievement nobody can finish is a locked card that never moves — which
// is exactly the kind of quiet nagging the Codex is supposed to avoid.

// Continent identifiers, ordered as the UI lists them.
const (
	Africa       = "AF"
	Asia         = "AS"
	Europe       = "EU"
	NorthAmerica = "NA"
	SouthAmerica = "SA"
	Oceania      = "OC"
)

// InhabitedContinents is what Cartographer asks for.
var InhabitedContinents = []string{Africa, Asia, Europe, NorthAmerica, SouthAmerica, Oceania}

// ContinentNames are the words a highlight line uses.
var ContinentNames = map[string]string{
	Africa:       "Africa",
	Asia:         "Asia",
	Europe:       "Europe",
	NorthAmerica: "North America",
	SouthAmerica: "South America",
	Oceania:      "Oceania",
}

// continentCountries lists ISO 3166-1 alpha-2 codes per continent. Territories
// are filed with the continent they sit on rather than with the country that
// administers them, because a customer in Guadeloupe is a customer in North
// America however Paris files the paperwork.
var continentCountries = map[string]string{
	Africa: `AO BF BI BJ BW CD CF CG CI CM CV DJ DZ EG EH ER ET GA GH GM GN GQ GW KE KM LR LS LY
             MA MG ML MR MU MW MZ NA NE NG RE RW SC SD SH SL SN SO SS ST SZ TD TG TN TZ UG YT ZA ZM ZW`,
	Asia: `AE AF AM AZ BD BH BN BT CC CN CX CY GE HK ID IL IN IO IQ IR JO JP KG KH KP KR KW KZ LA LB LK
           MM MN MO MV MY NP OM PH PK PS QA SA SG SY TH TJ TL TM TR TW UZ VN YE`,
	Europe: `AD AL AT AX BA BE BG BY CH CZ DE DK EE ES FI FO FR GB GG GI GR HR HU IE IM IS IT JE LI LT
             LU LV MC MD ME MK MT NL NO PL PT RO RS RU SE SI SJ SK SM UA VA XK`,
	NorthAmerica: `AG AI AW BB BL BM BQ BS BZ CA CR CU CW DM DO GD GL GP GT HN HT JM KN KY LC MF MQ MS MX
                   NI PA PM PR SV SX TC TT US VC VG VI`,
	SouthAmerica: `AR BO BR CL CO EC FK GF GY PE PY SR UY VE`,
	Oceania:      `AS AU CK FJ FM GU KI MH MP NC NF NR NU NZ PF PG PN PW SB TK TO TV UM VU WF WS`,
}

// continentOf is the reverse lookup, built once at startup.
var continentOf = func() map[string]string {
	out := make(map[string]string, 250)
	for continent, list := range continentCountries {
		for _, code := range strings.Fields(list) {
			out[code] = continent
		}
	}
	return out
}()

// ContinentOf returns the continent an ISO2 country code sits on, or "" for a
// code this table does not know. An unknown code counts for nothing rather
// than being filed somewhere plausible: guessing would hand out Cartographer
// for a typo.
func ContinentOf(iso2 string) string {
	return continentOf[strings.ToUpper(strings.TrimSpace(iso2))]
}
