package googleplay

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// DecodeText turns a report file into a UTF-8 string. Play is inconsistent
// about encodings — the sales CSV inside the monthly zip is UTF-8, while the
// statistics CSVs have historically been UTF-16LE with a byte order mark — so
// the encoding is sniffed from the BOM rather than assumed per report.
func DecodeText(b []byte) string {
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return decodeUTF16(b[2:], false)
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return decodeUTF16(b[2:], true)
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return string(b[3:])
	default:
		return string(b)
	}
}

func decodeUTF16(b []byte, bigEndian bool) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			units = append(units, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	return string(utf16.Decode(units))
}

// header maps a report's column names to their positions. Play renames columns
// between report generations ("Product ID" became "Package ID", "Buyer
// Country" became "Country of Buyer"), and adds new ones on the right, so
// every lookup goes through the header rather than through a fixed index.
type header map[string]int

// readCSV decodes a report file and returns its header and data rows.
func readCSV(raw []byte) (header, [][]string, error) {
	r := csv.NewReader(strings.NewReader(DecodeText(raw)))
	// Reports gained columns over the years and some rows carry stray quotes;
	// neither is a reason to drop a month of money on the floor.
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	records, err := r.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("parse csv: %w", err)
	}
	if len(records) == 0 {
		return header{}, nil, nil
	}

	h := make(header, len(records[0]))
	for i, name := range records[0] {
		key := normalizeHeader(name)
		if key == "" {
			continue
		}
		if _, dup := h[key]; !dup {
			h[key] = i
		}
	}
	return h, records[1:], nil
}

// normalizeHeader folds a column name to its comparable form: lowercase, no
// surrounding space, no BOM leftovers.
func normalizeHeader(s string) string {
	return strings.ToLower(strings.TrimSpace(strings.Trim(s, "\uFEFF\"")))
}

// get returns the first of names present in the row, so a column that has been
// renamed across report generations can be listed with its aliases.
func (h header) get(row []string, names ...string) string {
	for _, name := range names {
		i, ok := h[name]
		if !ok || i >= len(row) {
			continue
		}
		if v := strings.TrimSpace(row[i]); v != "" {
			return v
		}
	}
	return ""
}

// has reports whether any of names is a column of this report.
func (h header) has(names ...string) bool {
	for _, name := range names {
		if _, ok := h[name]; ok {
			return true
		}
	}
	return false
}

// parseFloat reads a report money field. Play quotes amounts plainly, but
// locale-formatted exports (thousands separators, parenthesised negatives, a
// stray currency symbol) turn up often enough to be worth tolerating.
func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	negative := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")

	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r == '.':
			b.WriteRune(r)
		case r == '-':
			negative = true
		}
	}
	v, err := strconv.ParseFloat(b.String(), 64)
	if err != nil {
		return 0
	}
	if negative {
		return -v
	}
	return v
}

// parseInt reads a report count field. Statistics files write plain integers,
// but an empty cell means zero rather than an error.
func parseInt(s string) int {
	return int(parseFloat(s))
}
