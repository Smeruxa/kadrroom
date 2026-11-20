package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"kadrroom/internal/catalogmedia/testdb"
)

func TestProgressDuration(t *testing.T) {
	if elapsedSeconds("01:02:03.5") != 3723.5 || elapsedSeconds("03:00:Inf") != 0 || elapsedSeconds("N/A") != 0 {
		t.Fatal("invalid ffmpeg progress parsing")
	}
	cancelled := false
	if _, err := readProgress(strings.NewReader("progress=continue\nout_time=05:00:00.00000\n"), func() {
		cancelled = true
	}); err == nil || !cancelled {
		t.Fatal("job beyond the duration limit was not stopped")
	}
}

func TestConvertVideoAndAudio(t *testing.T) {
	binary := testFFmpeg(t)
	for _, fixture := range []struct {
		kind string
		path string
	}{
		{kind: "video", path: "last-tram.mp4"},
		{kind: "audio", path: "blue-light.m4a"},
	} {
		t.Run(fixture.kind, func(t *testing.T) {
			w := worker{dir: t.TempDir(), ffmpeg: binary}
			job := mediaJob{id: "fixture", mediaType: fixture.kind}
			input, err := os.ReadFile(filepath.Join("..", "frontend", "public", "demo-media", fixture.path))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(w.dir, job.id+".input"), input, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			path, duration, err := w.transcode(ctx, job)
			if err != nil {
				t.Fatal(err)
			}
			output, err := os.ReadFile(path)
			if err != nil || len(output) < 8 || !bytes.Equal(output[4:8], []byte("ftyp")) || duration <= 0 {
				t.Fatalf("expected a playable MP4 family file: %v", err)
			}
			if err := os.WriteFile(filepath.Join(w.dir, job.id+".input"), []byte("invalid file"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := w.transcode(ctx, job); err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
}

func TestWorkerClaimsAndCompletesCatalogJobsWithPostgres(t *testing.T) {
	pool := testdb.Open(t).Pool
	w := worker{pool: pool, dir: t.TempDir(), ffmpeg: testFFmpeg(t)}
	input, err := os.ReadFile(filepath.Join("..", "frontend", "public", "demo-media", "blue-light.m4a"))
	if err != nil {
		t.Fatal(err)
	}

	id := insertWorkerInput(t, w, input)
	job, err := w.next(t.Context())
	if err != nil || job.id != id || job.claim == "" {
		t.Fatalf("worker did not claim submitted job: %v", err)
	}
	if _, err := w.next(t.Context()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("active claim was not exclusive: %v", err)
	}
	interrupted, cancel := context.WithCancel(t.Context())
	cancel()
	if err := w.process(interrupted, job); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := w.next(t.Context())
	if err != nil || reclaimed.id != id || reclaimed.claim == job.claim {
		t.Fatalf("interruption did not put job back in the queue: %v", err)
	}
	job = reclaimed
	if err := w.process(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, id+".m4a")); err != nil {
		t.Fatal(err)
	}
	var status string
	var duration int
	if err := pool.QueryRow(t.Context(), "SELECT status, duration_seconds FROM catalog_media WHERE id=$1::uuid", id).
		Scan(&status, &duration); err != nil || status != "ready" || duration <= 0 {
		t.Fatalf("job did not become playable: %s %d %v", status, duration, err)
	}

	id = insertWorkerInput(t, w, []byte("unplayable media"))
	job, err = w.next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.process(t.Context(), job); err == nil {
		t.Fatal("invalid upload unexpectedly finalized")
	}
	if err := pool.QueryRow(t.Context(), "SELECT status FROM catalog_media WHERE id=$1::uuid", id).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("failed job did not enter retryable state: %v", err)
	}
}

func testFFmpeg(t *testing.T) string {
	t.Helper()
	ffmpeg := os.Getenv("KADRROOM_FFMPEG_BIN")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	binary, err := exec.LookPath(ffmpeg)
	if err != nil {
		t.Skip("ffmpeg is required for the conversion test")
	}
	return binary
}

func insertWorkerInput(t *testing.T, w worker, content []byte) string {
	t.Helper()
	var id string
	if err := w.pool.QueryRow(t.Context(), `
		INSERT INTO catalog_media (title, description, media_type, status)
		VALUES ('Title', 'Description', 'audio', 'processing') RETURNING id::text
	`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.dir, id+".input"), content, 0600); err != nil {
		t.Fatal(err)
	}
	return id
}
