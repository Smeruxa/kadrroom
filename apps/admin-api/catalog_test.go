package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kadrroom/internal/catalogmedia"
	"kadrroom/internal/catalogmedia/testdb"
)

func TestBoundedStagingAndCanonicalMediaIDs(t *testing.T) {
	dir := t.TempDir()
	if _, err := stageFile(dir, strings.NewReader("12345"), 4); err != errUploadLarge {
		t.Fatalf("stageFile oversized error = %v", err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("rejected upload left a file")
	}
	id, err := catalogmedia.NewID()
	if err != nil || !catalogmedia.ValidID(id) {
		t.Fatalf("invalid generated ID: %v", err)
	}
	if catalogmedia.ValidID("../../../private") || catalogmedia.ValidID(strings.Repeat("a", 36)) || catalogmedia.ValidID(strings.Repeat("!", 36)) {
		t.Fatal("unsafe ID accepted")
	}
}

func TestOperatorUploadAndTransitionsWithPostgres(t *testing.T) {
	store := testdb.Open(t)
	identityURL, err := url.Parse("http://identity.example/v1/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	admin := &adminAPI{
		store:      store,
		mediaDir:   t.TempDir(),
		identity:   identityURL,
		origin:     "https://admin.example",
		httpClient: &http.Client{Transport: testIdentityTransport{}},
	}
	handler := admin.routes(t.Context())
	api := func(method, path, cookie string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		if cookie != "" {
			request.AddCookie(&http.Cookie{Name: "kr_account", Value: cookie})
		}
		return hitCatalog(handler, request)
	}
	expectCatalogStatus(t, api("GET", "/healthz", ""), 200)
	expectCatalogStatus(t, api("GET", "/readyz", ""), 200)
	expectCatalogStatus(t, api("GET", "/v1/catalog", ""), 404)
	expectCatalogStatus(t, api("GET", "/v1/admin/catalog", ""), 401)
	expectCatalogStatus(t, api("GET", "/v1/admin/catalog", "user"), 403)
	expectCatalogStatus(t, api("GET", "/v1/admin/catalog", "operator"), 200)

	const input = "example uploaded media, representation verified by worker tests"
	var cover bytes.Buffer
	source := image.NewRGBA(image.Rect(0, 0, 2, 2))
	source.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&cover, source); err != nil {
		t.Fatal(err)
	}
	crossOrigin := draftRequest(t, input, cover.Bytes())
	crossOrigin.Header.Set("Origin", "https://viewer.example")
	expectCatalogStatus(t, hitCatalog(handler, crossOrigin), 403)
	badCover := draftRequest(t, input, []byte("not an image"))
	expectCatalogStatus(t, hitCatalog(handler, badCover), 400)
	request := draftRequest(t, input, cover.Bytes())
	request.Header.Set("Origin", admin.origin)
	created := hitCatalog(handler, request)
	expectCatalogStatus(t, created, 201)
	var draft catalogmedia.Record
	if err := json.Unmarshal(created.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	if !catalogmedia.ValidID(draft.ID) || draft.Status != "processing" || draft.MediaType != "video" || draft.CoverURL != "" {
		t.Fatal("draft response did not match the processing contract")
	}
	dirFile, err := os.ReadFile(filepath.Join(admin.mediaDir, draft.ID+catalogmedia.SourceSuffix))
	if err != nil || string(dirFile) != input {
		t.Fatalf("persisted input mismatch: %v", err)
	}
	adminPath := "/v1/admin/catalog/" + draft.ID
	publish := adminPath + "/publish"
	expectCatalogStatus(t, api("POST", publish, "operator"), 409)
	if _, err := store.Pool.Exec(t.Context(), "UPDATE catalog_media SET status='ready', duration_seconds=42 WHERE id=$1::uuid", draft.ID); err != nil {
		t.Fatal(err)
	}
	expectCatalogStatus(t, api("POST", publish, "operator"), 200)
	expectCatalogStatus(t, api("POST", publish, "operator"), 200)
	expectCatalogStatus(t, api("POST", adminPath+"/unpublish", "operator"), 200)
	if _, err := store.Pool.Exec(t.Context(), "UPDATE catalog_media SET status='failed' WHERE id=$1::uuid", draft.ID); err != nil {
		t.Fatal(err)
	}
	retry := api("POST", adminPath+"/retry", "operator")
	expectCatalogStatus(t, retry, 200)
	if err := json.Unmarshal(retry.Body.Bytes(), &draft); err != nil || draft.Status != "processing" {
		t.Fatal("retry did not requeue conversion")
	}
}

func TestRejectedDraftCleansStagedFilesWithPostgres(t *testing.T) {
	admin := &adminAPI{store: testdb.Open(t), mediaDir: t.TempDir()}
	id, err := catalogmedia.NewID()
	if err != nil {
		t.Fatal(err)
	}
	input, err := stageFile(admin.mediaDir, strings.NewReader("source media"), 20)
	if err != nil {
		t.Fatal(err)
	}
	cover, err := stageFile(admin.mediaDir, strings.NewReader("cover"), 20)
	if err != nil {
		t.Fatal(err)
	}
	draft := uploadDraft{
		record:   catalogmedia.Record{ID: id, Title: "Invalid record", MediaType: "unsupported"},
		filePath: input,
		cover:    cover,
	}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/admin/catalog", nil)
	if err := admin.saveDraft(request, &draft); err == nil {
		t.Fatal("database accepted an invalid draft")
	}
	draft.removeTemporaryFiles()
	files, err := os.ReadDir(admin.mediaDir)
	if err != nil || len(files) != 0 {
		t.Fatalf("definitive database rejection left staged files: %v", err)
	}
}

type testIdentityTransport struct{}

func (testIdentityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cookie, err := request.Cookie("kr_account")
	status, payload := 401, "{}"
	if err == nil && cookie.Value == "operator" {
		status, payload = 200, `{"id":"test-operator","role":"operator"}`
	}
	if err == nil && cookie.Value == "user" {
		status, payload = 200, `{"id":"test-user","role":"user"}`
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(payload))}, nil
}

func draftRequest(t *testing.T, content string, cover []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{"title": "Тестовый фильм", "description": "Описание", "mediaType": "video"} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", "test.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, content); err != nil {
		t.Fatal(err)
	}
	part, err = writer.CreateFormFile("cover", "cover.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(cover); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/v1/admin/catalog", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: "kr_account", Value: "operator"})
	return request
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
