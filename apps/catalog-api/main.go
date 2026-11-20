package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kadrroom/apps/catalog-api/internal/catalog"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("KadrRoom API stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	films, err := catalog.Load()
	if err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}

	startup, cancelStartup := context.WithTimeout(ctx, 20*time.Second)
	published, err := openPublishedCatalog(startup)
	cancelStartup()
	if err != nil {
		return err
	}
	if published != nil {
		defer published.store.Pool.Close()
	}

	addr := os.Getenv("KADRROOM_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	server := &http.Server{
		Handler:           catalogRoutes(ctx, films, published),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if published != nil {
		server.WriteTimeout = 5 * time.Minute
	}

	slog.Info("KadrRoom API listening", "address", listener.Addr().String())
	return serve(ctx, server, listener)
}

func serve(ctx context.Context, server *http.Server, listener net.Listener) error {
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	select {
	case err := <-serveResult:
		return fmt.Errorf("serve API: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down API: %w", errors.Join(err, server.Close()))
	}
	if err := <-serveResult; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve API: %w", err)
	}
	return nil
}

func routes(ctx context.Context, films catalog.Catalog) http.Handler {
	return catalogRoutes(ctx, films, nil)
}

func catalogRoutes(ctx context.Context, films catalog.Catalog, published *publishedCatalog) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if ctx.Err() != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "shutting_down"})
			return
		}
		if published != nil {
			probe, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if published.store.ReadyPublic(probe) != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		if published == nil {
			writeJSON(w, http.StatusOK, films)
			return
		}
		rows, err := published.store.ListPublished(r.Context())
		if err != nil {
			catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
			return
		}
		items := append(make([]catalog.Item, 0, len(films.Items)+len(rows)), films.Items...)
		for _, record := range rows {
			items = append(items, catalog.Item{
				ID:              record.ID,
				Title:           record.Title,
				Description:     record.Description,
				DurationSeconds: record.DurationSeconds,
				Available:       true,
				MediaType:       record.MediaType,
				CoverURL:        record.CoverURL,
			})
		}
		writeJSON(w, http.StatusOK, catalog.Catalog{Items: items})
	})
	if published != nil {
		published.addRoutes(mux)
	}
	return mux
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Debug("write response", "error", err)
	}
}
