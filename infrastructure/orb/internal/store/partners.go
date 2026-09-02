package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrSamePartner is returned when an account is linked to itself.
var ErrSamePartner = errors.New("store: an account cannot be its own partner")

// Partner is the other person in the bed, as far as the algorithms care.
type Partner struct {
	AccountID int64
	Email     string
	Name      string
}

// PartnerOf returns the account's partner, if it has one.
//
// The link is explicit (account_partners), not inferred from a shared Sense as
// the reference did. Two people can share a bed and each keep their own Sense.
func (s *Store) PartnerOf(ctx context.Context, accountID int64) (Partner, bool, error) {
	var out Partner
	err := s.pool.QueryRow(ctx, `
		SELECT a.id, a.email, a.name
		FROM account_partners p
		JOIN accounts a ON a.id = p.partner_id
		WHERE p.account_id = $1`, accountID).Scan(&out.AccountID, &out.Email, &out.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, fmt.Errorf("store: partner of %d: %w", accountID, err)
	}
	return out, true, nil
}

// SetPartner links two accounts as partners, both ways, in one transaction.
//
// Any partner either account already had is unlinked first, in both
// directions, so the table never holds a one-sided link and nobody ends up
// with two partners. Linking the same pair again is a no-op that refreshes
// nothing and fails nothing.
func (s *Store) SetPartner(ctx context.Context, accountID, partnerID int64) error {
	if accountID == partnerID {
		return ErrSamePartner
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		DELETE FROM account_partners
		WHERE account_id IN ($1, $2) OR partner_id IN ($1, $2)`, accountID, partnerID); err != nil {
		return fmt.Errorf("store: unlink partners: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO account_partners (account_id, partner_id)
		VALUES ($1, $2), ($2, $1)`, accountID, partnerID); err != nil {
		return fmt.Errorf("store: link partners: %w", err)
	}
	return tx.Commit(ctx)
}

// ClearPartner removes the account's partner link in both directions.
func (s *Store) ClearPartner(ctx context.Context, accountID int64) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM account_partners
		WHERE account_id = $1 OR partner_id = $1`, accountID)
	if err != nil {
		return fmt.Errorf("store: clear partner of %d: %w", accountID, err)
	}
	return nil
}

// AccountIDByEmail resolves an address to an account id.
func (s *Store) AccountIDByEmail(ctx context.Context, email string) (int64, bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM accounts WHERE lower(email) = lower($1)`, email).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: account by email: %w", err)
	}
	return id, true, nil
}
