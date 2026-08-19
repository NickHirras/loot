package appstore

import (
	"fmt"
	"strconv"
	"strings"
)

// Apple's report columns move: 1_0 and 1_1 of the sales summary differ, and
// Apple has added columns (Client, Order Type, Preserved Pricing…) mid-version
// without warning. Everything here therefore addresses columns *by name*,
// normalized, so an inserted or reordered column cannot silently shift every
// value one place to the left.

// table is a parsed TSV report: a header index plus the data rows.
type table struct {
	// columns maps a normalized header name to its position.
	columns map[string]int
	// header keeps the original names, in order, for diagnostics.
	header []string
	rows   [][]string
}

// parseTSV splits an Apple report into a table. Reports are plain tab
// separated values with a single header line and no quoting; a field
// containing a tab is not representable, so a naive split is also the correct
// one. Blank lines and a trailing newline are tolerated.
func parseTSV(data []byte) (*table, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimPrefix(text, "\ufeff")

	lines := strings.Split(text, "\n")
	t := &table{columns: map[string]int{}}

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if t.header == nil {
			t.header = fields
			for i, name := range fields {
				key := normalizeColumn(name)
				if key == "" {
					continue
				}
				if _, seen := t.columns[key]; !seen {
					t.columns[key] = i
				}
			}
			continue
		}
		t.rows = append(t.rows, fields)
	}

	if t.header == nil {
		return nil, fmt.Errorf("appstore: report is empty (no header row)")
	}
	if len(t.columns) < 2 {
		return nil, fmt.Errorf("appstore: report header does not look like a TSV report: %q", strings.Join(t.header, "|"))
	}
	return t, nil
}

// normalizeColumn reduces a header to letters and digits, lowercased, so
// "Developer Proceeds", "developer proceeds" and "Developer_Proceeds" all
// resolve to the same column.
func normalizeColumn(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// index returns the position of the first of names that the report has, or -1.
// A name also matches a longer header that starts with it, which is how
// "Developer Proceeds" finds Apple's "Developer Proceeds (per unit)".
func (t *table) index(names ...string) int {
	for _, name := range names {
		if i, ok := t.columns[normalizeColumn(name)]; ok {
			return i
		}
	}
	for _, name := range names {
		want := normalizeColumn(name)
		if want == "" {
			continue
		}
		best := -1
		for have, i := range t.columns {
			if strings.HasPrefix(have, want) && (best == -1 || i < best) {
				best = i
			}
		}
		if best >= 0 {
			return best
		}
	}
	return -1
}

// get reads a field by column name, returning "" when the column is missing or
// the row is short. Apple's last columns are frequently absent on older rows.
func (t *table) get(row []string, names ...string) string {
	i := t.index(names...)
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// has reports whether the report carries any of the named columns.
func (t *table) has(names ...string) bool { return t.index(names...) >= 0 }

// atoi parses a report integer. Apple writes plain digits, but thousands
// separators and a leading "+" have both been seen in the wild, and an empty
// cell simply means zero.
func atoi(s string) int {
	s = cleanNumber(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		// A decimal in an integer column ("1.0") is still worth reading.
		if f, ferr := strconv.ParseFloat(s, 64); ferr == nil {
			return int(f)
		}
		return 0
	}
	return n
}

// atof parses a report decimal. Apple reports money with a period as the
// decimal separator regardless of the currency.
func atof(s string) float64 {
	s = cleanNumber(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func cleanNumber(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.TrimPrefix(s, "+")
	return s
}

// round2 rounds money to cents. Multiplying a per-unit proceeds figure by a
// unit count otherwise leaves 0.7000000000000001 in the database.
func round2(f float64) float64 {
	return float64(int64(f*100+copysign(0.5, f))) / 100
}

func copysign(magnitude, sign float64) float64 {
	if sign < 0 {
		return -magnitude
	}
	return magnitude
}
