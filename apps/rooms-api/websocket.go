package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type roomEvents struct {
	mutex       sync.Mutex
	subscribers map[string]*roomSubscribers
}

type roomSubscribers struct {
	updates     map[chan struct{}]struct{}
	hostSockets int
}

func newRoomEvents() *roomEvents {
	return &roomEvents{subscribers: make(map[string]*roomSubscribers)}
}

func (e *roomEvents) subscribe(room string, host bool) (<-chan struct{}, func()) {
	updates := make(chan struct{}, 1)
	e.mutex.Lock()
	subscribers := e.subscribers[room]
	if subscribers == nil {
		subscribers = &roomSubscribers{updates: make(map[chan struct{}]struct{})}
		e.subscribers[room] = subscribers
	}
	if host {
		subscribers.hostSockets++
		if subscribers.hostSockets == 1 {
			subscribers.notify()
		}
	}
	subscribers.updates[updates] = struct{}{}
	e.mutex.Unlock()
	return updates, func() {
		e.mutex.Lock()
		defer e.mutex.Unlock()
		delete(subscribers.updates, updates)
		if host {
			subscribers.hostSockets--
			if subscribers.hostSockets == 0 {
				subscribers.notify()
			}
		}
		if len(subscribers.updates) == 0 {
			delete(e.subscribers, room)
		}
	}
}

func (e *roomEvents) publish(room string) {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	if subscribers := e.subscribers[room]; subscribers != nil {
		subscribers.notify()
	}
}

func (e *roomEvents) hostConnected(room string) bool {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	subscribers := e.subscribers[room]
	return subscribers != nil && subscribers.hostSockets > 0
}

func (s *roomSubscribers) notify() {
	for updates := range s.updates {
		select {
		case updates <- struct{}{}:
		default:
		}
	}
}

type socketError struct {
	Type string `json:"type"`
	Code string `json:"code"`
}

type socketPong struct {
	Type         string `json:"type"`
	ClientTimeMS int64  `json:"clientTimeMs"`
	ServerTimeMS int64  `json:"serverTimeMs"`
}

func (a *api) websocket(w http.ResponseWriter, r *http.Request, access roomAccess) {
	if a.ctx.Err() != nil || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeError(w, errBadRequest)
		return
	}
	roomID := access.roomID
	p, err := a.repo.consumeTicket(r.Context(), roomID, access.credential, r.URL.Query().Get("ticket"))
	if err != nil {
		writeError(w, err)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{a.cfg.publicOrigin}})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(8192)
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	updates, unsubscribe := a.rooms.subscribe(roomID, p.Role == roleHost)
	defer unsubscribe()
	go func() {
		defer cancel()
		a.readSocket(ctx, conn, access, p)
	}()
	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()
	for {
		snapshot, err := a.repo.snapshot(ctx, roomID)
		if err != nil {
			if ctx.Err() == nil {
				_ = sendSocketError(ctx, conn, err)
			}
			return
		}
		snapshot.HostConnected = a.rooms.hostConnected(roomID)
		if err := sendSocket(ctx, conn, snapshot); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-updates:
		case <-keepAlive.C:
			pingCtx, stopPing := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			stopPing()
			if err != nil {
				return
			}
			if err := a.validateHost(ctx, access.host); err != nil && !errors.Is(err, errUpstream) {
				_ = sendSocketError(ctx, conn, err)
				return
			}
			if _, err := a.repo.session(ctx, roomID, access.credential); err != nil {
				_ = sendSocketError(ctx, conn, err)
				return
			}
		}
	}
}

func (a *api) readSocket(ctx context.Context, conn *websocket.Conn, access roomAccess, p participant) {
	for {
		var message socketMessage
		if err := wsjson.Read(ctx, conn, &message); err != nil {
			return
		}
		if err := a.handleSocket(ctx, conn, access, p, message); err != nil {
			if sendSocketError(ctx, conn, err) != nil || errors.Is(err, errUnauthenticated) {
				return
			}
		}
	}
}

func (a *api) handleSocket(ctx context.Context, conn *websocket.Conn, access roomAccess, p participant, message socketMessage) error {
	switch message.Type {
	case "ping":
		if message.ClientTimeMS == nil {
			return errBadRequest
		}
		return sendSocket(ctx, conn, socketPong{Type: "pong", ClientTimeMS: *message.ClientTimeMS, ServerTimeMS: time.Now().UnixMilli()})
	case "command":
		if p.Role != roleHost || access.host.accountID == "" {
			return errForbidden
		}
		if !message.validCommand() {
			return errBadRequest
		}
		if err := a.validateHost(ctx, access.host); err != nil {
			return err
		}
		if err := a.repo.command(ctx, access.roomID, p.ID, message); err != nil {
			return err
		}
		a.rooms.publish(access.roomID)
		return nil
	default:
		return errBadRequest
	}
}

func sendSocketError(ctx context.Context, conn *websocket.Conn, err error) error {
	status, code := statusAndCode(err)
	if status >= 500 && ctx.Err() == nil {
		slog.Error("room websocket request failed", "error", err)
	}
	return sendSocket(ctx, conn, socketError{Type: "error", Code: code})
}

func sendSocket(ctx context.Context, conn *websocket.Conn, message any) error {
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return wsjson.Write(writeCtx, conn, message)
}
