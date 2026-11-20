package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kadrroom/internal/catalogmedia"
)

const playbackTTL = 10 * time.Minute

type catalogFailure struct {
	Code string `json:"code"`
}

type playbackLink struct {
	URL       string    `json:"url"`
	MediaType string    `json:"mediaType"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (c *publishedCatalog) addRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /_internal/catalog/{id}/playback", c.preparePlayback)
	mux.HandleFunc("GET /v1/media/{id}", c.playback)
	mux.HandleFunc("GET /v1/covers/{id}", c.cover)
}

func (c *publishedCatalog) preparePlayback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	authToken, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !bearer || !hmac.Equal([]byte(authToken), []byte(c.internalToken)) {
		catalogError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	record, ok := c.publishedRecord(w, r)
	if !ok {
		return
	}
	expires := time.Now().UTC().Add(playbackTTL).Truncate(time.Second)
	deadline := strconv.FormatInt(expires.Unix(), 10)
	query := url.Values{"expires": {deadline}, "signature": {c.signature(record.ID, deadline)}}
	target := c.publicURL.ResolveReference(&url.URL{Path: "/v1/media/" + record.ID, RawQuery: query.Encode()})
	writeJSON(w, http.StatusOK, playbackLink{URL: target.String(), MediaType: record.MediaType, ExpiresAt: expires})
}

func (c *publishedCatalog) playback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	id := r.PathValue("id")
	if !catalogmedia.ValidID(id) {
		catalogError(w, http.StatusNotFound, "not_found")
		return
	}
	query := r.URL.Query()
	deadline := query.Get("expires")
	expires, err := strconv.ParseInt(deadline, 10, 64)
	if err != nil || expires <= time.Now().Unix() ||
		!hmac.Equal([]byte(query.Get("signature")), []byte(c.signature(id, deadline))) {
		catalogError(w, http.StatusForbidden, "invalid_media_link")
		return
	}
	record, ok := c.publishedRecord(w, r)
	if !ok {
		return
	}
	suffix, contentType := catalogmedia.PlaybackFormat(record.MediaType)
	c.serveMedia(w, r, record.ID+suffix, contentType)
}

func (c *publishedCatalog) cover(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	record, ok := c.publishedRecord(w, r)
	if !ok {
		return
	}
	if record.CoverType == "" {
		catalogError(w, http.StatusNotFound, "not_found")
		return
	}
	c.serveMedia(w, r, record.ID+catalogmedia.CoverSuffix, record.CoverType)
}

func (c *publishedCatalog) publishedRecord(w http.ResponseWriter, r *http.Request) (catalogmedia.Record, bool) {
	id := r.PathValue("id")
	if !catalogmedia.ValidID(id) {
		catalogError(w, http.StatusNotFound, "not_found")
		return catalogmedia.Record{}, false
	}
	record, err := c.store.Published(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		catalogError(w, http.StatusNotFound, "not_found")
		return catalogmedia.Record{}, false
	}
	if err != nil {
		catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
		return catalogmedia.Record{}, false
	}
	return record, true
}

func (c *publishedCatalog) serveMedia(w http.ResponseWriter, r *http.Request, name, contentType string) {
	file, err := os.Open(filepath.Join(c.mediaDir, name))
	if err != nil {
		catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		catalogError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (c *publishedCatalog) signature(id, deadline string) string {
	digest := hmac.New(sha256.New, c.signingKey)
	_, _ = digest.Write([]byte("kadrroom-playback-v1:" + id + ":" + deadline))
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func catalogError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, status, catalogFailure{Code: code})
}
