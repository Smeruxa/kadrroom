package catalogmedia_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"kadrroom/internal/catalogmedia"
	"kadrroom/internal/catalogmedia/testdb"
)

func TestPublicationRolesWithPostgres(t *testing.T) {
	store := testdb.Open(t)
	ctx := t.Context()
	reader := testRole(t, store, "reader")
	worker := testRole(t, store, "worker")
	if err := store.Migrate(ctx, catalogmedia.Grants{Reader: reader, Worker: worker}); err != nil {
		t.Fatal(err)
	}
	published := testMedia(t, store)
	draft := testMedia(t, store)
	if _, err := store.Pool.Exec(ctx, "UPDATE catalog_media SET status='published' WHERE id=$1::uuid", published); err != nil {
		t.Fatal(err)
	}
	withRole(t, store, reader, func(tx pgx.Tx) {
		var total int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM catalog_published_media").Scan(&total); err != nil || total != 1 {
			t.Fatalf("public reader saw %d rows, err %v", total, err)
		}
		_, err := tx.Exec(ctx, "SELECT * FROM catalog_media")
		assertDenied(t, err)
	})
	withRole(t, store, reader, func(tx pgx.Tx) {
		_, err := tx.Exec(ctx, "UPDATE catalog_published_media SET title = 'changed' WHERE id=$1::uuid", published)
		assertDenied(t, err)
	})
	withRole(t, store, worker, func(tx pgx.Tx) {
		var rows int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM catalog_media WHERE id=$1::uuid", published).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("worker saw published media: %d, %v", rows, err)
		}
		tag, err := tx.Exec(ctx, "UPDATE catalog_media SET status='failed' WHERE id=$1::uuid", published)
		if err != nil || tag.RowsAffected() != 0 {
			t.Fatalf("worker changed published media: %v, %v", tag, err)
		}
		_, err = tx.Exec(ctx, "UPDATE catalog_media SET status='published' WHERE id=$1::uuid", draft)
		assertDenied(t, err)
	})
	withRole(t, store, worker, func(tx pgx.Tx) {
		var claimed string
		err := tx.QueryRow(ctx, `
			WITH pending AS (
				SELECT id FROM catalog_media
				WHERE status = 'processing' AND id = $1::uuid FOR UPDATE SKIP LOCKED
			)
			UPDATE catalog_media AS media
			SET claim_id = gen_random_uuid(), lease_until = now()+interval '2 minutes'
			FROM pending WHERE media.id = pending.id
			RETURNING media.claim_id::text
		`, draft).Scan(&claimed)
		if err != nil || claimed == "" {
			t.Fatalf("worker could not claim an eligible row: %v", err)
		}
		if _, err := tx.Exec(ctx, "UPDATE catalog_media SET status='ready', duration_seconds=10 WHERE id=$1::uuid", draft); err != nil {
			t.Fatal(err)
		}
		tag, err := tx.Exec(ctx, "UPDATE catalog_media SET status='failed' WHERE id=$1::uuid", draft)
		if err != nil || tag.RowsAffected() != 0 {
			t.Fatalf("worker changed a finalized row: %v, %v", tag, err)
		}
		_, err = tx.Exec(ctx, "UPDATE catalog_media SET title='changed' WHERE id=$1::uuid", draft)
		assertDenied(t, err)
	})
	if _, err := store.Pool.Exec(ctx, "UPDATE catalog_media SET status='ready', duration_seconds=10 WHERE id=$1::uuid", draft); err != nil {
		t.Fatal(err)
	}
	if record, err := store.Transition(ctx, draft, "publish"); err != nil || record.Status != "published" {
		t.Fatalf("owner lost ability to publish under row-level security: %v", err)
	}
	if record, err := store.Transition(ctx, draft, "unpublish"); err != nil || record.Status != "ready" {
		t.Fatalf("owner lost ability to unpublish under row-level security: %v", err)
	}
}

func testRole(t *testing.T, store catalogmedia.Store, label string) string {
	t.Helper()
	id, err := catalogmedia.NewID()
	if err != nil {
		t.Fatal(err)
	}
	role := "test_catalog_" + label + "_" + strings.ReplaceAll(id, "-", "")
	identifier := pgx.Identifier{role}.Sanitize()
	if _, err := store.Pool.Exec(t.Context(), "CREATE ROLE "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := store.Pool.Exec(ctx, "DROP OWNED BY "+identifier+"; DROP ROLE "+identifier); err != nil {
			t.Error(err)
		}
	})
	var schema string
	if err := store.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(t.Context(), "GRANT USAGE ON SCHEMA "+pgx.Identifier{schema}.Sanitize()+" TO "+identifier); err != nil {
		t.Fatal(err)
	}
	return role
}

func testMedia(t *testing.T, store catalogmedia.Store) string {
	t.Helper()
	id, err := catalogmedia.NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := catalogmedia.Record{ID: id, Title: "Media", MediaType: "audio"}
	if err := store.Insert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	return id
}

func withRole(t *testing.T, store catalogmedia.Store, role string, check func(pgx.Tx)) {
	t.Helper()
	ctx := t.Context()
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	check(tx)
}

func assertDenied(t *testing.T, err error) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "42501" {
		t.Fatalf("PostgreSQL did not deny access: %v", err)
	}
}
