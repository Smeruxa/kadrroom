package main

import (
    "encoding/json"
    "errors"
    "net/http"
    "net/http/httptest"
    "testing"

    "kadrroom/apps/catalog-api/internal/catalog"
)

type catalogResponse struct {
    Items []catalogItemResponse `json:"items"`
}

type catalogItemResponse struct {
    ID              string `json:"id"`
    Title           string `json:"title"`
    Description     string `json:"description"`
    DurationSeconds int    `json:"durationSeconds"`
    Available       *bool  `json:"available"`
}

type failingResponseWriter struct {
    *httptest.ResponseRecorder
}

func (failingResponseWriter) Write([]byte) (int, error) {
    return 0, errors.New("connection closed")
}

func TestCatalogServesDemoMetadata(t *testing.T) {
    films, err := catalog.Load()
    if err != nil {
        t.Fatal(err)
    }
    request := httptest.NewRequest(http.MethodGet, "/v1/catalog", nil)
    response := httptest.NewRecorder()
    routes(films).ServeHTTP(response, request)
    if response.Code != http.StatusOK {
        t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
    }
    var body catalogResponse
    if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
        t.Fatal(err)
    }
    if len(body.Items) != 2 {
        t.Fatalf("catalog contains %d items, want 2", len(body.Items))
    }
    for _, item := range body.Items {
        if item.ID == "" || item.Title == "" || item.Description == "" || item.DurationSeconds <= 0 {
            t.Errorf("incomplete catalog item: %+v", item)
        }
        if item.Available == nil || *item.Available {
            t.Errorf("demo item %q must explicitly report available=false", item.ID)
        }
    }
}

func TestHealth(t *testing.T) {
    response := httptest.NewRecorder()
    routes(catalog.Catalog{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

    if response.Code != http.StatusOK {
        t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
    }
    if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
        t.Fatalf("content type = %q, want JSON", contentType)
    }
    if body := response.Body.String(); body != "{\"status\":\"ok\"}\n" {
        t.Fatalf("body = %q, want health status", body)
    }
}

func TestCatalogRejectsUnsupportedRequests(t *testing.T) {
    handler := routes(catalog.Catalog{})
    unknownPath := httptest.NewRecorder()
    handler.ServeHTTP(unknownPath, httptest.NewRequest(http.MethodGet, "/unknown", nil))
    if unknownPath.Code != http.StatusNotFound {
        t.Errorf("unknown path status = %d, want %d", unknownPath.Code, http.StatusNotFound)
    }

    unsupportedMethod := httptest.NewRecorder()
    handler.ServeHTTP(unsupportedMethod, httptest.NewRequest(http.MethodPost, "/v1/catalog", nil))
    if unsupportedMethod.Code != http.StatusMethodNotAllowed {
        t.Errorf("unsupported method status = %d, want %d", unsupportedMethod.Code, http.StatusMethodNotAllowed)
    }
}

func TestCatalogHandlesDisconnectedClient(t *testing.T) {
    writer := failingResponseWriter{httptest.NewRecorder()}
    handler := routes(catalog.Catalog{Items: []catalog.Item{}})
    handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/v1/catalog", nil))

    response := httptest.NewRecorder()
    handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/catalog", nil))
    if body := response.Body.String(); body != "{\"items\":[]}\n" {
        t.Fatalf("body after a disconnected client = %q, want an empty catalog", body)
    }
}
