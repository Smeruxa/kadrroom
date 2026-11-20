package main

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const rtcTokenTime = 2 * time.Minute

type rtcConfig struct {
	url         string
	internalURL string
	key         string
	secret      string
}

type rtcResponse struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type rtcClaims struct {
	jwt.RegisteredClaims
	Name       string        `json:"name"`
	Video      rtcGrant      `json:"video"`
	RoomConfig rtcRoomConfig `json:"roomConfig"`
}

type rtcGrant struct {
	Room              string   `json:"room"`
	RoomJoin          bool     `json:"roomJoin"`
	CanSubscribe      bool     `json:"canSubscribe"`
	CanPublish        bool     `json:"canPublish"`
	CanPublishData    bool     `json:"canPublishData"`
	CanPublishSources []string `json:"canPublishSources"`
}

type rtcRoomConfig struct {
	MaxParticipants int `json:"maxParticipants"`
}

func loadRTCConfig(getenv func(string) string, secure bool) (rtcConfig, error) {
	cfg := rtcConfig{
		url: getenv("KADRROOM_LIVEKIT_URL"), internalURL: getenv("KADRROOM_LIVEKIT_INTERNAL_URL"),
		key: getenv("KADRROOM_LIVEKIT_API_KEY"), secret: getenv("KADRROOM_LIVEKIT_API_SECRET"),
	}
	if cfg == (rtcConfig{}) {
		return cfg, nil
	}
	if cfg.url == "" || cfg.internalURL == "" || cfg.key == "" || cfg.secret == "" {
		return rtcConfig{}, errors.New("set KADRROOM_LIVEKIT_URL, KADRROOM_LIVEKIT_INTERNAL_URL, KADRROOM_LIVEKIT_API_KEY and KADRROOM_LIVEKIT_API_SECRET together")
	}
	parsed, err := url.Parse(cfg.url)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "wss" && (secure || parsed.Scheme != "ws")) {
		return rtcConfig{}, errors.New("KADRROOM_LIVEKIT_URL must be a WebSocket URL and use WSS with secure cookies")
	}
	if secure && len(cfg.secret) < 32 {
		return rtcConfig{}, errors.New("KADRROOM_LIVEKIT_API_SECRET must have at least 32 bytes with secure cookies")
	}
	cfg.internalURL, err = upstreamBase(cfg.internalURL, "")
	if err != nil {
		return rtcConfig{}, errors.New("KADRROOM_LIVEKIT_INTERNAL_URL must be a trusted HTTP or HTTPS service URL")
	}
	return cfg, nil
}

func (a *api) rtcToken(w http.ResponseWriter, r *http.Request, access roomAccess) {
	if a.cfg.rtc.url == "" {
		writeError(w, errRTC)
		return
	}
	ctx := r.Context()
	session, err := a.repo.rtcSession(ctx, access.roomID, access.credential, func() error {
		if err := a.controlLiveKitRoom(ctx, access.roomID, "CreateRoom", rtcCreateRequest{
			Name: access.roomID, MaxParticipants: roomCapacity,
		}); err != nil {
			return errors.Join(errUpstream, err)
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	token, err := a.cfg.rtc.token(session, time.Now())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rtcResponse{URL: a.cfg.rtc.url, Token: token})
}

func (cfg rtcConfig) token(session roomSession, now time.Time) (string, error) {
	expires := now.Add(rtcTokenTime)
	if session.ExpiresAt.Before(expires) {
		expires = session.ExpiresAt
	}
	claims := rtcClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: cfg.key, Subject: session.ParticipantID, IssuedAt: jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)), ExpiresAt: jwt.NewNumericDate(expires),
		},
		Name: session.Name,
		Video: rtcGrant{Room: session.RoomID, RoomJoin: true, CanSubscribe: true, CanPublish: true,
			CanPublishSources: []string{"camera", "microphone"}},
		RoomConfig: rtcRoomConfig{MaxParticipants: roomCapacity},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(cfg.secret))
}
