package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type store struct {
	pool *pgxpool.Pool
}

type storedRoom struct {
	id        string
	mediaType string
	duration  int64
	expiresAt time.Time
	state     playbackState
}

func (s store) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(3589711842)`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
}

func roomLock(ctx context.Context, tx pgx.Tx, id string) (storedRoom, error) {
	var room storedRoom
	var open bool
	room.id = id
	err := tx.QueryRow(ctx, `
        SELECT media_id, media_type, duration_ms, expires_at, playing, anchor_position_ms,
               anchor_server_time_ms, revision, closed_at IS NULL AND expires_at > now() AND EXISTS (
                   SELECT 1 FROM room_participants p WHERE p.room_id = rooms.id AND p.role = 'host' AND p.seat_expires_at > now()
               )
        FROM rooms WHERE id = $1 FOR UPDATE`, id).Scan(&room.state.MediaID, &room.mediaType, &room.duration,
		&room.expiresAt, &room.state.Playing, &room.state.AnchorPositionMS, &room.state.AnchorServerTimeMS,
		&room.state.Revision, &open)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedRoom{}, errRoomMissing
	}
	if err != nil {
		return storedRoom{}, err
	}
	if !open {
		return storedRoom{}, errRoomClosed
	}
	return room, nil
}

func (room storedRoom) session(p participant) roomSession {
	return roomSession{RoomID: room.id, ParticipantID: p.ID, Name: p.Name, Role: p.Role,
		MediaID: room.state.MediaID, MediaType: room.mediaType, ExpiresAt: room.expiresAt}
}

func (s store) create(ctx context.Context, host account, media catalogMedia) (roomSession, string, error) {
	now := time.Now()
	expires := now.Add(roomLifetime)
	roomID := rand.Text()
	p := participant{ID: rand.Text(), Name: host.DisplayName, Role: roleHost}
	invite, session := newCredential(), newCredential()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
            INSERT INTO rooms (id, host_account_id, media_id, media_type, duration_ms, invite_hash, expires_at, anchor_server_time_ms)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, roomID, host.ID, media.ID, media.MediaType,
			media.DurationMS, hashCredential(invite), expires, now.UnixMilli())
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
            INSERT INTO room_participants (room_id, id, name, role, session_hash, seat_expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, roomID, p.ID, p.Name, p.Role, hashCredential(session), now.Add(hostSeatTime))
		return err
	})
	return roomSession{RoomID: roomID, ParticipantID: p.ID, Name: p.Name, Role: p.Role, MediaID: media.ID,
		MediaType: media.MediaType, Invite: invite, ExpiresAt: expires}, session, err
}

func (s store) inviteRoom(ctx context.Context, invite string) (string, error) {
	var roomID string
	err := s.pool.QueryRow(ctx, `SELECT id FROM rooms WHERE invite_hash = $1`, hashCredential(invite)).Scan(&roomID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errInvite
	}
	return roomID, err
}

func (s store) join(ctx context.Context, roomID, invite, name, credential string) (roomSession, string, error) {
	var result roomSession
	var token string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		room, err := roomLock(ctx, tx, roomID)
		if err != nil {
			return err
		}
		var validInvite bool
		if err := tx.QueryRow(ctx, `SELECT invite_hash = $2 FROM rooms WHERE id = $1`, roomID, hashCredential(invite)).Scan(&validInvite); err != nil {
			return err
		}
		if !validInvite {
			return errInvite
		}
		if validCredential(credential) {
			p, err := member(ctx, tx, room, credential)
			if err == nil {
				result, token = room.session(p), credential
				return nil
			}
			if !errors.Is(err, errForbidden) {
				return err
			}
		}
		if err := seatAvailable(ctx, tx, roomID); err != nil {
			return err
		}
		p := participant{ID: rand.Text(), Name: name, Role: roleGuest}
		token = newCredential()
		if _, err := tx.Exec(ctx, `
            INSERT INTO room_participants (room_id, id, name, role, session_hash, seat_expires_at)
            VALUES ($1, $2, $3, $4, $5, now() + $6::integer * interval '1 second')`, roomID, p.ID, p.Name, p.Role,
			hashCredential(token), int(guestSeatTime/time.Second)); err != nil {
			return err
		}
		result = room.session(p)
		return nil
	})
	return result, token, err
}

func seatAvailable(ctx context.Context, tx pgx.Tx, id string) error {
	var full bool
	err := tx.QueryRow(ctx, `
        SELECT count(*) >= $2 FROM room_participants
        WHERE room_id = $1 AND (role = 'host' OR seat_expires_at > now())`, id, roomCapacity).Scan(&full)
	if err != nil {
		return err
	}
	if full {
		return errRoomFull
	}
	return nil
}

func member(ctx context.Context, tx pgx.Tx, room storedRoom, credential string) (participant, error) {
	var p participant
	var active bool
	err := tx.QueryRow(ctx, `
        SELECT id, name, role, seat_expires_at > now() FROM room_participants
        WHERE room_id = $1 AND session_hash = $2`, room.id, hashCredential(credential)).Scan(&p.ID, &p.Name, &p.Role, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return participant{}, errForbidden
	}
	if err != nil {
		return participant{}, err
	}
	seatTime := guestSeatTime
	if p.Role == roleHost {
		seatTime = hostSeatTime
	} else if !active {
		if err := seatAvailable(ctx, tx, room.id); err != nil {
			return participant{}, err
		}
	}
	_, err = tx.Exec(ctx, `
        UPDATE room_participants SET seat_expires_at = now() + $3::integer * interval '1 second'
		WHERE room_id = $1 AND id = $2`, room.id, p.ID, int(seatTime/time.Second))
	return p, err
}

func (s store) withMember(ctx context.Context, roomID, credential string, operation func(pgx.Tx, storedRoom, participant) error) error {
	if !validCredential(credential) {
		return errForbidden
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		room, err := roomLock(ctx, tx, roomID)
		if err != nil {
			return err
		}
		p, err := member(ctx, tx, room, credential)
		if err != nil {
			return err
		}
		return operation(tx, room, p)
	})
}

func (s store) session(ctx context.Context, roomID, credential string) (roomSession, error) {
	var session roomSession
	err := s.withMember(ctx, roomID, credential, func(_ pgx.Tx, room storedRoom, p participant) error {
		session = room.session(p)
		return nil
	})
	return session, err
}

func (s store) hostForCredential(ctx context.Context, roomID, credential string) (string, error) {
	if !validCredential(credential) {
		return "", nil
	}
	var hostID string
	err := s.pool.QueryRow(ctx, `
        SELECT r.host_account_id FROM rooms r JOIN room_participants p ON p.room_id = r.id
        WHERE r.id = $1 AND p.session_hash = $2 AND p.role = 'host'`, roomID, hashCredential(credential)).Scan(&hostID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return hostID, err
}

func (s store) rotate(ctx context.Context, roomID, credential string) (string, error) {
	invite := newCredential()
	err := s.withMember(ctx, roomID, credential, func(tx pgx.Tx, room storedRoom, p participant) error {
		if p.Role != roleHost {
			return errForbidden
		}
		_, err := tx.Exec(ctx, `UPDATE rooms SET invite_hash = $2 WHERE id = $1`, room.id, hashCredential(invite))
		return err
	})
	return invite, err
}

func (s store) closeRoom(ctx context.Context, roomID, credential string, stopRTC bool) error {
	return s.withMember(ctx, roomID, credential, func(tx pgx.Tx, room storedRoom, p participant) error {
		if p.Role != roleHost {
			return errForbidden
		}
		_, err := tx.Exec(ctx, `UPDATE rooms SET closed_at = now() WHERE id = $1`, room.id)
		if err != nil || !stopRTC {
			return err
		}
		return markRTCCall(ctx, tx, room.id)
	})
}

func (s store) issueTicket(ctx context.Context, roomID, credential string) (string, error) {
	ticket := newCredential()
	err := s.withMember(ctx, roomID, credential, func(tx pgx.Tx, room storedRoom, p participant) error {
		if _, err := tx.Exec(ctx, `DELETE FROM room_tickets WHERE room_id = $1 AND expires_at <= now()`, room.id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
            INSERT INTO room_tickets (ticket_hash, room_id, participant_id, expires_at)
            VALUES ($1, $2, $3, now() + $4::integer * interval '1 second')`, hashCredential(ticket), room.id, p.ID, int(ticketTime/time.Second))
		return err
	})
	return ticket, err
}

func (s store) consumeTicket(ctx context.Context, roomID, credential, ticket string) (participant, error) {
	var result participant
	if !validCredential(ticket) {
		return result, errTicket
	}
	err := s.withMember(ctx, roomID, credential, func(tx pgx.Tx, room storedRoom, p participant) error {
		tag, err := tx.Exec(ctx, `
            DELETE FROM room_tickets
            WHERE ticket_hash = $1 AND room_id = $2 AND participant_id = $3 AND expires_at > now()`,
			hashCredential(ticket), room.id, p.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errTicket
		}
		result = p
		return nil
	})
	return result, err
}

func (s store) snapshot(ctx context.Context, roomID string) (roomSnapshot, error) {
	message := roomSnapshot{Type: "snapshot", Participants: make([]participant, 0, roomCapacity)}
	rows, err := s.pool.Query(ctx, `
        SELECT r.media_id, r.playing, r.anchor_position_ms, r.anchor_server_time_ms, r.revision,
               r.closed_at IS NULL AND r.expires_at > now() AND EXISTS (
                   SELECT 1 FROM room_participants host WHERE host.room_id = r.id AND host.role = 'host' AND host.seat_expires_at > now()
               ), p.id, p.name, p.role
        FROM rooms r JOIN room_participants p ON p.room_id = r.id
        WHERE r.id = $1 AND (p.role = 'host' OR p.seat_expires_at > now())
        ORDER BY (p.role = 'host') DESC, p.id`, roomID)
	if err != nil {
		return roomSnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var p participant
		var open bool
		if err := rows.Scan(&message.State.MediaID, &message.State.Playing, &message.State.AnchorPositionMS,
			&message.State.AnchorServerTimeMS, &message.State.Revision, &open, &p.ID, &p.Name, &p.Role); err != nil {
			return roomSnapshot{}, err
		}
		if !open {
			return roomSnapshot{}, errRoomClosed
		}
		message.Participants = append(message.Participants, p)
	}
	if err := rows.Err(); err != nil {
		return roomSnapshot{}, err
	}
	if len(message.Participants) == 0 {
		return roomSnapshot{}, errRoomMissing
	}
	return message, nil
}

func (s store) command(ctx context.Context, roomID, participantID string, message socketMessage) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		room, err := roomLock(ctx, tx, roomID)
		if err != nil {
			return err
		}
		var repeated bool
		if err := tx.QueryRow(ctx, `
            SELECT EXISTS (SELECT 1 FROM room_playback_commands WHERE room_id = $1 AND participant_id = $2 AND command_id = $3)`,
			room.id, participantID, message.CommandID).Scan(&repeated); err != nil {
			return err
		}
		if repeated {
			return nil
		}
		if *message.ExpectedRevision != room.state.Revision {
			return errStale
		}
		state := applyPlayback(room.state, room.duration, time.Now().UnixMilli(), message)
		if _, err := tx.Exec(ctx, `
            INSERT INTO room_playback_commands (room_id, participant_id, command_id) VALUES ($1, $2, $3)`,
			room.id, participantID, message.CommandID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
            UPDATE rooms SET playing = $2, anchor_position_ms = $3, anchor_server_time_ms = $4, revision = $5 WHERE id = $1`,
			room.id, state.Playing, state.AnchorPositionMS, state.AnchorServerTimeMS, state.Revision)
		return err
	})
}
