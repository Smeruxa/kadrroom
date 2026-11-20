package catalogmedia

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed media_schema.sql
var mediaSchema string

const mediaFields = "id::text, title, description, media_type, status, duration_seconds, cover_type"

type Record struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	MediaType       string `json:"mediaType"`
	Status          string `json:"status"`
	DurationSeconds int    `json:"durationSeconds"`
	CoverURL        string `json:"coverUrl,omitempty"`
	CoverType       string `json:"-"`
}

type Store struct {
	Pool *pgxpool.Pool
}

type Grants struct {
	Reader string
	Worker string
}

var ErrMediaNotReady = errors.New("media state does not allow the operation")

func Open(ctx context.Context, databaseURL string) (Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return Store{}, errors.New("invalid catalog database configuration")
	}
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	if _, configured := cfg.ConnConfig.RuntimeParams["statement_timeout"]; !configured {
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return Store{}, errors.New("connect to catalog database")
	}
	return Store{Pool: pool}, nil
}

func (s Store) Migrate(ctx context.Context, grants Grants) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(338745230)"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, mediaSchema); err != nil {
			return err
		}
		if grants.Reader != "" {
			role := pgx.Identifier{grants.Reader}.Sanitize()
			if _, err := tx.Exec(ctx, "GRANT SELECT ON catalog_published_media TO "+role); err != nil {
				return fmt.Errorf("grant published catalog access: %w", err)
			}
		}
		if grants.Worker != "" {
			return grantWorker(ctx, tx, grants.Worker)
		}
		return nil
	})
}

func grantWorker(ctx context.Context, tx pgx.Tx, worker string) error {
	var unrestricted bool
	if err := tx.QueryRow(ctx, `
		SELECT rolsuper OR rolbypassrls OR pg_has_role(oid, current_user, 'MEMBER')
		FROM pg_roles WHERE rolname = $1
	`, worker).Scan(&unrestricted); err != nil {
		return fmt.Errorf("read media worker role: %w", err)
	}
	if unrestricted {
		return errors.New("media worker role must not inherit admin rights or bypass row security")
	}
	role := pgx.Identifier{worker}.Sanitize()
	if _, err := tx.Exec(ctx, `
		ALTER TABLE catalog_media ENABLE ROW LEVEL SECURITY;
		DROP POLICY IF EXISTS catalog_worker_read ON catalog_media;
		DROP POLICY IF EXISTS catalog_worker_update ON catalog_media;
		CREATE POLICY catalog_worker_read ON catalog_media FOR SELECT TO `+role+`
			USING (status IN ('processing', 'ready', 'failed'));
		CREATE POLICY catalog_worker_update ON catalog_media FOR UPDATE TO `+role+`
			USING (status = 'processing')
			WITH CHECK (status IN ('processing', 'ready', 'failed'));
		GRANT SELECT ON catalog_media TO `+role+`;
		GRANT UPDATE (status, duration_seconds, claim_id, lease_until) ON catalog_media TO `+role); err != nil {
		return fmt.Errorf("restrict media worker queue access: %w", err)
	}
	return nil
}

func (s Store) ReadyPublic(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, "SELECT 1 FROM catalog_published_media LIMIT 0")
	return err
}

func (s Store) ReadyAdmin(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, "SELECT 1 FROM catalog_media LIMIT 0")
	return err
}

func (s Store) ListPublished(ctx context.Context) ([]Record, error) {
	return s.list(ctx, "catalog_published_media")
}

func (s Store) ListAll(ctx context.Context) ([]Record, error) {
	return s.list(ctx, "catalog_media")
}

func (s Store) list(ctx context.Context, relation string) ([]Record, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+mediaFields+" FROM "+relation+" ORDER BY created_at DESC, id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]Record, 0)
	for rows.Next() {
		record, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s Store) Insert(ctx context.Context, record Record) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO catalog_media (id, title, description, media_type, status, cover_type)
		VALUES ($1::uuid, $2, $3, $4, 'processing', $5)
	`, record.ID, record.Title, record.Description, record.MediaType, record.CoverType)
	return err
}

func (s Store) Published(ctx context.Context, id string) (Record, error) {
	return scanMedia(s.Pool.QueryRow(ctx, "SELECT "+mediaFields+
		" FROM catalog_published_media WHERE id = $1::uuid", id))
}

func (s Store) Transition(ctx context.Context, id, action string) (Record, error) {
	var record Record
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		current, err := scanMedia(tx.QueryRow(ctx, "SELECT "+mediaFields+" FROM catalog_media WHERE id = $1::uuid FOR UPDATE", id))
		if err != nil {
			return err
		}
		var next string
		switch action {
		case "publish":
			if current.Status == "published" {
				record = current
				return nil
			}
			if current.Status == "ready" {
				next = "published"
			}
		case "unpublish":
			if current.Status == "ready" {
				record = current
				return nil
			}
			if current.Status == "published" {
				next = "ready"
			}
		case "retry":
			if current.Status == "processing" {
				record = current
				return nil
			}
			if current.Status == "failed" {
				next = "processing"
			}
		}
		if next == "" {
			return ErrMediaNotReady
		}
		record, err = scanMedia(tx.QueryRow(ctx, `
			UPDATE catalog_media
			SET status = $2, published_at = CASE WHEN $2 = 'published' THEN now() ELSE NULL END,
			    claim_id = NULL, lease_until = NULL
			WHERE id = $1::uuid
			RETURNING `+mediaFields, id, next))
		return err
	})
	return record, err
}

func scanMedia(row pgx.Row) (Record, error) {
	var record Record
	err := row.Scan(&record.ID, &record.Title, &record.Description, &record.MediaType, &record.Status, &record.DurationSeconds, &record.CoverType)
	if record.CoverType != "" && record.Status == "published" {
		record.CoverURL = "/v1/covers/" + record.ID
	}
	return record, err
}
