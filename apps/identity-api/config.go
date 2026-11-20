package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type config struct {
	addr          string
	databaseURL   string
	publicURL     *url.URL
	adminURL      *url.URL
	secureCookies bool
	operatorEmail string
	proxyToken    string
	mailTokenKey  [32]byte
	mail          smtpMailer
}

func loadConfig() (config, error) {
	cfg := config{
		addr:          envOr("KADRROOM_IDENTITY_ADDR", "127.0.0.1:8081"),
		databaseURL:   os.Getenv("KADRROOM_IDENTITY_DATABASE_URL"),
		secureCookies: true,
		operatorEmail: os.Getenv("KADRROOM_OPERATOR_EMAIL"),
	}
	if cfg.databaseURL == "" {
		return config{}, errors.New("KADRROOM_IDENTITY_DATABASE_URL is required")
	}
	cfg.proxyToken = os.Getenv("KADRROOM_AUTH_PROXY_TOKEN")
	if cfg.proxyToken != "" && (len(cfg.proxyToken) < 32 || strings.ContainsAny(cfg.proxyToken, " \t\r\n")) {
		return config{}, errors.New("KADRROOM_AUTH_PROXY_TOKEN must contain at least 32 bytes without whitespace")
	}
	mailKey := os.Getenv("KADRROOM_MAIL_TOKEN_KEY")
	if len(mailKey) != hex.EncodedLen(len(cfg.mailTokenKey)) {
		return config{}, errors.New("KADRROOM_MAIL_TOKEN_KEY must be a 32-byte key in hexadecimal")
	}
	if _, err := hex.Decode(cfg.mailTokenKey[:], []byte(mailKey)); err != nil {
		return config{}, errors.New("KADRROOM_MAIL_TOKEN_KEY must be a 32-byte key in hexadecimal")
	}
	if value := os.Getenv("KADRROOM_SECURE_COOKIES"); value != "" {
		secure, err := strconv.ParseBool(value)
		if err != nil {
			return config{}, errors.New("KADRROOM_SECURE_COOKIES must be true or false")
		}
		cfg.secureCookies = secure
	}
	defaultPublicURL := "https://localhost:5173"
	if !cfg.secureCookies {
		defaultPublicURL = "http://127.0.0.1:5173"
	}
	var err error
	cfg.publicURL, err = parseOrigin("KADRROOM_PUBLIC_URL", envOr("KADRROOM_PUBLIC_URL", defaultPublicURL), cfg.secureCookies)
	if err != nil {
		return config{}, err
	}
	if adminURL := os.Getenv("KADRROOM_ADMIN_PUBLIC_URL"); adminURL != "" {
		cfg.adminURL, err = parseOrigin("KADRROOM_ADMIN_PUBLIC_URL", adminURL, cfg.secureCookies)
		if err != nil {
			return config{}, err
		}
	}
	if cfg.operatorEmail != "" {
		email, valid := normalizeEmail(cfg.operatorEmail)
		if !valid {
			return config{}, errors.New("KADRROOM_OPERATOR_EMAIL must be an email address")
		}
		cfg.operatorEmail = email
	}
	cfg.mail = smtpMailer{
		addr:     os.Getenv("KADRROOM_SMTP_ADDR"),
		from:     os.Getenv("KADRROOM_SMTP_FROM"),
		username: os.Getenv("KADRROOM_SMTP_USERNAME"),
		password: os.Getenv("KADRROOM_SMTP_PASSWORD"),
		mode:     envOr("KADRROOM_SMTP_TLS", "starttls"),
	}
	if _, err := cfg.mail.host(); err != nil {
		return config{}, err
	}
	from, valid := normalizeEmail(cfg.mail.from)
	if !valid {
		return config{}, errors.New("KADRROOM_SMTP_FROM must be an email address")
	}
	cfg.mail.from = from
	if cfg.mail.mode != "starttls" && cfg.mail.mode != "implicit" && cfg.mail.mode != "none" {
		return config{}, errors.New("KADRROOM_SMTP_TLS must be starttls, implicit, or none")
	}
	if (cfg.mail.username == "") != (cfg.mail.password == "") {
		return config{}, errors.New("KADRROOM_SMTP_USERNAME and KADRROOM_SMTP_PASSWORD must be set together")
	}
	if cfg.mail.mode == "none" && cfg.mail.username != "" {
		return config{}, errors.New("SMTP credentials require TLS")
	}
	return cfg, nil
}

func parseOrigin(name, raw string, secureCookies bool) (*url.URL, error) {
	origin, err := url.Parse(raw)
	if err != nil || origin.Opaque != "" || origin.User != nil || origin.Hostname() == "" ||
		(origin.Scheme != "https" && (secureCookies || origin.Scheme != "http")) ||
		(origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" ||
		origin.ForceQuery || origin.Fragment != "" {
		return nil, fmt.Errorf("%s must be an origin and use HTTPS with secure cookies", name)
	}
	return origin, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func (m smtpMailer) host() (string, error) {
	host, port, err := net.SplitHostPort(m.addr)
	if err != nil || host == "" || strings.ContainsAny(host, " \t\r\n") {
		return "", errors.New("KADRROOM_SMTP_ADDR must be host:port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("KADRROOM_SMTP_ADDR has an invalid port")
	}
	return host, nil
}
