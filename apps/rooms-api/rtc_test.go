package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestRTCConfiguration(t *testing.T) {
	env := map[string]string{}
	getenv := func(name string) string { return env[name] }
	if cfg, err := loadRTCConfig(getenv, true); err != nil || cfg != (rtcConfig{}) {
		t.Fatalf("unset configuration = %#v %v", cfg, err)
	}
	env["KADRROOM_LIVEKIT_URL"] = "ws://localhost:7880"
	if _, err := loadRTCConfig(getenv, false); err == nil {
		t.Fatal("partial credentials were accepted")
	}
	env["KADRROOM_LIVEKIT_API_KEY"] = "test-key"
	env["KADRROOM_LIVEKIT_API_SECRET"] = "test-secret"
	env["KADRROOM_LIVEKIT_INTERNAL_URL"] = "http://livekit:7880"
	if _, err := loadRTCConfig(getenv, false); err != nil {
		t.Fatalf("local configuration refused: %v", err)
	}
	if _, err := loadRTCConfig(getenv, true); err == nil {
		t.Fatal("insecure transport accepted with secure cookies")
	}
}

func TestRTCDisabled(t *testing.T) {
	a := &api{}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://localhost/v1/rooms/id/rtc-token", nil))
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != "{\"code\":\"rtc_unavailable\"}\n" {
		t.Fatalf("disabled signaling = %d %s", w.Code, w.Body)
	}
}

func TestRTCTokenIsScopedAndShortLived(t *testing.T) {
	cfg := rtcConfig{url: "wss://livekit.example", key: "example", secret: "locally-isolated-unit-test-secret"}
	now := time.Unix(1800000000, 0)
	session := roomSession{RoomID: "room-id", ParticipantID: "participant-id", Name: "Зритель", ExpiresAt: now.Add(60 * time.Second)}
	signed, err := cfg.token(session, now)
	if err != nil {
		t.Fatal(err)
	}
	var claims rtcClaims
	token, err := jwt.ParseWithClaims(signed, &claims, func(_ *jwt.Token) (any, error) {
		return []byte(cfg.secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(func() time.Time { return now }), jwt.WithIssuer(cfg.key))
	if err != nil || !token.Valid {
		t.Fatalf("validating JWT: %v", err)
	}
	if claims.Subject != session.ParticipantID || claims.Name != session.Name || !claims.ExpiresAt.Equal(session.ExpiresAt) {
		t.Fatalf("unexpected token identity or expiry: %#v", claims)
	}
	if claims.Video.Room != session.RoomID || !claims.Video.RoomJoin || !claims.Video.CanSubscribe || !claims.Video.CanPublish ||
		claims.Video.CanPublishData || !slices.Equal(claims.Video.CanPublishSources, []string{"camera", "microphone"}) ||
		claims.RoomConfig.MaxParticipants != roomCapacity {
		t.Fatalf("unexpected grants: %#v", claims)
	}
}

func TestRTCRequiresRoomMembership(t *testing.T) {
	s := integrationStore(t)
	cfg, _ := integrationAPI(t, s)
	cfg.rtc = rtcConfig{url: "wss://livekit.example", key: "fixture", secret: "isolated-fixture"}
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/twirp/livekit.RoomService/CreateRoom" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(control.Close)
	cfg.rtc.internalURL = control.URL
	server := startRoomsServer(t, cfg, s)
	host := browser(t, server.URL, true)
	guest := browser(t, server.URL, false)
	var room roomSession
	requestJSON(t, host, http.MethodPost, server.URL+"/v1/rooms", createRequest{MediaID: "clip"}, &room, 201)
	endpoint := server.URL + "/v1/rooms/" + room.RoomID + "/rtc-token"
	var failure errorResponse
	requestJSON(t, guest, http.MethodPost, endpoint, nil, &failure, 403)
	var result rtcResponse
	requestJSON(t, host, http.MethodPost, endpoint, nil, &result, 200)
	if result.URL != cfg.rtc.url || result.Token == "" {
		t.Fatalf("bad RTC response: %#v", result)
	}
}

func TestRTCCloseRetriesProviderAndRejectsNewTickets(t *testing.T) {
	s := integrationStore(t)
	cfg, _ := integrationAPI(t, s)
	cfg.rtc = rtcConfig{url: "wss://livekit.example", key: "fixture", secret: "local-test-secret"}
	var creates, deletes atomic.Int32
	var deleteAvailable atomic.Bool
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var claims rtcControlClaims
		value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, err := jwt.ParseWithClaims(value, &claims, func(_ *jwt.Token) (any, error) {
			return []byte(cfg.rtc.secret), nil
		}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(cfg.rtc.key))
		if err != nil || !claims.Video.RoomCreate {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/twirp/livekit.RoomService/CreateRoom":
			var payload rtcCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil ||
				payload.Name != claims.Video.Room || payload.MaxParticipants != roomCapacity {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			creates.Add(1)
		case "/twirp/livekit.RoomService/DeleteRoom":
			var payload rtcDeleteRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Room != claims.Video.Room {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			deletes.Add(1)
			if !deleteAvailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(control.Close)
	cfg.rtc.internalURL = control.URL
	server := startRoomsServer(t, cfg, s)
	host := browser(t, server.URL, true)
	var room roomSession
	requestJSON(t, host, http.MethodPost, server.URL+"/v1/rooms", createRequest{MediaID: "clip"}, &room, 201)
	roomURL := server.URL + "/v1/rooms/" + room.RoomID
	requestJSON(t, host, http.MethodPost, roomURL+"/rtc-token", nil, nil, 200)
	requestJSON(t, host, http.MethodPost, roomURL+"/close", nil, nil, 204)
	requestJSON(t, host, http.MethodPost, roomURL+"/rtc-token", nil, nil, 410)
	if creates.Load() != 1 {
		t.Fatal("LiveKit created a room after it was closed")
	}
	first := newAPI(t.Context(), cfg, s)
	if worked, err := first.deleteClosedCall(t.Context()); !worked || err == nil {
		t.Fatalf("simulated provider outage: worked=%v err=%v", worked, err)
	}
	if worked, err := first.deleteClosedCall(t.Context()); worked || err != nil {
		t.Fatalf("immediate retry ignored the backoff: worked=%v err=%v", worked, err)
	}
	testQuery := `UPDATE room_rtc_calls SET next_delete_at = now() WHERE room_id = $1`
	if _, err := s.pool.Exec(t.Context(), testQuery, room.RoomID); err != nil {
		t.Fatal(err)
	}
	deleteAvailable.Store(true)
	restarted := newAPI(t.Context(), cfg, s)
	if worked, err := restarted.deleteClosedCall(t.Context()); !worked || err != nil {
		t.Fatalf("expected pending delete after restart: worked=%v err=%v", worked, err)
	}
	if worked, err := restarted.deleteClosedCall(t.Context()); worked || err != nil {
		t.Fatalf("completed delete remained pending: worked=%v err=%v", worked, err)
	}
	if attemptCount := deletes.Load(); attemptCount != 2 {
		t.Fatalf("delete attempts = %d, want 2", attemptCount)
	}
}

func TestRTCExpiryClosesRoomWhenProviderCallAlreadyEnded(t *testing.T) {
	s := integrationStore(t)
	room, token, err := s.create(t.Context(), account{ID: "host", DisplayName: "Ведущий"},
		catalogMedia{ID: "clip", MediaType: "video", DurationMS: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.rtcSession(t.Context(), room.RoomID, token, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE room_participants SET seat_expires_at = now() WHERE id = $1`, room.ParticipantID); err != nil {
		t.Fatal(err)
	}
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/twirp/livekit.RoomService/DeleteRoom" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusNotFound, rtcServiceError{Code: "not_found"})
	}))
	t.Cleanup(control.Close)
	a := newAPI(t.Context(), config{rtc: rtcConfig{internalURL: control.URL}}, s)
	if worked, err := a.deleteClosedCall(t.Context()); !worked || err != nil {
		t.Fatalf("expired room deletion: worked=%v err=%v", worked, err)
	}
	if worked, err := a.deleteClosedCall(t.Context()); worked || err != nil {
		t.Fatalf("nonexistent provider room was not acknowledged: worked=%v err=%v", worked, err)
	}
	var closed bool
	if err := s.pool.QueryRow(t.Context(), `SELECT closed_at IS NOT NULL FROM rooms WHERE id = $1`, room.RoomID).Scan(&closed); err != nil || !closed {
		t.Fatalf("expired room was not permanently closed: closed=%v err=%v", closed, err)
	}
}

func TestRTCLostCreateResponsePreservesCleanup(t *testing.T) {
	for _, ending := range []string{"close", "expiry"} {
		t.Run(ending, func(t *testing.T) {
			s := integrationStore(t)
			cfg, _ := integrationAPI(t, s)
			cfg.rtc = rtcConfig{url: "wss://livekit.example", key: "fixture", secret: "local-test-secret"}
			var providerRoomCreated, responseLost atomic.Bool
			control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/twirp/livekit.RoomService/CreateRoom":
					providerRoomCreated.Store(true)
					hijacker, ok := w.(http.Hijacker)
					if !ok {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					conn, _, err := hijacker.Hijack()
					if err != nil {
						return
					}
					responseLost.Store(true)
					_ = conn.Close()
				case "/twirp/livekit.RoomService/DeleteRoom":
					providerRoomCreated.Store(false)
					w.WriteHeader(http.StatusOK)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(control.Close)
			cfg.rtc.internalURL = control.URL
			server := startRoomsServer(t, cfg, s)
			host := browser(t, server.URL, true)
			var room roomSession
			requestJSON(t, host, http.MethodPost, server.URL+"/v1/rooms", createRequest{MediaID: "clip"}, &room, 201)
			roomURL := server.URL + "/v1/rooms/" + room.RoomID
			var response errorResponse
			requestJSON(t, host, http.MethodPost, roomURL+"/rtc-token", nil, &response, 502)
			if !providerRoomCreated.Load() || !responseLost.Load() || response.Code != string(errUpstream) {
				t.Fatal("fixture did not create a provider room and lose the response")
			}
			var cleanupRecorded bool
			if err := s.pool.QueryRow(t.Context(), `SELECT deleted_at IS NULL FROM room_rtc_calls WHERE room_id = $1`, room.RoomID).Scan(&cleanupRecorded); err != nil || !cleanupRecorded {
				t.Fatalf("ambiguous CreateRoom lost cleanup intent: recorded=%v err=%v", cleanupRecorded, err)
			}
			if ending == "close" {
				requestJSON(t, host, http.MethodPost, roomURL+"/close", nil, nil, 204)
			} else {
				if _, err := s.pool.Exec(t.Context(), `UPDATE room_participants SET seat_expires_at = now() WHERE id = $1`, room.ParticipantID); err != nil {
					t.Fatal(err)
				}
			}
			restarted := newAPI(t.Context(), cfg, s)
			if worked, err := restarted.deleteClosedCall(t.Context()); !worked || err != nil || providerRoomCreated.Load() {
				t.Fatalf("orphan RTC room did not get deleted: worked=%v err=%v", worked, err)
			}
		})
	}
}
