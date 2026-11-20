package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	MediaType       string `json:"mediaType"`
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
	routes(t.Context(), films).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var body catalogResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 3 {
		t.Fatalf("catalog contains %d items, want 3", len(body.Items))
	}
	for _, item := range body.Items {
		if item.ID == "" || item.Title == "" || item.Description == "" || item.DurationSeconds <= 0 {
			t.Errorf("incomplete catalog item: %+v", item)
		}
		if item.Available == nil || *item.Available != (item.ID != "lighthouse-window") {
			t.Errorf("demo item %q has incorrect availability", item.ID)
		}
		if item.MediaType != "video" && item.MediaType != "audio" {
			t.Errorf("demo item %q has wrong mediaType %q", item.ID, item.MediaType)
		}
	}
}

func TestHealth(t *testing.T) {
	response := httptest.NewRecorder()
	routes(t.Context(), catalog.Catalog{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

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

func TestReadinessReflectsShutdownWithoutFailingLiveness(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler := routes(ctx, catalog.Catalog{})
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || ready.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("ready response = %d %q", ready.Code, ready.Body.String())
	}

	cancel()
	stopping := httptest.NewRecorder()
	handler.ServeHTTP(stopping, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if stopping.Code != http.StatusServiceUnavailable || stopping.Body.String() != "{\"status\":\"shutting_down\"}\n" {
		t.Errorf("stopping response = %d %q", stopping.Code, stopping.Body.String())
	}
	healthy := httptest.NewRecorder()
	handler.ServeHTTP(healthy, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if healthy.Code != http.StatusOK {
		t.Errorf("liveness status during shutdown = %d, want %d", healthy.Code, http.StatusOK)
	}
}

func TestRunReportsListenFailure(t *testing.T) {
	t.Setenv("KADRROOM_ADDR", "127.0.0.1:65536")
	var addrError *net.AddrError
	if err := run(t.Context()); !errors.As(err, &addrError) {
		t.Fatalf("run error = %v, want invalid listen address", err)
	}
}

func TestShutdownWaitsForActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(deadline)
	defer cancel()

	requestStarted := make(chan struct{})
	finishRequest := make(chan struct{})
	shutdownStarted := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		select {
		case <-finishRequest:
			w.WriteHeader(http.StatusNoContent)
		case <-deadline.Done():
		}
	})}
	server.RegisterOnShutdown(func() { close(shutdownStarted) })
	serveResult := make(chan error, 1)
	go func() { serveResult <- serve(ctx, server, listener) }()

	request, err := http.NewRequestWithContext(deadline, http.MethodGet, "http://"+listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	responseResult := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			responseResult <- err
			return
		}
		err = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			err = errors.Join(err, fmt.Errorf("request status = %d, want %d", response.StatusCode, http.StatusNoContent))
		}
		responseResult <- err
	}()

	select {
	case <-requestStarted:
	case <-deadline.Done():
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case <-shutdownStarted:
	case <-deadline.Done():
		t.Fatal("shutdown did not start")
	}
	close(finishRequest)
	if err := <-responseResult; err != nil {
		t.Fatalf("active request failed during shutdown: %v", err)
	}
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("shutdown failed: %v", err)
		}
	case <-deadline.Done():
		t.Fatal("server did not stop")
	}
}

func TestCatalogRejectsUnsupportedRequests(t *testing.T) {
	handler := routes(t.Context(), catalog.Catalog{})
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
	handler := routes(t.Context(), catalog.Catalog{Items: []catalog.Item{}})
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/v1/catalog", nil))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/catalog", nil))
	if body := response.Body.String(); body != "{\"items\":[]}\n" {
		t.Fatalf("body after a disconnected client = %q, want an empty catalog", body)
	}
}
