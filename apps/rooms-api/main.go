package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		slog.Error("Rooms API stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(startup, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("open rooms database: %w", err)
	}
	defer pool.Close()
	repo := store{pool: pool}
	if err := repo.migrate(startup); err != nil {
		return fmt.Errorf("migrate rooms database: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}
	service := newAPI(ctx, cfg, repo)
	if cfg.rtc.internalURL != "" {
		go service.cleanupRTC()
	}
	server := &http.Server{
		Handler:           service.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	slog.Info("Rooms API listening", "address", listener.Addr().String())
	select {
	case err := <-result:
		return fmt.Errorf("serve rooms API: %w", err)
	case <-ctx.Done():
	}
	shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := server.Shutdown(shutdown); err != nil {
		return fmt.Errorf("shut down rooms API: %w", errors.Join(err, server.Close()))
	}
	if err := <-result; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve rooms API: %w", err)
	}
	return nil
}
