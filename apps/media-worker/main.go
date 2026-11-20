package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kadrroom/internal/catalogmedia"
)

type worker struct {
	pool   *pgxpool.Pool
	dir    string
	ffmpeg string
}

type mediaJob struct {
	id        string
	mediaType string
	claim     string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("media worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	url := os.Getenv("KADRROOM_CATALOG_DATABASE_URL")
	dir := os.Getenv("KADRROOM_CATALOG_MEDIA_DIR")
	ffmpeg := os.Getenv("KADRROOM_FFMPEG_BIN")
	if url == "" || !filepath.IsAbs(dir) {
		return errors.New("KADRROOM_CATALOG_DATABASE_URL and absolute KADRROOM_CATALOG_MEDIA_DIR are required")
	}
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	binary, err := exec.LookPath(ffmpeg)
	if err != nil {
		return errors.New("ffmpeg executable not found")
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create media directory: %w", err)
	}
	store, err := catalogmedia.Open(ctx, url)
	if err != nil {
		return err
	}
	defer store.Pool.Close()
	return (worker{pool: store.Pool, dir: dir, ffmpeg: binary}).run(ctx)
}

func (w worker) run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		query, cancel := context.WithTimeout(ctx, 5*time.Second)
		job, err := w.next(query)
		cancel()
		if err == nil {
			if err := w.process(ctx, job); err != nil && ctx.Err() == nil {
				slog.Warn("process catalog media", "media_id", job.id, "error", err)
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
			slog.Warn("claim catalog media job", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}

func (w worker) next(ctx context.Context) (mediaJob, error) {
	var job mediaJob
	err := w.pool.QueryRow(ctx, `
		WITH pending AS (
			SELECT id FROM catalog_media
			WHERE status = 'processing' AND (lease_until IS NULL OR lease_until <= now())
			ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE catalog_media AS media
		SET claim_id = gen_random_uuid(), lease_until = now() + interval '2 minutes'
		FROM pending WHERE media.id = pending.id
		RETURNING media.id::text, media.media_type, media.claim_id::text
	`).Scan(&job.id, &job.mediaType, &job.claim)
	return job, err
}

func (w worker) process(ctx context.Context, job mediaJob) error {
	transcodeCtx, stop := context.WithTimeout(ctx, 45*time.Minute)
	defer stop()
	leaseDone := make(chan struct{})
	go func() {
		defer close(leaseDone)
		w.keepLease(transcodeCtx, stop, job)
	}()
	path, duration, processErr := w.transcode(transcodeCtx, job)
	interrupted := errors.Is(transcodeCtx.Err(), context.Canceled)
	stop()
	<-leaseDone
	if path != "" {
		defer func() { _ = os.Remove(path) }()
	}
	final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
	defer cancel()
	if interrupted || ctx.Err() != nil {
		return w.release(final, job, "processing")
	}
	if processErr != nil {
		return errors.Join(processErr, w.release(final, job, "failed"))
	}
	return w.finish(final, job, path, duration)
}

func (w worker) keepLease(ctx context.Context, stop context.CancelFunc, job mediaJob) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			query, cancel := context.WithTimeout(ctx, 5*time.Second)
			tag, err := w.pool.Exec(query, `
				UPDATE catalog_media SET lease_until = now() + interval '2 minutes'
				WHERE id = $1::uuid AND claim_id = $2::uuid AND status = 'processing' AND lease_until > now()
			`, job.id, job.claim)
			cancel()
			if err != nil || tag.RowsAffected() != 1 {
				stop()
				return
			}
		}
	}
}

func (w worker) release(ctx context.Context, job mediaJob, nextStatus string) error {
	_, err := w.pool.Exec(ctx, `
		UPDATE catalog_media SET status = $3, claim_id = NULL, lease_until = NULL
		WHERE id = $1::uuid AND claim_id = $2::uuid AND status = 'processing'
	`, job.id, job.claim, nextStatus)
	return err
}

func (w worker) finish(ctx context.Context, job mediaJob, temporary string, duration int) error {
	return pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM catalog_media
			WHERE id = $1::uuid AND claim_id = $2::uuid AND status = 'processing' AND lease_until > now()
			FOR UPDATE
		`, job.id, job.claim).Scan(&id); err != nil {
			return err
		}
		suffix, _ := catalogmedia.PlaybackFormat(job.mediaType)
		if err := os.Rename(temporary, filepath.Join(w.dir, id+suffix)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE catalog_media SET status = 'ready', duration_seconds = $2, claim_id = NULL, lease_until = NULL
			WHERE id = $1::uuid
		`, id, duration)
		return err
	})
}
