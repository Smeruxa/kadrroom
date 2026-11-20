package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"kadrroom/internal/catalogmedia"
)

type adminAPI struct {
	store      catalogmedia.Store
	mediaDir   string
	identity   *url.URL
	origin     string
	httpClient *http.Client
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("admin API stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	admin, err := openAdmin(startup)
	cancel()
	if err != nil {
		return err
	}
	defer admin.store.Pool.Close()
	address := os.Getenv("KADRROOM_ADMIN_ADDR")
	if address == "" {
		address = "127.0.0.1:8083"
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{
		Handler:           admin.routes(ctx),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       3 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	slog.Info("admin API listening", "address", listener.Addr().String())
	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdown); err != nil {
		return errors.Join(err, server.Close())
	}
	if err := <-stopped; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func openAdmin(ctx context.Context) (*adminAPI, error) {
	databaseURL := os.Getenv("KADRROOM_ADMIN_DATABASE_URL")
	if databaseURL == "" {
		return nil, errors.New("KADRROOM_ADMIN_DATABASE_URL is required")
	}
	identity, err := catalogmedia.ParseOrigin(os.Getenv("KADRROOM_IDENTITY_URL"), "KADRROOM_IDENTITY_URL")
	if err != nil {
		return nil, err
	}
	adminOrigin, err := catalogmedia.ParseOrigin(os.Getenv("KADRROOM_ADMIN_PUBLIC_URL"), "KADRROOM_ADMIN_PUBLIC_URL")
	if err != nil {
		return nil, err
	}
	directory := os.Getenv("KADRROOM_CATALOG_MEDIA_DIR")
	if !filepath.IsAbs(directory) {
		return nil, errors.New("KADRROOM_CATALOG_MEDIA_DIR must be an absolute path")
	}
	if err := os.MkdirAll(directory, 0750); err != nil {
		return nil, fmt.Errorf("create catalog media directory: %w", err)
	}
	store, err := catalogmedia.Open(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	grants := catalogmedia.Grants{
		Reader: os.Getenv("KADRROOM_CATALOG_READER_ROLE"),
		Worker: os.Getenv("KADRROOM_CATALOG_WORKER_ROLE"),
	}
	if err := store.Migrate(ctx, grants); err != nil {
		store.Pool.Close()
		return nil, fmt.Errorf("initialize catalog media tables: %w", err)
	}
	return &adminAPI{
		store:    store,
		mediaDir: directory,
		identity: identity.ResolveReference(&url.URL{Path: "/v1/auth/session"}),
		origin:   adminOrigin.Scheme + "://" + adminOrigin.Host,
		httpClient: &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

type serviceStatus struct {
	Status string `json:"status"`
}

func (a *adminAPI) routes(ctx context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, serviceStatus{Status: "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		probe, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if ctx.Err() != nil || a.store.ReadyAdmin(probe) != nil {
			writeJSON(w, http.StatusServiceUnavailable, serviceStatus{Status: "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, serviceStatus{Status: "ok"})
	})
	a.addRoutes(mux)
	return mux
}
