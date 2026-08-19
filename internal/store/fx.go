package store

import (
	"context"
	"fmt"
	"strings"
)

// PutFXRates replaces the cached rate table for base. Rates are quoted as
// "how many units of quote buy one base", matching the upstream ECB feed.
func (s *Store) PutFXRates(ctx context.Context, base string, rates map[string]float64, asOf string) error {
	base = strings.ToUpper(strings.TrimSpace(base))
	if base == "" || len(rates) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fx rates: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM fx_rates WHERE base = ?`, base); err != nil {
		return fmt.Errorf("clear fx rates: %w", err)
	}
	for quote, rate := range rates {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO fx_rates (base, quote, rate, as_of) VALUES (?, ?, ?, ?)`,
			base, strings.ToUpper(quote), rate, asOf); err != nil {
			return fmt.Errorf("insert fx rate %s/%s: %w", base, quote, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fx rates: %w", err)
	}
	return nil
}

// GetFXRates returns the cached rate table for base and the date it was
// published. A cold cache returns an empty map and no error.
func (s *Store) GetFXRates(ctx context.Context, base string) (map[string]float64, string, error) {
	base = strings.ToUpper(strings.TrimSpace(base))
	rows, err := s.db.QueryContext(ctx,
		`SELECT quote, rate, as_of FROM fx_rates WHERE base = ?`, base)
	if err != nil {
		return nil, "", fmt.Errorf("get fx rates: %w", err)
	}
	defer rows.Close()

	out := map[string]float64{}
	asOf := ""
	for rows.Next() {
		var (
			quote string
			rate  float64
			at    string
		)
		if err := rows.Scan(&quote, &rate, &at); err != nil {
			return nil, "", fmt.Errorf("scan fx rate: %w", err)
		}
		out[quote] = rate
		if at > asOf {
			asOf = at
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate fx rates: %w", err)
	}
	return out, asOf, nil
}

// BackfillAmountBase fills amount_base for rows that already store their money
// in the display currency. It runs at startup and is cheap after the first
// pass because it only touches rows still sitting at zero. Rows in a foreign
// currency need real rates and are handled by RecomputeAmountBase.
func (s *Store) BackfillAmountBase(ctx context.Context, displayCurrency string) (int64, error) {
	displayCurrency = strings.ToUpper(strings.TrimSpace(displayCurrency))
	if displayCurrency == "" {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `
        UPDATE events SET amount_base = amount
        WHERE amount <> 0 AND amount_base = 0
          AND (UPPER(currency) = ? OR currency = '')`, displayCurrency)
	if err != nil {
		return 0, fmt.Errorf("backfill amount_base: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("backfill amount_base rows: %w", err)
	}
	return n, nil
}

// RecomputeAmountBase re-derives amount_base for every event carrying money,
// using convert. It backs `loot fx recompute`, which is how a historical
// database picks up conversions after FX rates first become available.
// Conversions that fail leave the row untouched and are counted in skipped.
func (s *Store) RecomputeAmountBase(ctx context.Context, convert func(amount float64, currency string) (float64, bool)) (updated, skipped int, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, amount, currency FROM events WHERE amount <> 0`)
	if err != nil {
		return 0, 0, fmt.Errorf("recompute amount_base: %w", err)
	}

	type row struct {
		id     string
		amount float64
		cur    string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.amount, &r.cur); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("scan amount row: %w", err)
		}
		pending = append(pending, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterate amount rows: %w", err)
	}

	for _, r := range pending {
		base, ok := convert(r.amount, r.cur)
		if !ok {
			skipped++
			continue
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE events SET amount_base = ? WHERE id = ?`, base, r.id); err != nil {
			return updated, skipped, fmt.Errorf("update amount_base: %w", err)
		}
		updated++
	}
	return updated, skipped, nil
}
