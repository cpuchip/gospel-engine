package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// TokenPrefix is the bearer-token prefix. ALL valid tokens look like
// `stdy_` + 64 hex chars.
const TokenPrefix = "stdy_"

// bcryptCost is intentionally moderate. Tokens are validated on every API
// call so we don't want it as high as a password hash.
const bcryptCost = 10

// APIToken is the metadata for a stored token. The raw secret is never
// kept in the database — only its bcrypt hash.
type APIToken struct {
	ID           int64      `json:"id"`
	ExternalUser string     `json:"external_user,omitempty"`
	Name         string     `json:"name"`
	Prefix       string     `json:"prefix"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsed     *time.Time `json:"last_used,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	RateLimit    int        `json:"rate_limit"`
	Revoked      bool       `json:"revoked"`
}

// CreateAPIToken issues a new bearer token. The raw token string is
// returned exactly once.
func (d *DB) CreateAPIToken(ctx context.Context, externalUser, name string, expiresAt *time.Time, rateLimit int) (*APIToken, string, error) {
	if name == "" {
		return nil, "", errors.New("token name is required")
	}
	if rateLimit <= 0 {
		rateLimit = 60
	}

	rawSecret, err := randomHex(32) // 64 hex chars
	if err != nil {
		return nil, "", err
	}
	full := TokenPrefix + rawSecret
	prefix := full[:12] // "stdy_" (5) + 7 hex chars

	hash, err := bcrypt.GenerateFromPassword([]byte(full), bcryptCost)
	if err != nil {
		return nil, "", fmt.Errorf("hashing token: %w", err)
	}

	row := d.Pool.QueryRow(ctx, `
		INSERT INTO api_tokens (external_user, name, prefix, token_hash, expires_at, rate_limit)
		VALUES (NULLIF($1,''), $2, $3, $4, $5, $6)
		RETURNING id, created_at
	`, externalUser, name, prefix, string(hash), expiresAt, rateLimit)

	t := &APIToken{
		ExternalUser: externalUser,
		Name:         name,
		Prefix:       prefix,
		ExpiresAt:    expiresAt,
		RateLimit:    rateLimit,
	}
	if err := row.Scan(&t.ID, &t.CreatedAt); err != nil {
		return nil, "", fmt.Errorf("inserting token: %w", err)
	}
	return t, full, nil
}

// ValidateAPIToken returns the matching APIToken if the raw token is valid,
// otherwise (nil, nil) for "no match" or (nil, err) for db errors.
func (d *DB) ValidateAPIToken(ctx context.Context, raw string) (*APIToken, error) {
	if len(raw) < 12 {
		return nil, nil
	}
	prefix := raw[:12]

	rows, err := d.Pool.Query(ctx, `
		SELECT id, COALESCE(external_user,''), name, prefix, token_hash,
		       created_at, last_used, expires_at, rate_limit, revoked
		FROM api_tokens
		WHERE prefix = $1 AND revoked = FALSE
	`, prefix)
	if err != nil {
		return nil, fmt.Errorf("query tokens by prefix: %w", err)
	}
	defer rows.Close()

	now := time.Now().UTC()
	for rows.Next() {
		var (
			t    APIToken
			hash string
		)
		if err := rows.Scan(&t.ID, &t.ExternalUser, &t.Name, &t.Prefix, &hash,
			&t.CreatedAt, &t.LastUsed, &t.ExpiresAt, &t.RateLimit, &t.Revoked); err != nil {
			return nil, err
		}
		if t.ExpiresAt != nil && now.After(*t.ExpiresAt) {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(raw)) == nil {
			return &t, nil
		}
	}
	return nil, nil
}

// TouchAPIToken updates last_used. Errors are swallowed — best-effort.
func (d *DB) TouchAPIToken(ctx context.Context, id int64) {
	_, _ = d.Pool.Exec(ctx, `UPDATE api_tokens SET last_used = NOW() WHERE id = $1`, id)
}

// ListAPITokens returns all tokens (admin-only endpoint normally).
func (d *DB) ListAPITokens(ctx context.Context) ([]*APIToken, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT id, COALESCE(external_user,''), name, prefix,
		       created_at, last_used, expires_at, rate_limit, revoked
		FROM api_tokens
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []*APIToken
	for rows.Next() {
		t := &APIToken{}
		if err := rows.Scan(&t.ID, &t.ExternalUser, &t.Name, &t.Prefix,
			&t.CreatedAt, &t.LastUsed, &t.ExpiresAt, &t.RateLimit, &t.Revoked); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, nil
}

// RevokeAPIToken marks a token as revoked (does not delete history).
func (d *DB) RevokeAPIToken(ctx context.Context, id int64) error {
	tag, err := d.Pool.Exec(ctx, `UPDATE api_tokens SET revoked = TRUE WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}
