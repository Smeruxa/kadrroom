package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

const sessionCookie = "kr_account"

type identityHandler struct {
	store         store
	publicOrigin  string
	adminOrigin   string
	secureCookies bool
	operatorEmail string
	unknownHash   string
	passwordSlots chan struct{}
	draining      atomic.Bool
}

type emailRequest struct {
	Email string `json:"email"`
}

type tokenRequest struct {
	Token       string `json:"token"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName,omitempty"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type errorResponse struct {
	Code string `json:"code"`
}

type statusResponse struct {
	Status string `json:"status"`
}

func newIdentityHandler(s store, cfg config) (*identityHandler, error) {
	dummy, err := newToken()
	if err != nil {
		return nil, err
	}
	unknownHash, err := hashPassword(dummy)
	if err != nil {
		return nil, err
	}
	adminOrigin := ""
	if cfg.adminURL != nil {
		adminOrigin = canonicalOrigin(cfg.adminURL)
	}
	return &identityHandler{
		store:         s,
		publicOrigin:  canonicalOrigin(cfg.publicURL),
		adminOrigin:   adminOrigin,
		secureCookies: cfg.secureCookies,
		operatorEmail: cfg.operatorEmail,
		unknownHash:   unknownHash,
		passwordSlots: make(chan struct{}, 4),
	}, nil
}

func canonicalOrigin(origin *url.URL) string {
	host := strings.ToLower(origin.Host)
	if (origin.Scheme == "https" && origin.Port() == "443") || (origin.Scheme == "http" && origin.Port() == "80") {
		host = strings.ToLower(origin.Hostname())
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return origin.Scheme + "://" + host
}

func (h *identityHandler) routes() http.Handler {
	mux := http.NewServeMux()
	handle := func(method, path string, endpoint http.HandlerFunc) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			methodAllowed := r.Method == method || (method == http.MethodGet && r.Method == http.MethodHead)
			if !methodAllowed {
				w.Header().Set("Allow", method)
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
				return
			}
			if method == http.MethodPost {
				origin := r.Header.Get("Origin")
				if (origin != "" && origin != h.publicOrigin && origin != h.adminOrigin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
					writeError(w, http.StatusForbidden, "invalid_origin")
					return
				}
			}
			ctx, cancel := context.WithTimeout(r.Context(), 7*time.Second)
			defer cancel()
			endpoint(w, r.WithContext(ctx))
		})
	}
	handle(http.MethodGet, "/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
	})
	handle(http.MethodGet, "/readyz", h.ready)
	handle(http.MethodPost, "/v1/auth/register", h.requestEmail(purposeVerify))
	handle(http.MethodPost, "/v1/auth/verify", h.confirm(purposeVerify))
	handle(http.MethodPost, "/v1/auth/login", h.login)
	handle(http.MethodGet, "/v1/auth/session", h.session)
	handle(http.MethodPost, "/v1/auth/logout", h.logout)
	handle(http.MethodPost, "/v1/auth/password/reset-request", h.requestEmail(purposeReset))
	handle(http.MethodPost, "/v1/auth/password/reset", h.confirm(purposeReset))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found")
	})
	return mux
}

func (h *identityHandler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if h.draining.Load() || h.store.pool.Ping(ctx) != nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

func (h *identityHandler) requestEmail(purpose string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.allowPeer(w, r) {
			return
		}
		var body emailRequest
		if !readJSON(w, r, &body) {
			return
		}
		email, valid := normalizeEmail(body.Email)
		if !valid {
			writeError(w, http.StatusBadRequest, "invalid_email")
			return
		}
		if !h.allow(w, r, rateKey("email:"+purpose, email), 3, 15*time.Minute) {
			return
		}
		if err := h.store.queueMail(r.Context(), purpose, email); err != nil {
			serviceError(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func (h *identityHandler) confirm(purpose string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.allowPeer(w, r) {
			return
		}
		var body tokenRequest
		if !readJSON(w, r, &body) {
			return
		}
		if !validToken(body.Token) {
			writeError(w, http.StatusBadRequest, "invalid_token")
			return
		}
		if !acceptableNewPassword(body.Password) {
			writeError(w, http.StatusBadRequest, "invalid_password")
			return
		}
		name, validName := normalizeName(body.DisplayName)
		if purpose == purposeVerify && !validName {
			writeError(w, http.StatusBadRequest, "invalid_display_name")
			return
		}
		if purpose == purposeReset && body.DisplayName != "" {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		exists, err := h.store.challengeExists(r.Context(), purpose, body.Token)
		if err != nil {
			serviceError(w, err)
			return
		}
		if !exists {
			writeError(w, http.StatusBadRequest, "invalid_token")
			return
		}
		if !h.takePasswordSlot(w) {
			return
		}
		passwordHash, err := hashPassword(body.Password)
		<-h.passwordSlots
		if err != nil {
			serviceError(w, err)
			return
		}
		var used bool
		if purpose == purposeVerify {
			used, err = h.store.verify(r.Context(), body.Token, name, passwordHash, h.operatorEmail)
		} else {
			used, err = h.store.reset(r.Context(), body.Token, passwordHash)
		}
		if err != nil {
			serviceError(w, err)
			return
		}
		if !used {
			writeError(w, http.StatusBadRequest, "invalid_token")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *identityHandler) login(w http.ResponseWriter, r *http.Request) {
	if !h.allowPeer(w, r) {
		return
	}
	var body loginRequest
	if !readJSON(w, r, &body) {
		return
	}
	email, valid := normalizeEmail(body.Email)
	if !valid || !validPassword(body.Password) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if !h.allow(w, r, rateKey("login", email), 8, 15*time.Minute) {
		return
	}
	profile, passwordHash, err := h.store.credentials(r.Context(), email)
	found := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		passwordHash = h.unknownHash
	} else if err != nil {
		serviceError(w, err)
		return
	}
	if !h.takePasswordSlot(w) {
		return
	}
	matches := verifyPassword(passwordHash, body.Password)
	<-h.passwordSlots
	if !found || !matches {
		writeError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	token, err := newToken()
	if err != nil {
		serviceError(w, err)
		return
	}
	created, err := h.store.createSession(r.Context(), profile.ID, passwordHash, token, requestToken(r))
	if err != nil {
		serviceError(w, err)
		return
	}
	if !created {
		writeError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	h.setSessionCookie(w, token, int(sessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, profile)
}

func (h *identityHandler) session(w http.ResponseWriter, r *http.Request) {
	token := requestToken(r)
	if !validToken(token) {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	profile, err := h.store.session(r.Context(), token)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err != nil {
		serviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (h *identityHandler) logout(w http.ResponseWriter, r *http.Request) {
	token := requestToken(r)
	if validToken(token) {
		if err := h.store.logout(r.Context(), token); err != nil {
			serviceError(w, err)
			return
		}
	}
	h.setSessionCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (h *identityHandler) setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func requestToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (h *identityHandler) allowPeer(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return h.allow(w, r, rateKey("auth-peer", host), 150, time.Minute)
}

func (h *identityHandler) allow(w http.ResponseWriter, r *http.Request, key string, maxAttempts int, window time.Duration) bool {
	allowed, err := h.store.allow(r.Context(), key, maxAttempts, window)
	if err != nil {
		serviceError(w, err)
		return false
	}
	if !allowed {
		writeRateLimit(w, window)
	}
	return allowed
}

func (h *identityHandler) takePasswordSlot(w http.ResponseWriter) bool {
	select {
	case h.passwordSlots <- struct{}{}:
		return true
	default:
		writeRateLimit(w, time.Second)
		return false
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, payload any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_request")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func serviceError(w http.ResponseWriter, err error) {
	if !errors.Is(err, context.Canceled) {
		slog.Warn("identity request failed")
	}
	writeError(w, http.StatusServiceUnavailable, "service_unavailable")
}

func writeRateLimit(w http.ResponseWriter, after time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(after.Seconds())))
	writeError(w, http.StatusTooManyRequests, "rate_limited")
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, errorResponse{Code: code})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Debug("write identity response")
	}
}
