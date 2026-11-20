package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
)

type rtcDeletion struct {
	roomID   string
	failures int
}

type rtcDeleteRequest struct {
	Room string `json:"room"`
}

type rtcCreateRequest struct {
	Name            string `json:"name"`
	MaxParticipants int    `json:"maxParticipants"`
}

type rtcControlGrant struct {
	Room       string `json:"room"`
	RoomCreate bool   `json:"roomCreate"`
}

type rtcControlClaims struct {
	jwt.RegisteredClaims
	Video rtcControlGrant `json:"video"`
}

type rtcServiceError struct {
	Code string `json:"code"`
}

func markRTCCall(ctx context.Context, tx pgx.Tx, roomID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO room_rtc_calls (room_id) VALUES ($1) ON CONFLICT (room_id) DO NOTHING`, roomID)
	return err
}

func (s store) rtcSession(ctx context.Context, roomID, credential string, prepare func() error) (roomSession, error) {
	if err := s.withMember(ctx, roomID, credential, func(tx pgx.Tx, room storedRoom, _ participant) error {
		return markRTCCall(ctx, tx, room.id)
	}); err != nil {
		return roomSession{}, err
	}
	var session roomSession
	err := s.withMember(ctx, roomID, credential, func(_ pgx.Tx, room storedRoom, p participant) error {
		session = room.session(p)
		return prepare()
	})
	return session, err
}

func (s store) nextRTCDeletion(ctx context.Context) (rtcDeletion, error) {
	var job rtcDeletion
	err := s.pool.QueryRow(ctx, `
        WITH due AS (
            SELECT call.room_id FROM room_rtc_calls call JOIN rooms r ON r.id = call.room_id
            WHERE call.deleted_at IS NULL AND (call.next_delete_at IS NULL OR call.next_delete_at <= now())
                AND (r.closed_at IS NOT NULL OR r.expires_at <= now() OR NOT EXISTS (
                    SELECT 1 FROM room_participants host
                    WHERE host.room_id = r.id AND host.role = 'host' AND host.seat_expires_at > now()
                ))
            ORDER BY call.next_delete_at NULLS FIRST, call.room_id
            LIMIT 1 FOR UPDATE OF r, call SKIP LOCKED
        ), closed AS (
            UPDATE rooms SET closed_at = COALESCE(closed_at, now())
            FROM due WHERE rooms.id = due.room_id RETURNING rooms.id
        )
        UPDATE room_rtc_calls SET next_delete_at = now() + interval '30 seconds'
        FROM closed WHERE room_id = closed.id RETURNING room_id, delete_failures`).Scan(&job.roomID, &job.failures)
	return job, err
}

func (s store) finishRTCDeletion(ctx context.Context, job rtcDeletion, deleted bool) error {
	if deleted {
		_, err := s.pool.Exec(ctx, `UPDATE room_rtc_calls SET deleted_at = now() WHERE room_id = $1`, job.roomID)
		return err
	}
	backoff := min(30, 1<<min(5, job.failures))
	_, err := s.pool.Exec(ctx, `
        UPDATE room_rtc_calls SET delete_failures = LEAST(delete_failures + 1, 6),
            next_delete_at = now() + $2::integer * interval '1 second'
        WHERE room_id = $1`, job.roomID, backoff)
	return err
}

func (a *api) cleanupRTC() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for range 8 {
			ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
			worked, err := a.deleteClosedCall(ctx)
			cancel()
			if err != nil && a.ctx.Err() == nil {
				slog.Error("LiveKit room cleanup deferred", "error", err)
			}
			if !worked || err != nil {
				break
			}
		}
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		case <-a.rtcWake:
		}
	}
}

func (a *api) deleteClosedCall(ctx context.Context) (bool, error) {
	job, err := a.repo.nextRTCDeletion(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	deleteErr := a.controlLiveKitRoom(ctx, job.roomID, "DeleteRoom", rtcDeleteRequest{Room: job.roomID})
	finishErr := a.repo.finishRTCDeletion(ctx, job, deleteErr == nil)
	return true, errors.Join(deleteErr, finishErr)
}

func (a *api) controlLiveKitRoom(ctx context.Context, roomID, method string, payload any) error {
	now := time.Now()
	cfg := a.cfg.rtc
	claims := rtcControlClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: cfg.key, NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)), ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Second)),
		},
		Video: rtcControlGrant{Room: roomID, RoomCreate: true},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(cfg.secret))
	if err != nil {
		return fmt.Errorf("sign LiveKit room control: %w", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.internalURL+"/twirp/livekit.RoomService/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusOK {
		return nil
	}
	if method == "DeleteRoom" && response.StatusCode == http.StatusNotFound {
		var status rtcServiceError
		if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&status); err == nil && status.Code == "not_found" {
			return nil
		}
	}
	return fmt.Errorf("LiveKit %s returned HTTP %d", method, response.StatusCode)
}
