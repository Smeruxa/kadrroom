package main

import (
    "encoding/json"
    "log/slog"
    "net/http"
    "os"
    "time"

    "kadrroom/apps/catalog-api/internal/catalog"
)

func main() {
    films, err := catalog.Load()
    if err != nil {
        slog.Error("load catalog", "error", err)
        os.Exit(1)
    }

    addr := os.Getenv("KADRROOM_ADDR")
    if addr == "" {
        addr = "127.0.0.1:8080"
    }
    server := &http.Server{
        Addr:              addr,
        Handler:           routes(films),
        ReadHeaderTimeout: 5 * time.Second,
    }

    slog.Info("KadrRoom API listening", "address", addr)
    if err := server.ListenAndServe(); err != nil {
        slog.Error("serve API", "error", err)
        os.Exit(1)
    }
}

func routes(films catalog.Catalog) http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
        writeJSON(w, map[string]string{"status": "ok"})
    })
    mux.HandleFunc("GET /v1/catalog", func(w http.ResponseWriter, _ *http.Request) {
        writeJSON(w, films)
    })
    return mux
}

func writeJSON(w http.ResponseWriter, payload any) {
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    if err := json.NewEncoder(w).Encode(payload); err != nil {
        slog.Debug("write response", "error", err)
    }
}
