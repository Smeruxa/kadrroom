package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"kadrroom/internal/catalogmedia"
)

type publishedCatalog struct {
	store         catalogmedia.Store
	mediaDir      string
	publicURL     *url.URL
	internalToken string
	signingKey    []byte
}

func openPublishedCatalog(ctx context.Context) (*publishedCatalog, error) {
	databaseURL := os.Getenv("KADRROOM_CATALOG_DATABASE_URL")
	if databaseURL == "" {
		return nil, nil
	}
	publicURL, err := catalogmedia.ParseOrigin(os.Getenv("KADRROOM_PUBLIC_URL"), "KADRROOM_PUBLIC_URL")
	if err != nil {
		return nil, err
	}
	directory := os.Getenv("KADRROOM_CATALOG_MEDIA_DIR")
	if !filepath.IsAbs(directory) {
		return nil, errors.New("KADRROOM_CATALOG_MEDIA_DIR must be an absolute path")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("KADRROOM_CATALOG_MEDIA_DIR must be a readable directory")
	}
	internalToken := os.Getenv("KADRROOM_CATALOG_INTERNAL_TOKEN")
	signingKey := os.Getenv("KADRROOM_CATALOG_SIGNING_KEY")
	if signingKey == "" {
		signingKey = internalToken
	}
	if len(internalToken) < 32 || len(signingKey) < 32 {
		return nil, errors.New("catalog internal token and signing key must each contain at least 32 bytes")
	}
	store, err := catalogmedia.Open(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	return &publishedCatalog{
		store:         store,
		mediaDir:      directory,
		publicURL:     publicURL,
		internalToken: internalToken,
		signingKey:    []byte(signingKey),
	}, nil
}
