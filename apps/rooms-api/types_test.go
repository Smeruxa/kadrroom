package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlaybackAnchors(t *testing.T) {
	initial := playbackState{MediaID: "clip", AnchorPositionMS: 1000, AnchorServerTimeMS: 10000, Revision: 2}
	playing := applyPlayback(initial, 5000, 10300, socketMessage{Action: "play"})
	if !playing.Playing || playing.AnchorPositionMS != 1000 || playing.AnchorServerTimeMS != 10300 || playing.Revision != 3 {
		t.Fatalf("play state = %#v", playing)
	}
	paused := applyPlayback(playing, 5000, 11000, socketMessage{Action: "pause"})
	if paused.Playing || paused.AnchorPositionMS != 1700 || paused.Revision != 4 {
		t.Fatalf("pause state = %#v", paused)
	}
	seek := int64(6000)
	atEnd := applyPlayback(paused, 5000, 11100, socketMessage{Action: "seek", PositionMS: &seek})
	if atEnd.AnchorPositionMS != 5000 || atEnd.Playing {
		t.Fatalf("seek at end = %#v", atEnd)
	}
	reset := applyPlayback(atEnd, 5000, 11200, socketMessage{Action: "play"})
	if !reset.Playing || reset.AnchorPositionMS != 0 {
		t.Fatalf("restart state = %#v", reset)
	}
}

func TestCommandsRequireRevisionAndSeekPosition(t *testing.T) {
	revision, positive, negative := int64(0), int64(1200), int64(-1)
	tests := []struct {
		message socketMessage
		valid   bool
	}{
		{socketMessage{Action: "play", CommandID: "play", ExpectedRevision: &revision}, true},
		{socketMessage{Action: "play", CommandID: "play"}, false},
		{socketMessage{Action: "seek", CommandID: "seek", ExpectedRevision: &revision}, false},
		{socketMessage{Action: "seek", CommandID: "seek", ExpectedRevision: &revision, PositionMS: &positive}, true},
		{socketMessage{Action: "seek", CommandID: "seek", ExpectedRevision: &revision, PositionMS: &negative}, false},
		{socketMessage{Action: "skip", CommandID: "skip", ExpectedRevision: &revision}, false},
	}
	for _, test := range tests {
		if result := test.message.validCommand(); result != test.valid {
			t.Errorf("validCommand(%#v) = %v, want %v", test.message, result, test.valid)
		}
	}
}

func TestConfigValidatesURLsAndSecureDefault(t *testing.T) {
	env := map[string]string{
		"KADRROOM_ROOMS_DATABASE_URL": "postgres://localhost/rooms",
		"KADRROOM_DEMO_MEDIA_URLS":    `{"clip":"/demo/clip.mp4","track":"https://media.test/demo.m4a?track=1"}`,
	}
	getenv := func(name string) string { return env[name] }
	cfg, err := loadConfig(getenv)
	if err != nil || !cfg.secureCookies || cfg.addr != "127.0.0.1:8082" || mediaKind("", cfg.mediaURLs["track"]) != "audio" {
		t.Fatalf("configuration = %#v, %v", cfg, err)
	}
	env["KADRROOM_DEMO_MEDIA_URLS"] = `{"clip":"//external.test/clip.mp4"}`
	if _, err := loadConfig(getenv); err == nil {
		t.Fatal("accepted a protocol-relative media URL")
	}
}

func TestRoomRequestRejectsCrossHostOrigin(t *testing.T) {
	a := &api{}
	request := httptest.NewRequest(http.MethodPost, "http://kadrroom.test/v1/rooms", nil)
	request.Header.Set("Origin", "https://external.test")
	response := httptest.NewRecorder()
	a.routes().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Body.String() != "{\"code\":\"invalid_origin\"}\n" {
		t.Fatalf("cross-origin response = %d %s", response.Code, response.Body)
	}
	status, code := statusAndCode(errors.Join(errUpstream, errors.New("connection failed")))
	if status != http.StatusBadGateway || code != string(errUpstream) {
		t.Fatalf("upstream error = %d %s", status, code)
	}
}
