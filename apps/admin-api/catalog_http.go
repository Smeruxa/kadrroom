package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"kadrroom/internal/catalogmedia"
)

type catalogFailure struct {
	Code string `json:"code"`
}

type mediaList struct {
	Items []catalogmedia.Record `json:"items"`
}

type identitySession struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

func (c *adminAPI) addRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/catalog", c.requireOperator(c.adminList))
	mux.HandleFunc("POST /v1/admin/catalog", c.requireOperator(c.createDraft))
	mux.HandleFunc("POST /v1/admin/catalog/{id}/publish", c.requireOperator(c.transition("publish")))
	mux.HandleFunc("POST /v1/admin/catalog/{id}/unpublish", c.requireOperator(c.transition("unpublish")))
	mux.HandleFunc("POST /v1/admin/catalog/{id}/retry", c.requireOperator(c.transition("retry")))
}

func (c *adminAPI) requireOperator(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodPost {
			origin := r.Header.Get("Origin")
			expected := c.origin
			if (origin != "" && origin != expected) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				catalogError(w, http.StatusForbidden, "invalid_origin")
				return
			}
		}
		cookie, err := r.Cookie("kr_account")
		if err != nil || cookie.Value == "" {
			catalogError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.identity.String(), nil)
		if err != nil {
			catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
			return
		}
		request.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
		response, err := c.httpClient.Do(request)
		if err != nil {
			catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
			return
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode == http.StatusUnauthorized {
			catalogError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		var session identitySession
		if response.StatusCode != http.StatusOK ||
			json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&session) != nil || session.ID == "" {
			catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
			return
		}
		if session.Role != "operator" {
			catalogError(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r)
	}
}

func (c *adminAPI) adminList(w http.ResponseWriter, r *http.Request) {
	records, err := c.store.ListAll(r.Context())
	if err != nil {
		catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, mediaList{Items: records})
}

func (c *adminAPI) transition(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !catalogmedia.ValidID(id) {
			catalogError(w, http.StatusNotFound, "not_found")
			return
		}
		record, err := c.store.Transition(r.Context(), id, action)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			catalogError(w, http.StatusNotFound, "not_found")
		case errors.Is(err, catalogmedia.ErrMediaNotReady):
			catalogError(w, http.StatusConflict, "media_not_ready")
		case err != nil:
			catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
		default:
			writeJSON(w, http.StatusOK, record)
		}
	}
}

func catalogError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, catalogFailure{Code: code})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Debug("write admin response", "error", err)
	}
}
