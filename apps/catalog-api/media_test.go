package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kadrroom/apps/catalog-api/internal/catalog"
	"kadrroom/internal/catalogmedia"
	"kadrroom/internal/catalogmedia/testdb"
)

func TestCatalogOnlyServesPublishedMediaWithPostgres(t *testing.T) {
	s := testdb.Open(t)
	publicURL, err := url.Parse("https://kadrroom.example")
	if err != nil {
		t.Fatal(err)
	}
	published := &publishedCatalog{
		store:         s,
		mediaDir:      t.TempDir(),
		publicURL:     publicURL,
		internalToken: strings.Repeat("s", 32),
		signingKey:    bytes.Repeat([]byte("k"), 32),
	}
	demos, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	handler := catalogRoutes(t.Context(), demos, published)
	api := func(method, path string) *httptest.ResponseRecorder {
		return hitCatalog(handler, httptest.NewRequest(method, path, nil))
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		expectCatalogStatus(t, api(method, "/v1/admin/catalog"), 404)
	}
	expectCatalogStatus(t, api("GET", "/readyz"), 200)
	id, err := catalogmedia.NewID()
	if err != nil {
		t.Fatal(err)
	}
	draft := catalogmedia.Record{ID: id, Title: "Проверка публикации", Description: "Описание", MediaType: "video", CoverType: "image/png"}
	if err := s.Insert(t.Context(), draft); err != nil {
		t.Fatal(err)
	}
	publicPath := "/v1/covers/" + draft.ID
	expectCatalogStatus(t, api("GET", publicPath), 404)
	var public catalog.Catalog
	demoList := api("GET", "/v1/catalog")
	expectCatalogStatus(t, demoList, 200)
	if err := json.Unmarshal(demoList.Body.Bytes(), &public); err != nil || len(public.Items) != len(demos.Items) {
		t.Fatal("draft leaked into the public catalog")
	}

	finalContent := []byte("1234567890")
	if err := os.WriteFile(filepath.Join(published.mediaDir, draft.ID+".mp4"), finalContent, 0600); err != nil {
		t.Fatal(err)
	}
	cover := []byte("cover")
	if err := os.WriteFile(filepath.Join(published.mediaDir, draft.ID+catalogmedia.CoverSuffix), cover, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(t.Context(), "UPDATE catalog_media SET status='ready', duration_seconds=42 WHERE id=$1::uuid", draft.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(t.Context(), draft.ID, "publish"); err != nil {
		t.Fatal(err)
	}
	publishedCover := api("GET", publicPath)
	expectCatalogStatus(t, publishedCover, 200)
	if !bytes.Equal(publishedCover.Body.Bytes(), cover) || publishedCover.Header().Get("Content-Type") != "image/png" {
		t.Fatal("public cover was not returned with its configured image type")
	}
	publishedList := api("GET", "/v1/catalog")
	expectCatalogStatus(t, publishedList, 200)
	if err := json.Unmarshal(publishedList.Body.Bytes(), &public); err != nil || len(public.Items) != len(demos.Items)+1 {
		t.Fatal("published upload is absent from the public catalog")
	}
	added := public.Items[len(public.Items)-1]
	if added.ID != draft.ID || !added.Available || added.DurationSeconds != 42 || added.CoverURL != publicPath {
		t.Fatal("published catalog metadata is incorrect")
	}

	internal := "/_internal/catalog/" + draft.ID + "/playback"
	expectCatalogStatus(t, api("GET", internal), 401)
	signing := httptest.NewRequest("GET", internal, nil)
	signing.Header.Set("Authorization", "Bearer "+published.internalToken)
	linkResponse := hitCatalog(handler, signing)
	expectCatalogStatus(t, linkResponse, 200)
	var sourceLink playbackLink
	if err := json.Unmarshal(linkResponse.Body.Bytes(), &sourceLink); err != nil {
		t.Fatal(err)
	}
	if sourceLink.MediaType != "video" || !strings.HasPrefix(sourceLink.URL, "https://kadrroom.example/v1/media/") || !sourceLink.ExpiresAt.After(time.Now()) {
		t.Fatal("playback link missing public origin, type, or expiry")
	}
	ranged := httptest.NewRequest("GET", sourceLink.URL, nil)
	ranged.Header.Set("Range", "bytes=2-4")
	rangedResponse := hitCatalog(handler, ranged)
	expectCatalogStatus(t, rangedResponse, 206)
	if rangedResponse.Body.String() != "345" || rangedResponse.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Fatal("HTTP Range did not select the expected bytes")
	}
	expectCatalogStatus(t, api("GET", "/v1/media/"+draft.ID), 403)
	if _, err := s.Transition(t.Context(), draft.ID, "unpublish"); err != nil {
		t.Fatal(err)
	}
	expectCatalogStatus(t, api("GET", publicPath), 404)
	expectCatalogStatus(t, api("GET", sourceLink.URL), 404)
}
func hitCatalog(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func expectCatalogStatus(t *testing.T, response *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if response.Code != expected {
		t.Fatalf("status %d; want %d: %s", response.Code, expected, response.Body.String())
	}
}
