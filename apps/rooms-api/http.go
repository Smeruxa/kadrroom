package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type api struct {
	ctx     context.Context
	cfg     config
	repo    store
	client  *http.Client
	rooms   *roomEvents
	rtcWake chan struct{}
}

type createRequest struct {
	MediaID string `json:"mediaId"`
}

type joinRequest struct {
	Invite string `json:"invite"`
	Name   string `json:"name"`
}

type statusResponse struct {
	Status string `json:"status"`
}

type errorResponse struct {
	Code string `json:"code"`
}

type inviteResponse struct {
	Invite string `json:"invite"`
}

type ticketResponse struct {
	Ticket string `json:"ticket"`
}

type mediaResponse struct {
	URL       string `json:"url"`
	MediaType string `json:"mediaType"`
}

type catalogResponse struct {
	Items []catalogMedia `json:"items"`
}

type hostAccess struct {
	accountID string
	token     string
}

type roomAccess struct {
	roomID     string
	credential string
	host       hostAccess
}

func newAPI(ctx context.Context, cfg config, repo store) *api {
	client := &http.Client{
		Timeout:       4 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &api{ctx: ctx, cfg: cfg, repo: repo, client: client, rooms: newRoomEvents(), rtcWake: make(chan struct{}, 1)}
}

func (a *api) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
	})
	mux.HandleFunc("GET /readyz", a.ready)
	mux.HandleFunc("POST /v1/rooms", a.create)
	mux.HandleFunc("POST /v1/join", a.join)
	mux.HandleFunc("GET /v1/rooms/{id}", a.forRoom(a.session))
	mux.HandleFunc("POST /v1/rooms/{id}/invite", a.forRoom(a.rotate))
	mux.HandleFunc("POST /v1/rooms/{id}/close", a.forRoom(a.close))
	mux.HandleFunc("POST /v1/rooms/{id}/ws-ticket", a.forRoom(a.ticket))
	mux.HandleFunc("POST /v1/rooms/{id}/rtc-token", a.forRoom(a.rtcToken))
	mux.HandleFunc("GET /v1/rooms/{id}/ws", a.forRoom(a.websocket))
	mux.HandleFunc("GET /v1/rooms/{id}/playback-url", a.forRoom(a.playback))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodPost && !a.sameOrigin(r) {
			writeError(w, errOrigin)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 9*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *api) forRoom(handler func(http.ResponseWriter, *http.Request, roomAccess)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.sameOrigin(r) {
			writeError(w, errOrigin)
			return
		}
		access, err := a.authorizeRoom(r, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		handler(w, r, access)
	}
}

func (a *api) authorizeRoom(r *http.Request, roomID string) (roomAccess, error) {
	access := roomAccess{roomID: roomID, credential: roomCredential(r, roomID)}
	id, err := a.repo.hostForCredential(r.Context(), roomID, access.credential)
	if err != nil || id == "" {
		return access, err
	}
	access.host = hostAccess{accountID: id, token: accountCredential(r)}
	return access, a.validateHost(r.Context(), access.host)
}

func (a *api) validateHost(ctx context.Context, access hostAccess) error {
	if access.accountID == "" {
		return nil
	}
	current, err := a.currentAccount(ctx, access.token)
	if err != nil {
		return err
	}
	if current.ID != access.accountID {
		return errUnauthenticated
	}
	return nil
}

func (a *api) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if a.ctx.Err() != nil || a.repo.pool.Ping(ctx) != nil {
		writeJSON(w, http.StatusServiceUnavailable, statusResponse{Status: "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

func (a *api) create(w http.ResponseWriter, r *http.Request) {
	var request createRequest
	if err := readJSON(w, r, &request); err != nil || request.MediaID == "" || len(request.MediaID) > 128 {
		writeError(w, errBadRequest)
		return
	}
	creator, err := a.currentAccount(r.Context(), accountCredential(r))
	if err != nil {
		writeError(w, err)
		return
	}
	media, err := a.catalogMedia(r.Context(), request.MediaID)
	if err != nil {
		writeError(w, err)
		return
	}
	session, token, err := a.repo.create(r.Context(), creator, media)
	if err != nil {
		writeError(w, err)
		return
	}
	a.setRoomCookie(w, session, token)
	writeJSON(w, http.StatusCreated, session)
}

func (a *api) join(w http.ResponseWriter, r *http.Request) {
	var request joinRequest
	if err := readJSON(w, r, &request); err != nil {
		writeError(w, errBadRequest)
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if !validName(request.Name) {
		writeError(w, errBadRequest)
		return
	}
	if !validCredential(request.Invite) {
		writeError(w, errInvite)
		return
	}
	roomID, err := a.repo.inviteRoom(r.Context(), request.Invite)
	if err != nil {
		writeError(w, err)
		return
	}
	access, err := a.authorizeRoom(r, roomID)
	if err != nil {
		writeError(w, err)
		return
	}
	session, token, err := a.repo.join(r.Context(), roomID, request.Invite, request.Name, access.credential)
	if err != nil {
		writeError(w, err)
		return
	}
	a.setRoomCookie(w, session, token)
	a.rooms.publish(roomID)
	writeJSON(w, http.StatusOK, session)
}

func (a *api) session(w http.ResponseWriter, r *http.Request, access roomAccess) {
	session, err := a.repo.session(r.Context(), access.roomID, access.credential)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (a *api) rotate(w http.ResponseWriter, r *http.Request, access roomAccess) {
	invite, err := a.repo.rotate(r.Context(), access.roomID, access.credential)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inviteResponse{Invite: invite})
}

func (a *api) close(w http.ResponseWriter, r *http.Request, access roomAccess) {
	if err := a.repo.closeRoom(r.Context(), access.roomID, access.credential, a.cfg.rtc.internalURL != ""); err != nil {
		writeError(w, err)
		return
	}
	a.rooms.publish(access.roomID)
	select {
	case a.rtcWake <- struct{}{}:
	default:
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) ticket(w http.ResponseWriter, r *http.Request, access roomAccess) {
	ticket, err := a.repo.issueTicket(r.Context(), access.roomID, access.credential)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ticketResponse{Ticket: ticket})
}

func (a *api) playback(w http.ResponseWriter, r *http.Request, access roomAccess) {
	session, err := a.repo.session(r.Context(), access.roomID, access.credential)
	if err != nil {
		writeError(w, err)
		return
	}
	media, err := a.playableMedia(r.Context(), session)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, media)
}

func roomCredential(r *http.Request, roomID string) string {
	cookie, err := r.Cookie(roomCookieName(roomID))
	if err != nil {
		return ""
	}
	return cookie.Value
}

func roomCookieName(roomID string) string { return "kr_room_" + roomID }

func (a *api) setRoomCookie(w http.ResponseWriter, session roomSession, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: roomCookieName(session.RoomID), Value: token, Path: "/v1", Expires: session.ExpiresAt,
		SameSite: http.SameSiteLaxMode, HttpOnly: true, Secure: a.cfg.secureCookies,
	})
}

func accountCredential(r *http.Request) string {
	cookie, err := r.Cookie("kr_account")
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (a *api) currentAccount(ctx context.Context, token string) (account, error) {
	if token == "" {
		return account{}, errUnauthenticated
	}
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.identityURL+"/v1/auth/session", nil)
	if err != nil {
		return account{}, errors.Join(errUpstream, err)
	}
	request.AddCookie(&http.Cookie{Name: "kr_account", Value: token})
	response, err := a.client.Do(request)
	if err != nil {
		return account{}, errors.Join(errUpstream, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return account{}, errUnauthenticated
	}
	if response.StatusCode != http.StatusOK {
		return account{}, fmt.Errorf("%w: identity response status %d", errUpstream, response.StatusCode)
	}
	var creator account
	if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&creator); err != nil || creator.ID == "" {
		return account{}, fmt.Errorf("%w: malformed identity session", errUpstream)
	}
	creator.DisplayName = strings.TrimSpace(creator.DisplayName)
	if !validName(creator.DisplayName) {
		creator.DisplayName = "Ведущий"
	}
	return creator, nil
}

func (a *api) catalogMedia(ctx context.Context, mediaID string) (catalogMedia, error) {
	mediaURL, demo := a.cfg.mediaURLs[mediaID]
	if !demo && a.cfg.catalogToken == "" {
		return catalogMedia{}, errMedia
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.catalogURL+"/v1/catalog", nil)
	if err != nil {
		return catalogMedia{}, errors.Join(errUpstream, err)
	}
	response, err := a.client.Do(request)
	if err != nil {
		return catalogMedia{}, errors.Join(errUpstream, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return catalogMedia{}, fmt.Errorf("%w: catalog response status %d", errUpstream, response.StatusCode)
	}
	var catalog catalogResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&catalog); err != nil {
		return catalogMedia{}, errors.Join(errUpstream, err)
	}
	for _, media := range catalog.Items {
		if media.ID != mediaID || !media.Available || media.DurationSeconds <= 0 || media.DurationSeconds >= float64(math.MaxInt64)/1000 {
			continue
		}
		media.MediaType = mediaKind(media.MediaType, mediaURL)
		media.DurationMS = max(1, int64(math.Round(media.DurationSeconds*1000)))
		return media, nil
	}
	return catalogMedia{}, errMedia
}

func (a *api) playableMedia(ctx context.Context, session roomSession) (mediaResponse, error) {
	if mediaURL, ok := a.cfg.mediaURLs[session.MediaID]; ok {
		return mediaResponse{URL: mediaURL, MediaType: session.MediaType}, nil
	}
	if a.cfg.catalogToken == "" {
		return mediaResponse{}, errMedia
	}
	endpoint := a.cfg.catalogURL + "/_internal/catalog/" + url.PathEscape(session.MediaID) + "/playback"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return mediaResponse{}, errors.Join(errUpstream, err)
	}
	request.Header.Set("Authorization", "Bearer "+a.cfg.catalogToken)
	response, err := a.client.Do(request)
	if err != nil {
		return mediaResponse{}, errors.Join(errUpstream, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return mediaResponse{}, errMedia
	}
	if response.StatusCode != http.StatusOK {
		return mediaResponse{}, fmt.Errorf("%w: catalog playback response status %d", errUpstream, response.StatusCode)
	}
	var media mediaResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&media); err != nil {
		return mediaResponse{}, errors.Join(errUpstream, err)
	}
	if !validMediaURL(media.URL) || (media.MediaType != "video" && media.MediaType != "audio") {
		return mediaResponse{}, fmt.Errorf("%w: malformed catalog playback response", errUpstream)
	}
	return media, nil
}

func (a *api) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return (origin == "" || origin == a.cfg.publicOrigin) && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

func readJSON(w http.ResponseWriter, r *http.Request, payload any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(payload); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errBadRequest
	}
	return nil
}

func statusAndCode(err error) (int, string) {
	var code roomError
	if !errors.As(err, &code) {
		return http.StatusInternalServerError, "internal_error"
	}
	switch code {
	case errUnauthenticated:
		return http.StatusUnauthorized, string(code)
	case errForbidden, errOrigin, errTicket:
		return http.StatusForbidden, string(code)
	case errRoomMissing, errInvite, errMedia:
		return http.StatusNotFound, string(code)
	case errRoomClosed:
		return http.StatusGone, string(code)
	case errRoomFull, errStale:
		return http.StatusConflict, string(code)
	case errUpstream:
		return http.StatusBadGateway, string(code)
	case errRTC:
		return http.StatusServiceUnavailable, string(code)
	default:
		return http.StatusBadRequest, string(code)
	}
}

func writeError(w http.ResponseWriter, err error) {
	status, code := statusAndCode(err)
	if status >= 500 && !errors.Is(err, errRTC) {
		slog.Error("rooms request failed", "error", err)
	}
	writeJSON(w, status, errorResponse{Code: code})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Debug("write rooms response", "error", err)
	}
}
