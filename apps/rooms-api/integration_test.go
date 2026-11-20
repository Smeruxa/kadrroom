package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func integrationStore(t *testing.T) store {
	t.Helper()
	databaseURL := os.Getenv("KADRROOM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("KADRROOM_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	base, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	schemaName := pgx.Identifier{"rooms_test_" + strings.ToLower(rand.Text())}.Sanitize()
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := base.Exec(cleanup, "DROP SCHEMA "+schemaName+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func integrationAPI(t *testing.T, s store) (config, *httptest.Server) {
	t.Helper()
	dependencies := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog":
			writeJSON(w, http.StatusOK, catalogResponse{Items: []catalogMedia{
				{ID: "clip", Available: true, DurationSeconds: 15.2},
				{ID: "concert", Available: true, DurationSeconds: 12, MediaType: "audio"},
			}})
		case "/v1/auth/session":
			cookie, err := r.Cookie("kr_account")
			if err != nil || cookie.Value != "fixture" {
				writeError(w, errUnauthenticated)
				return
			}
			writeJSON(w, http.StatusOK, account{ID: "account", DisplayName: "Ведущий"})
		case "/_internal/catalog/concert/playback":
			if r.Header.Get("Authorization") != "Bearer fixture-catalog-token" {
				writeError(w, errForbidden)
				return
			}
			writeJSON(w, http.StatusOK, mediaResponse{URL: "/v1/media/concert?signature=fixture", MediaType: "audio"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(dependencies.Close)
	cfg := config{identityURL: dependencies.URL, catalogURL: dependencies.URL, catalogToken: "fixture-catalog-token",
		mediaURLs: map[string]string{"clip": "/demo-media/clip.mp4"}}
	return cfg, startRoomsServer(t, cfg, s)
}

func startRoomsServer(t *testing.T, cfg config, s store) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	cfg.publicOrigin = "http://" + server.Listener.Addr().String()
	server.Config.Handler = newAPI(t.Context(), cfg, s).routes()
	server.Start()
	t.Cleanup(server.Close)
	return server
}

type wireMessage struct {
	Type          string        `json:"type"`
	Code          string        `json:"code"`
	State         playbackState `json:"state"`
	Participants  []participant `json:"participants"`
	HostConnected bool          `json:"hostConnected"`
	ClientTimeMS  int64         `json:"clientTimeMs"`
	ServerTimeMS  int64         `json:"serverTimeMs"`
}

func dialRoom(t *testing.T, client *http.Client, serverURL, roomID, ticket string, wantStatus int) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(serverURL, "http") + "/v1/rooms/" + roomID + "/ws?ticket=" + ticket
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: client, HTTPHeader: http.Header{"Origin": {serverURL}},
	})
	if wantStatus != http.StatusSwitchingProtocols {
		if response == nil || response.StatusCode != wantStatus || err == nil {
			t.Fatalf("dial failed to reject: response=%v error=%v", response, err)
		}
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func readSocketUntil(t *testing.T, conn *websocket.Conn, check func(wireMessage) bool) wireMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var frame wireMessage
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			t.Fatal(err)
		}
		if check(frame) {
			return frame
		}
	}
}

func writeSocketFrame(t *testing.T, conn *websocket.Conn, message socketMessage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, message); err != nil {
		t.Fatal(err)
	}
}

func TestWebSocketTimelineAndReconnect(t *testing.T) {
	s := integrationStore(t)
	cfg, server := integrationAPI(t, s)
	host := browser(t, server.URL, true)
	guest := browser(t, server.URL, false)
	var room roomSession
	requestJSON(t, host, http.MethodPost, server.URL+"/v1/rooms", createRequest{MediaID: "clip"}, &room, 201)
	var guestSession roomSession
	requestJSON(t, guest, http.MethodPost, server.URL+"/v1/join", joinRequest{Invite: room.Invite, Name: "Зритель"}, &guestSession, 200)
	roomPath := "/v1/rooms/" + room.RoomID
	var hostTicket, guestTicket ticketResponse
	requestJSON(t, host, http.MethodPost, server.URL+roomPath+"/ws-ticket", nil, &hostTicket, 200)
	requestJSON(t, guest, http.MethodPost, server.URL+roomPath+"/ws-ticket", nil, &guestTicket, 200)
	hostWS := dialRoom(t, host, server.URL, room.RoomID, hostTicket.Ticket, 101)
	guestWS := dialRoom(t, guest, server.URL, room.RoomID, guestTicket.Ticket, 101)
	dialRoom(t, guest, server.URL, room.RoomID, guestTicket.Ticket, 403)
	isSnapshot := func(frame wireMessage) bool { return frame.Type == "snapshot" }
	initialHost := readSocketUntil(t, hostWS, isSnapshot)
	initialGuest := readSocketUntil(t, guestWS, isSnapshot)
	if initialHost.State.Revision != 0 || initialHost.State != initialGuest.State || len(initialGuest.Participants) != 2 || !initialGuest.HostConnected {
		t.Fatalf("initial snapshots host=%#v guest=%#v", initialHost, initialGuest)
	}
	zero := int64(0)
	command := socketMessage{Type: "command", Action: "play", CommandID: "play-1", ExpectedRevision: &zero}
	writeSocketFrame(t, guestWS, command)
	readSocketUntil(t, guestWS, func(frame wireMessage) bool { return frame.Type == "error" && frame.Code == string(errForbidden) })
	writeSocketFrame(t, hostWS, command)
	isFirstRevision := func(frame wireMessage) bool { return isSnapshot(frame) && frame.State.Revision == 1 }
	playing := readSocketUntil(t, guestWS, isFirstRevision)
	if !playing.State.Playing {
		t.Fatalf("guest playback = %#v", playing.State)
	}
	readSocketUntil(t, hostWS, isFirstRevision)
	command.Action = "pause"
	writeSocketFrame(t, hostWS, command)
	if repeated := readSocketUntil(t, hostWS, isSnapshot); repeated.State != playing.State {
		t.Fatalf("replayed command changed state: got %#v, want %#v", repeated.State, playing.State)
	}
	command.CommandID = "stale"
	writeSocketFrame(t, hostWS, command)
	readSocketUntil(t, hostWS, func(frame wireMessage) bool { return frame.Type == "error" && frame.Code == string(errStale) })
	clientTime := time.Now().UnixMilli()
	writeSocketFrame(t, guestWS, socketMessage{Type: "ping", ClientTimeMS: &clientTime})
	if pong := readSocketUntil(t, guestWS, func(frame wireMessage) bool { return frame.Type == "pong" }); pong.ClientTimeMS != clientTime || pong.ServerTimeMS <= 0 {
		t.Fatalf("pong = %#v", pong)
	}
	if err := hostWS.CloseNow(); err != nil {
		t.Fatal(err)
	}
	readSocketUntil(t, guestWS, func(frame wireMessage) bool { return isSnapshot(frame) && !frame.HostConnected })
	if err := guestWS.CloseNow(); err != nil {
		t.Fatal(err)
	}
	restarted := startRoomsServer(t, cfg, s)
	var resumed roomSession
	requestJSON(t, guest, http.MethodGet, restarted.URL+roomPath, nil, &resumed, 200)
	if resumed.ParticipantID != guestSession.ParticipantID {
		t.Fatalf("new server lost membership: %#v", resumed)
	}
	requestJSON(t, guest, http.MethodPost, restarted.URL+roomPath+"/ws-ticket", nil, &guestTicket, 200)
	reconnected := dialRoom(t, guest, restarted.URL, room.RoomID, guestTicket.Ticket, 101)
	if snapshot := readSocketUntil(t, reconnected, isSnapshot); snapshot.State != playing.State {
		t.Fatalf("new server lost timeline: %#v", snapshot.State)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE room_participants SET seat_expires_at = now() WHERE id = $1`, room.ParticipantID); err != nil {
		t.Fatal(err)
	}
	var closed errorResponse
	requestJSON(t, guest, http.MethodGet, restarted.URL+roomPath, nil, &closed, 410)
	if closed.Code != string(errRoomClosed) {
		t.Fatalf("host reconnect window expired: %#v", closed)
	}
}

func TestUploadedMediaRequiresRoomMembership(t *testing.T) {
	repo := integrationStore(t)
	_, server := integrationAPI(t, repo)
	host := browser(t, server.URL, true)
	guest := browser(t, server.URL, false)
	var room roomSession
	requestJSON(t, host, http.MethodPost, server.URL+"/v1/rooms", createRequest{MediaID: "concert"}, &room, 201)
	playback := server.URL + "/v1/rooms/" + room.RoomID + "/playback-url"
	var code errorResponse
	requestJSON(t, guest, http.MethodGet, playback, nil, &code, 403)
	var media mediaResponse
	requestJSON(t, host, http.MethodGet, playback, nil, &media, 200)
	if media.MediaType != "audio" || media.URL != "/v1/media/concert?signature=fixture" {
		t.Fatalf("uploaded media response = %#v", media)
	}
}

func TestHostAuthorizationRevocationAndIdentityOutage(t *testing.T) {
	s := integrationStore(t)
	cfg, _ := integrationAPI(t, s)
	var identityState atomic.Int32
	identityState.Store(http.StatusOK)
	identityAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("kr_account")
		if err != nil || cookie.Value == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		status := int(identityState.Load())
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		writeJSON(w, status, account{ID: cookie.Value, DisplayName: "Ведущий"})
	}))
	t.Cleanup(identityAPI.Close)
	cfg.identityURL = identityAPI.URL
	server := startRoomsServer(t, cfg, s)
	host := browser(t, server.URL, true)
	guest := browser(t, server.URL, false)
	var owner, visitor roomSession
	requestJSON(t, host, http.MethodPost, server.URL+"/v1/rooms", createRequest{MediaID: "clip"}, &owner, 201)
	roomURL := server.URL + "/v1/rooms/" + owner.RoomID
	requestJSON(t, guest, http.MethodPost, server.URL+"/v1/join", joinRequest{Invite: owner.Invite, Name: "Гость"}, &visitor, 200)
	var ticket ticketResponse
	requestJSON(t, host, http.MethodPost, roomURL+"/ws-ticket", nil, &ticket, 200)
	hostWS := dialRoom(t, host, server.URL, owner.RoomID, ticket.Ticket, 101)
	readSocketUntil(t, hostWS, func(frame wireMessage) bool { return frame.Type == "snapshot" })

	zero := int64(0)
	play := socketMessage{Type: "command", Action: "play", CommandID: "start-clip", ExpectedRevision: &zero}
	identityState.Store(http.StatusServiceUnavailable)
	writeSocketFrame(t, hostWS, play)
	readSocketUntil(t, hostWS, func(frame wireMessage) bool { return frame.Type == "error" && frame.Code == string(errUpstream) })
	var media mediaResponse
	requestJSON(t, guest, http.MethodGet, roomURL+"/playback-url", nil, &media, 200)
	requestJSON(t, guest, http.MethodPost, roomURL+"/ws-ticket", nil, &ticket, 200)
	guestWS := dialRoom(t, guest, server.URL, owner.RoomID, ticket.Ticket, 101)
	if guestState := readSocketUntil(t, guestWS, func(frame wireMessage) bool { return frame.Type == "snapshot" }); guestState.State.Revision != 0 {
		t.Fatalf("identity outage changed the timeline: %#v", guestState.State)
	}
	identityState.Store(http.StatusOK)
	writeSocketFrame(t, hostWS, play)
	readSocketUntil(t, guestWS, func(frame wireMessage) bool { return frame.Type == "snapshot" && frame.State.Revision == 1 })

	identityState.Store(http.StatusUnauthorized)
	play.Action = "pause"
	play.CommandID = "try-revoked-session"
	writeSocketFrame(t, hostWS, play)
	readSocketUntil(t, hostWS, func(frame wireMessage) bool { return frame.Type == "error" && frame.Code == string(errUnauthenticated) })
	var authErr errorResponse
	requestJSON(t, host, http.MethodPost, roomURL+"/invite", nil, &authErr, 401)
	if authErr.Code != string(errUnauthenticated) {
		t.Fatalf("revoked session error = %q", authErr.Code)
	}
	waiting := readSocketUntil(t, guestWS, func(frame wireMessage) bool { return frame.Type == "snapshot" && !frame.HostConnected })
	if !waiting.State.Playing || waiting.State.Revision != 1 {
		t.Fatalf("revoked account mutated the timeline: %#v", waiting.State)
	}
	identityState.Store(http.StatusOK)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host.Jar.SetCookies(base, []*http.Cookie{{Name: "kr_account", Value: "other-account", Path: "/"}})
	requestJSON(t, host, http.MethodPost, roomURL+"/close", nil, &authErr, 401)
	requestJSON(t, guest, http.MethodGet, roomURL, nil, &visitor, 200)
}

func browser(t *testing.T, base string, host bool) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	if host {
		endpoint, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		jar.SetCookies(endpoint, []*http.Cookie{{Name: "kr_account", Value: "fixture", Path: "/"}})
	}
	return client
}

func requestJSON(t *testing.T, client *http.Client, method, endpoint string, body, result any, status int) {
	t.Helper()
	var requestBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, endpoint, requestBody)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != status {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s = %d %s, want %d", method, endpoint, response.StatusCode, data, status)
	}
	if result != nil {
		if err := json.NewDecoder(response.Body).Decode(result); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRoomHTTPLifecycle(t *testing.T) {
	repo := integrationStore(t)
	_, server := integrationAPI(t, repo)
	guest := browser(t, server.URL, false)
	host := browser(t, server.URL, true)
	var code errorResponse
	requestJSON(t, guest, http.MethodPost, server.URL+"/v1/rooms", createRequest{MediaID: "clip"}, &code, 401)
	if code.Code != string(errUnauthenticated) {
		t.Fatalf("unauthenticated code = %q", code.Code)
	}
	var room roomSession
	requestJSON(t, host, http.MethodPost, server.URL+"/v1/rooms", createRequest{MediaID: "clip"}, &room, 201)
	if room.Role != roleHost || !validCredential(room.Invite) {
		t.Fatalf("host session = %#v", room)
	}
	roomURL := server.URL + "/v1/rooms/" + room.RoomID
	requestJSON(t, guest, http.MethodGet, roomURL+"/playback-url", nil, &code, 403)
	var joined roomSession
	requestJSON(t, guest, http.MethodPost, server.URL+"/v1/join", joinRequest{Invite: room.Invite, Name: "Гость"}, &joined, 200)
	if joined.Role != roleGuest || joined.RoomID != room.RoomID || joined.ParticipantID == room.ParticipantID {
		t.Fatalf("guest session = %#v", joined)
	}
	var returned roomSession
	requestJSON(t, guest, http.MethodPost, server.URL+"/v1/join", joinRequest{Invite: room.Invite, Name: "Кто-то ещё"}, &returned, 200)
	if returned.ParticipantID != joined.ParticipantID || returned.Name != joined.Name {
		t.Fatalf("repeat join session = %#v, want %#v", returned, joined)
	}
	var media mediaResponse
	requestJSON(t, guest, http.MethodGet, roomURL+"/playback-url", nil, &media, 200)
	if media.URL != "/demo-media/clip.mp4" || media.MediaType != "video" {
		t.Fatalf("media = %#v", media)
	}
	requestJSON(t, guest, http.MethodPost, roomURL+"/invite", nil, &code, 403)
	requestJSON(t, guest, http.MethodPost, roomURL+"/close", nil, &code, 403)
	var invite inviteResponse
	requestJSON(t, host, http.MethodPost, roomURL+"/invite", nil, &invite, 200)
	if invite.Invite == room.Invite || !validCredential(invite.Invite) {
		t.Fatalf("rotate invite = %#v", invite)
	}
	requestJSON(t, guest, http.MethodPost, server.URL+"/v1/join", joinRequest{Invite: room.Invite, Name: "Старая ссылка"}, &code, 404)
	if code.Code != string(errInvite) {
		t.Fatalf("old invite error = %q", code.Code)
	}
	requestJSON(t, host, http.MethodPost, roomURL+"/close", nil, nil, 204)
	requestJSON(t, guest, http.MethodGet, roomURL, nil, &code, 410)
	if code.Code != string(errRoomClosed) {
		t.Fatalf("closed room error = %q", code.Code)
	}
}

func TestConcurrentJoinPreservesCapacityAndSeatOwnership(t *testing.T) {
	s := integrationStore(t)
	room, _, err := s.create(t.Context(), account{ID: "account", DisplayName: "Ведущий"},
		catalogMedia{ID: "clip", MediaType: "video", DurationMS: 15000})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		member roomSession
		token  string
		err    error
	}
	const attempts = 10
	var group sync.WaitGroup
	results := make(chan outcome, attempts)
	for range attempts {
		group.Go(func() {
			member, token, err := s.join(t.Context(), room.RoomID, room.Invite, "Гость", "")
			results <- outcome{member: member, token: token, err: err}
		})
	}
	group.Wait()
	close(results)
	var admitted []outcome
	for result := range results {
		if result.err == nil {
			admitted = append(admitted, result)
			continue
		}
		if !errors.Is(result.err, errRoomFull) {
			t.Fatal(result.err)
		}
	}
	if len(admitted) != roomCapacity-1 {
		t.Fatalf("guest capacity = %d", len(admitted))
	}
	guest := admitted[0]
	rejoined, token, err := s.join(t.Context(), room.RoomID, room.Invite, "Дубль", guest.token)
	if err != nil || rejoined.ParticipantID != guest.member.ParticipantID || token != guest.token {
		t.Fatalf("repeat join = %#v, %v", rejoined, err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE room_participants SET seat_expires_at = now() WHERE id = $1`, guest.member.ParticipantID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.join(t.Context(), room.RoomID, room.Invite, "Новый гость", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.session(t.Context(), room.RoomID, guest.token); !errors.Is(err, errRoomFull) {
		t.Fatalf("old guest reclaimed occupied seat: %v", err)
	}
}
