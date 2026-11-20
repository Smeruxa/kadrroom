package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

type config struct {
	addr          string
	databaseURL   string
	identityURL   string
	catalogURL    string
	catalogToken  string
	publicOrigin  string
	secureCookies bool
	mediaURLs     map[string]string
	rtc           rtcConfig
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		addr:          getenv("KADRROOM_ROOMS_ADDR"),
		databaseURL:   getenv("KADRROOM_ROOMS_DATABASE_URL"),
		identityURL:   getenv("KADRROOM_IDENTITY_URL"),
		catalogURL:    getenv("KADRROOM_CATALOG_URL"),
		catalogToken:  getenv("KADRROOM_CATALOG_INTERNAL_TOKEN"),
		secureCookies: true,
		mediaURLs:     make(map[string]string),
	}
	if cfg.addr == "" {
		cfg.addr = "127.0.0.1:8082"
	}
	if cfg.databaseURL == "" {
		return config{}, errors.New("KADRROOM_ROOMS_DATABASE_URL is required")
	}
	var err error
	cfg.identityURL, err = upstreamBase(cfg.identityURL, "http://127.0.0.1:8081")
	if err != nil {
		return config{}, fmt.Errorf("KADRROOM_IDENTITY_URL: %w", err)
	}
	cfg.catalogURL, err = upstreamBase(cfg.catalogURL, "http://127.0.0.1:8080")
	if err != nil {
		return config{}, fmt.Errorf("KADRROOM_CATALOG_URL: %w", err)
	}
	if raw := getenv("KADRROOM_SECURE_COOKIES"); raw != "" {
		cfg.secureCookies, err = strconv.ParseBool(raw)
		if err != nil {
			return config{}, fmt.Errorf("KADRROOM_SECURE_COOKIES: %w", err)
		}
	}
	publicURL := getenv("KADRROOM_PUBLIC_URL")
	if publicURL == "" {
		publicURL = "https://localhost:5173"
		if !cfg.secureCookies {
			publicURL = "http://127.0.0.1:5173"
		}
	}
	cfg.publicOrigin, err = publicOrigin(publicURL, cfg.secureCookies)
	if err != nil {
		return config{}, err
	}
	cfg.rtc, err = loadRTCConfig(getenv, cfg.secureCookies)
	if err != nil {
		return config{}, err
	}
	if raw := getenv("KADRROOM_DEMO_MEDIA_URLS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.mediaURLs); err != nil {
			return config{}, fmt.Errorf("KADRROOM_DEMO_MEDIA_URLS: %w", err)
		}
		for id, value := range cfg.mediaURLs {
			if id == "" || !validMediaURL(value) {
				return config{}, fmt.Errorf("KADRROOM_DEMO_MEDIA_URLS: invalid entry for %q", id)
			}
		}
	}
	return cfg, nil
}

func publicOrigin(raw string, secure bool) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !validHTTPURL(parsed) || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || (secure && parsed.Scheme != "https") {
		return "", errors.New("KADRROOM_PUBLIC_URL must be the public origin and use HTTPS with secure cookies")
	}
	host := strings.ToLower(parsed.Host)
	if (parsed.Scheme == "https" && parsed.Port() == "443") || (parsed.Scheme == "http" && parsed.Port() == "80") {
		host = strings.ToLower(parsed.Hostname())
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return parsed.Scheme + "://" + host, nil
}

func upstreamBase(raw, fallback string) (string, error) {
	if raw == "" {
		raw = fallback
	}
	parsed, err := url.Parse(raw)
	if err != nil || !validHTTPURL(parsed) || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("expected HTTP or HTTPS URL")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validHTTPURL(parsed *url.URL) bool {
	return parsed != nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil
}

func validMediaURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || strings.Contains(raw, "\\") {
		return false
	}
	if validHTTPURL(parsed) {
		return true
	}
	return strings.HasPrefix(raw, "/") && !strings.HasPrefix(parsed.Path, "//") && parsed.Host == "" && !parsed.IsAbs()
}

func mediaKind(kind, rawURL string) string {
	if kind == "audio" || kind == "video" {
		return kind
	}
	parsed, _ := url.Parse(rawURL)
	switch strings.ToLower(path.Ext(parsed.Path)) {
	case ".aac", ".flac", ".m4a", ".mp3", ".ogg", ".opus", ".wav":
		return "audio"
	default:
		return "video"
	}
}
