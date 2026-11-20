package testdb

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kadrroom/internal/catalogmedia"
)

func Open(t testing.TB) catalogmedia.Store {
	t.Helper()
	databaseURL := os.Getenv("KADRROOM_CATALOG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("KADRROOM_CATALOG_TEST_DATABASE_URL is required for the catalog integration test")
	}
	root, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	id, err := catalogmedia.NewID()
	if err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{"catalog_test_" + strings.ReplaceAll(id, "-", "")}.Sanitize()
	if _, err := root.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ", public"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := catalogmedia.Store{Pool: pool}
	if err := store.Migrate(t.Context(), catalogmedia.Grants{}); err != nil {
		t.Fatal(err)
	}
	return store
}
