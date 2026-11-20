package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	purposeVerify = "verify"
	purposeReset  = "reset"
	sessionTTL    = 7 * 24 * time.Hour
)

//go:embed schema.sql
var schema string

type account struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

type store struct {
	pool *pgxpool.Pool
}

func (s store) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(338745229)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
}

func (s store) queueMail(ctx context.Context, purpose, email string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO identity_mail_requests AS jobs (purpose, email)
		VALUES ($1, $2)
		ON CONFLICT (purpose, email) DO UPDATE
		SET attempts = 0, created_at = excluded.created_at, next_at = excluded.next_at
		WHERE jobs.attempts >= 5 AND jobs.next_at <= now()
	`, purpose, email)
	return err
}

func (s store) mailChallenge(ctx context.Context, key [32]byte, purpose, email string) (string, error) {
	var candidateNonce [tokenBytes]byte
	if _, err := rand.Read(candidateNonce[:]); err != nil {
		return "", err
	}
	candidate := deriveMailToken(key, purpose, email, candidateNonce[:])
	ttl := 30 * time.Minute
	if purpose == purposeVerify {
		ttl = 24 * time.Hour
	}
	var nonce, hash []byte
	err := s.pool.QueryRow(ctx, `
		INSERT INTO identity_challenges AS current (purpose, email, token_hash, expires_at, token_nonce)
		SELECT $1, $2, $3, now() + $4::int * interval '1 second', $5
		WHERE EXISTS (SELECT 1 FROM identity_accounts WHERE email = $2) = ($1 = 'reset')
		ON CONFLICT (purpose, email) DO UPDATE
		SET token_hash = excluded.token_hash,
		    expires_at = excluded.expires_at,
		    token_nonce = excluded.token_nonce,
		    legacy_token_hash = CASE WHEN current.token_nonce IS NULL AND current.expires_at > now()
		        THEN current.token_hash ELSE NULL END,
		    legacy_expires_at = CASE WHEN current.token_nonce IS NULL AND current.expires_at > now()
		        THEN current.expires_at ELSE NULL END
		WHERE current.token_nonce IS NULL OR current.expires_at <= now()
		RETURNING token_nonce, token_hash
	`, purpose, email, tokenHash(candidate), int(ttl.Seconds()), candidateNonce[:]).Scan(&nonce, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.pool.QueryRow(ctx, `
			SELECT token_nonce, token_hash FROM identity_challenges
			WHERE purpose = $1 AND email = $2 AND expires_at > now()
			  AND EXISTS (SELECT 1 FROM identity_accounts WHERE email = $2) = ($1 = 'reset')
		`, purpose, email).Scan(&nonce, &hash)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	token := deriveMailToken(key, purpose, email, nonce)
	if len(nonce) != tokenBytes || !hmac.Equal(tokenHash(token), hash) {
		return "", errors.New("mail token key does not match unexpired challenge")
	}
	return token, nil
}

func deriveMailToken(key [32]byte, purpose, email string, nonce []byte) string {
	digest := hmac.New(sha256.New, key[:])
	_, _ = digest.Write([]byte("kadrroom-mail-challenge-v1\x00" + purpose + "\x00" + email + "\x00"))
	_, _ = digest.Write(nonce)
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func (s store) challengeExists(ctx context.Context, purpose, token string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM identity_challenges
			WHERE purpose = $1 AND ((token_hash = $2 AND expires_at > now())
			  OR (legacy_token_hash = $2 AND legacy_expires_at > now()))
		)
	`, purpose, tokenHash(token)).Scan(&exists)
	return exists, err
}

func (s store) verify(ctx context.Context, token, name, passwordHash, operatorEmail string) (bool, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var email string
		if err := consumeChallenge(ctx, tx, purposeVerify, token).Scan(&email); err != nil {
			return err
		}
		role := "user"
		if email == operatorEmail {
			role = "operator"
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO identity_accounts (email, display_name, password_hash, role)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (email) DO NOTHING
		`, email, name, passwordHash, role)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		return nil
	})
	return rowOutcome(err)
}

func (s store) reset(ctx context.Context, token, passwordHash string) (bool, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var email string
		if err := consumeChallenge(ctx, tx, purposeReset, token).Scan(&email); err != nil {
			return err
		}
		var accountID string
		if err := tx.QueryRow(ctx, `
			UPDATE identity_accounts SET password_hash = $2 WHERE email = $1 RETURNING id::text
		`, email, passwordHash).Scan(&accountID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "DELETE FROM identity_sessions WHERE account_id = $1::uuid", accountID)
		return err
	})
	return rowOutcome(err)
}

func consumeChallenge(ctx context.Context, tx pgx.Tx, purpose, token string) pgx.Row {
	return tx.QueryRow(ctx, `
		DELETE FROM identity_challenges
		WHERE purpose = $1 AND ((token_hash = $2 AND expires_at > now())
		  OR (legacy_token_hash = $2 AND legacy_expires_at > now()))
		RETURNING email
	`, purpose, tokenHash(token))
}

func (s store) credentials(ctx context.Context, email string) (account, string, error) {
	var profile account
	var passwordHash string
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, email, display_name, role, password_hash FROM identity_accounts WHERE email = $1
	`, email).Scan(&profile.ID, &profile.Email, &profile.DisplayName, &profile.Role, &passwordHash)
	return profile, passwordHash, err
}

func (s store) createSession(ctx context.Context, accountID, passwordHash, token, oldToken string) (bool, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM identity_accounts
			WHERE id = $1::uuid AND password_hash = $2 FOR UPDATE
		`, accountID, passwordHash).Scan(&id); err != nil {
			return err
		}
		if validToken(oldToken) {
			if _, err := tx.Exec(ctx, "DELETE FROM identity_sessions WHERE token_hash = $1", tokenHash(oldToken)); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO identity_sessions (token_hash, account_id, expires_at)
			VALUES ($1, $2::uuid, now() + $3::int * interval '1 second')
		`, tokenHash(token), id, int(sessionTTL.Seconds()))
		return err
	})
	return rowOutcome(err)
}

func (s store) session(ctx context.Context, token string) (account, error) {
	var profile account
	err := s.pool.QueryRow(ctx, `
		SELECT a.id::text, a.email, a.display_name, a.role
		FROM identity_sessions AS s JOIN identity_accounts AS a ON a.id = s.account_id
		WHERE s.token_hash = $1 AND s.expires_at > now()
	`, tokenHash(token)).Scan(&profile.ID, &profile.Email, &profile.DisplayName, &profile.Role)
	return profile, err
}

func (s store) logout(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM identity_sessions WHERE token_hash = $1", tokenHash(token))
	return err
}

func (s store) allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	var hits int
	err := s.pool.QueryRow(ctx, `
		INSERT INTO identity_rate_limits AS limits (key, hits, expires_at)
		VALUES ($1, 1, now() + $3::int * interval '1 second')
		ON CONFLICT (key) DO UPDATE
		SET hits = CASE WHEN limits.expires_at <= now() THEN 1 ELSE limits.hits + 1 END,
		    expires_at = CASE WHEN limits.expires_at <= now() THEN excluded.expires_at ELSE limits.expires_at END
		WHERE limits.expires_at <= now() OR limits.hits < $2
		RETURNING hits
	`, key, limit, int(window.Seconds())).Scan(&hits)
	return rowOutcome(err)
}

func rowOutcome(err error) (bool, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
