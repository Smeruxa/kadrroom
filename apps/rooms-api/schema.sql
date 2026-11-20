CREATE TABLE IF NOT EXISTS rooms (
    id text PRIMARY KEY,
    host_account_id text NOT NULL,
    media_id text NOT NULL,
    media_type text NOT NULL CHECK (media_type IN ('audio', 'video')),
    duration_ms bigint NOT NULL CHECK (duration_ms > 0),
    invite_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    closed_at timestamptz,
    playing boolean NOT NULL DEFAULT false,
    anchor_position_ms bigint NOT NULL DEFAULT 0,
    anchor_server_time_ms bigint NOT NULL,
    revision bigint NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS room_participants (
    room_id text NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    id text NOT NULL,
    name text NOT NULL,
    role text NOT NULL CHECK (role IN ('host', 'guest')),
    session_hash bytea NOT NULL,
    seat_expires_at timestamptz NOT NULL,
    PRIMARY KEY (room_id, id),
    UNIQUE (room_id, session_hash)
);

CREATE TABLE IF NOT EXISTS room_tickets (
    ticket_hash bytea PRIMARY KEY,
    room_id text NOT NULL,
    participant_id text NOT NULL,
    expires_at timestamptz NOT NULL,
    FOREIGN KEY (room_id, participant_id) REFERENCES room_participants(room_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS room_tickets_expiry ON room_tickets(expires_at);

CREATE TABLE IF NOT EXISTS room_playback_commands (
    room_id text NOT NULL,
    participant_id text NOT NULL,
    command_id text NOT NULL,
    PRIMARY KEY (room_id, participant_id, command_id),
    FOREIGN KEY (room_id, participant_id) REFERENCES room_participants(room_id, id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS room_rtc_calls (
    room_id text PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
    next_delete_at timestamptz,
    deleted_at timestamptz,
    delete_failures integer NOT NULL DEFAULT 0
);
