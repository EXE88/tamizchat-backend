package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"tamizchat/internal/access"
	"tamizchat/internal/bots"
	"tamizchat/internal/config"
	"tamizchat/internal/control"
	"tamizchat/internal/media"
	"tamizchat/internal/protocol"
	"tamizchat/internal/rooms"
	"tamizchat/internal/session"
	"tamizchat/internal/storage"
	"tamizchat/internal/version"
)

// controlHandler is what the admin panel can do to a running server. It is the
// answer to the limitation every earlier phase carried: the panel wrote to the
// database and the change waited for a restart.
type controlHandler struct {
	cfg       *config.Config
	sessions  *session.Manager
	rooms     *rooms.Manager
	access    *access.Manager
	bots      *bots.Manager
	media     *media.Manager
	startedAt time.Time
	pid       int
	// onRoles re-caches every connected user's roles after a reload, so a role
	// granted from the panel takes effect for people already online.
	onRoles func()
}

func (h controlHandler) Status(context.Context) control.Status {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	playing := 0
	for _, b := range h.bots.Views() {
		if b.State == protocol.BotPlaying {
			playing++
		}
	}

	return control.Status{
		Version:     version.Version,
		PID:         h.pid,
		StartedAt:   h.startedAt.Unix(),
		UptimeSec:   int64(time.Since(h.startedAt).Seconds()),
		ListenAddr:  h.cfg.String(config.KeyListenAddr),
		ServerName:  h.cfg.String(config.KeyServerName),
		OnlineUsers: h.sessions.Count(),
		Rooms:       h.rooms.Count(),
		Bots:        len(h.bots.Views()),
		BotsPlaying: playing,
		MediaOK:     h.media.Enabled(),
		Goroutines:  runtime.NumGoroutine(),
		HeapMB:      int(mem.HeapAlloc >> 20),
	}
}

func (h controlHandler) Online(context.Context) []control.OnlineUser {
	roomNames := make(map[string]string)
	for _, room := range h.rooms.List() {
		roomNames[room.ID()] = room.Name()
	}

	sessions := h.sessions.Sessions()
	out := make([]control.OnlineUser, 0, len(sessions))
	for _, sess := range sessions {
		user := sess.User()

		names := make([]string, 0, len(user.Roles))
		for _, role := range h.access.Roles(sess.ClientUUID) {
			names = append(names, role.Name)
		}

		out = append(out, control.OnlineUser{
			ClientUUID: sess.ClientUUID,
			Username:   user.Username,
			RoomID:     user.RoomID,
			RoomName:   roomNames[user.RoomID],
			Roles:      strings.Join(names, ", "),
			Muted:      user.Muted,
			Remote:     sess.RemoteAddr,
			OnlineSec:  int64(time.Since(sess.JoinedAt).Seconds()),
		})
	}
	return out
}

// Reload re-reads everything the panel may have written. Each part reports what
// it found so the panel can say something concrete.
func (h controlHandler) Reload(ctx context.Context) (control.ReloadResult, error) {
	var result control.ReloadResult
	var err error

	if result.Settings, err = h.cfg.Reload(ctx); err != nil {
		return result, fmt.Errorf("settings: %w", err)
	}
	if result.Roles, err = h.access.Reload(ctx); err != nil {
		return result, fmt.Errorf("roles: %w", err)
	}
	if result.Rooms, err = h.rooms.Reload(ctx); err != nil {
		return result, fmt.Errorf("rooms: %w", err)
	}
	if err = h.bots.Reload(ctx); err != nil {
		return result, fmt.Errorf("bots: %w", err)
	}
	result.Bots = len(h.bots.Views())

	// Roles are cached on each session for presence payloads; a reload that
	// changed them has to reach the people already connected.
	if h.onRoles != nil {
		h.onRoles()
	}

	slog.Info("reloaded from the control channel",
		"settings", result.Settings, "roles", result.Roles,
		"rooms", result.Rooms, "bots", result.Bots)
	return result, nil
}

func (h controlHandler) Kick(ctx context.Context, args control.KickArgs) error {
	sess, ok := h.sessions.Get(strings.TrimSpace(args.ClientUUID))
	if !ok {
		return errors.New("that user is not online")
	}

	event := protocol.Moderation{
		ClientUUID: sess.ClientUUID,
		Username:   sess.Username(),
		ByUsername: "Admin panel",
		Reason:     args.Reason,
	}
	_ = sess.SendMessage(protocol.TypeUserKicked, "", event)
	h.sessions.Broadcast(protocol.TypeUserKicked, event, sess.ClientUUID)
	sess.Close(protocol.ReasonKicked)

	h.access.Log(ctx, storage.ModEntry{
		ActorName: "panel", Action: "kick",
		TargetUUID: sess.ClientUUID, TargetName: sess.Username(),
		Detail: args.Reason,
	})
	return nil
}

func (h controlHandler) Notice(ctx context.Context, args control.NoticeArgs) error {
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return errors.New("the notice text is empty")
	}

	h.sessions.Broadcast(protocol.TypeServerNotice, protocol.ServerNotice{
		Text: text,
		From: "Admin panel",
	}, "")

	h.access.Log(ctx, storage.ModEntry{ActorName: "panel", Action: "notice", Detail: text})
	return nil
}

func (h controlHandler) StopBots(ctx context.Context, args control.BotStopArgs) error {
	if id := strings.TrimSpace(args.BotID); id != "" {
		_, err := h.bots.Control(ctx, "", protocol.BotControl{
			BotID: id, Action: protocol.BotActionStop,
		})
		return err
	}

	for _, view := range h.bots.Views() {
		if view.State != protocol.BotPlaying {
			continue
		}
		if _, err := h.bots.Control(ctx, "", protocol.BotControl{
			BotID: view.ID, Action: protocol.BotActionStop,
		}); err != nil {
			return err
		}
	}
	return nil
}
