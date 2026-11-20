package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	roomLifetime  = 24 * time.Hour
	hostSeatTime  = 10 * time.Minute
	guestSeatTime = 10 * time.Minute
	ticketTime    = 45 * time.Second
	roomCapacity  = 5
	roleHost      = "host"
	roleGuest     = "guest"
)

type roomError string

func (e roomError) Error() string { return string(e) }

const (
	errBadRequest      roomError = "invalid_request"
	errUnauthenticated roomError = "not_authenticated"
	errForbidden       roomError = "forbidden"
	errOrigin          roomError = "invalid_origin"
	errRoomMissing     roomError = "room_not_found"
	errRoomClosed      roomError = "room_closed"
	errInvite          roomError = "invalid_invite"
	errRoomFull        roomError = "room_full"
	errMedia           roomError = "media_unavailable"
	errTicket          roomError = "invalid_ticket"
	errStale           roomError = "stale_revision"
	errUpstream        roomError = "upstream_unavailable"
	errRTC             roomError = "rtc_unavailable"
)

type account struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type catalogMedia struct {
	ID              string  `json:"id"`
	Available       bool    `json:"available"`
	DurationSeconds float64 `json:"durationSeconds"`
	MediaType       string  `json:"mediaType"`
	DurationMS      int64   `json:"-"`
}

type roomSession struct {
	RoomID        string    `json:"roomId"`
	ParticipantID string    `json:"participantId"`
	Name          string    `json:"name"`
	Role          string    `json:"role"`
	MediaID       string    `json:"mediaId"`
	Invite        string    `json:"invite,omitempty"`
	MediaType     string    `json:"-"`
	ExpiresAt     time.Time `json:"-"`
}

type participant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type playbackState struct {
	MediaID            string `json:"mediaId"`
	Playing            bool   `json:"playing"`
	AnchorPositionMS   int64  `json:"anchorPositionMs"`
	AnchorServerTimeMS int64  `json:"anchorServerTimeMs"`
	Revision           int64  `json:"revision"`
}

type roomSnapshot struct {
	Type          string        `json:"type"`
	State         playbackState `json:"state"`
	Participants  []participant `json:"participants"`
	HostConnected bool          `json:"hostConnected"`
}

type socketMessage struct {
	Type             string `json:"type"`
	Action           string `json:"action"`
	PositionMS       *int64 `json:"positionMs"`
	CommandID        string `json:"commandId"`
	ExpectedRevision *int64 `json:"expectedRevision"`
	ClientTimeMS     *int64 `json:"clientTimeMs"`
}

func newCredential() string {
	var value [32]byte
	rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func hashCredential(value string) []byte {
	hash := sha256.Sum256([]byte(value))
	return hash[:]
}

func validCredential(value string) bool {
	if len(value) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil
}

func validName(name string) bool {
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 40 {
		return false
	}
	return strings.IndexFunc(name, unicode.IsControl) == -1
}

func (message socketMessage) validCommand() bool {
	if message.CommandID == "" || len(message.CommandID) > 128 || message.ExpectedRevision == nil || *message.ExpectedRevision < 0 {
		return false
	}
	switch message.Action {
	case "play", "pause":
		return true
	case "seek":
		return message.PositionMS != nil && *message.PositionMS >= 0
	default:
		return false
	}
}

func applyPlayback(current playbackState, duration, nowMS int64, message socketMessage) playbackState {
	position := current.AnchorPositionMS
	if current.Playing {
		position += max(0, nowMS-current.AnchorServerTimeMS)
	}
	playing := current.Playing
	switch message.Action {
	case "play":
		if position >= duration {
			position = 0
		}
		playing = true
	case "pause":
		playing = false
	case "seek":
		position = *message.PositionMS
	}
	position = min(position, duration)
	return playbackState{
		MediaID: current.MediaID, Playing: playing && position < duration,
		AnchorPositionMS: position, AnchorServerTimeMS: nowMS, Revision: current.Revision + 1,
	}
}
