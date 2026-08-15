package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"tamizchat/internal/authz"
	"tamizchat/internal/bots"
	"tamizchat/internal/protocol"
	"tamizchat/internal/session"
	"tamizchat/internal/storage"
)

// handleBotList returns every bot with what it is doing now. It needs no
// permission: clients render bots in the room tree the same way they render
// people, and the reply says nothing a room member cannot already hear.
func (g *Gateway) handleBotList(sess *session.Session, env protocol.Envelope) {
	_ = sess.SendMessage(protocol.TypeBots, env.ID, protocol.BotList{Bots: g.bots.Views()})
}

func (g *Gateway) handleBotControl(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermControlBots) {
		return
	}

	var req protocol.BotControl
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	view, err := g.bots.Control(ctx, sess.ClientUUID, req)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: "bot_" + req.Action, TargetName: view.Name, Detail: trackTitle(view),
	})
	_ = sess.SendMessage(protocol.TypeBotState, env.ID, view)
}

func (g *Gateway) handleBotMove(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermControlBots) {
		return
	}

	var req protocol.BotMove
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	view, err := g.bots.Move(ctx, sess.ClientUUID, req.BotID, req.RoomID)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: "bot_move", TargetName: view.Name, Detail: req.RoomID,
	})
	_ = sess.SendMessage(protocol.TypeBotState, env.ID, view)
}

// handleBotCreate defines a new bot. This is a heavier job than driving one, so
// it takes manage_bots rather than control_bots: an operator can hand out the
// music controls without handing over the server's bot configuration.
func (g *Gateway) handleBotCreate(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageBots) {
		return
	}

	var req protocol.BotSpec
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	view, err := g.bots.Create(ctx, sess.ClientUUID, req)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: "bot_create", TargetName: view.Name,
	})
	_ = sess.SendMessage(protocol.TypeBotState, env.ID, view)
}

func (g *Gateway) handleBotUpdate(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageBots) {
		return
	}

	var req protocol.BotSpec
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	view, err := g.bots.Update(ctx, sess.ClientUUID, req)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: "bot_update", TargetName: view.Name,
	})
	_ = sess.SendMessage(protocol.TypeBotState, env.ID, view)
}

func (g *Gateway) handleBotDelete(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageBots) {
		return
	}

	var req protocol.BotRef
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	if err := g.bots.Delete(ctx, req.BotID); err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: "bot_delete", TargetName: req.BotID,
	})

	// The actor is excluded from the broadcast and correlates their own reply by
	// id — the same pattern every other administrative action here uses.
	g.sessions.Broadcast(protocol.TypeBotGone, req, sess.ClientUUID)
	_ = sess.SendMessage(protocol.TypeBotGone, env.ID, req)
}

// handleBotQueue returns one bot's full track list. Like bot.list it needs no
// permission: it is what a client shows in the little panel next to a bot, and
// the titles are already audible to anyone in the room.
func (g *Gateway) handleBotQueue(sess *session.Session, env protocol.Envelope) {
	var req protocol.BotRequest
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	queue, err := g.bots.Queue(req.BotID)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeBotQueueReply, env.ID, queue)
}

// handleBotPlaylistList lists a bot's playlists. Filling playlists is an
// administrative job, so unlike the queue this one takes manage_bots.
func (g *Gateway) handleBotPlaylistList(sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageBots) {
		return
	}

	var req protocol.BotRequest
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	list, err := g.bots.Playlists(req.BotID)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeBotPlaylists, env.ID, list)
}

func (g *Gateway) handleBotPlaylistCreate(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	spec, ok := g.decodePlaylistSpec(sess, env)
	if !ok {
		return
	}

	list, err := g.bots.CreatePlaylist(ctx, spec)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeBotPlaylist, env.ID, list)
}

func (g *Gateway) handleBotPlaylistRename(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	spec, ok := g.decodePlaylistSpec(sess, env)
	if !ok {
		return
	}

	list, err := g.bots.RenamePlaylist(ctx, spec)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeBotPlaylist, env.ID, list)
}

func (g *Gateway) handleBotPlaylistDelete(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	spec, ok := g.decodePlaylistSpec(sess, env)
	if !ok {
		return
	}

	if err := g.bots.DeletePlaylist(ctx, sess.ClientUUID, spec); err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeBotPlaylistGone, env.ID, spec)
}

func (g *Gateway) handleBotPlaylistSelect(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	spec, ok := g.decodePlaylistSpec(sess, env)
	if !ok {
		return
	}

	view, err := g.bots.SelectPlaylist(ctx, sess.ClientUUID, spec)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}

	g.access.Log(ctx, storage.ModEntry{
		ActorUUID: sess.ClientUUID, ActorName: sess.Username(),
		Action: "bot_playlist", TargetName: view.Name, Detail: view.PlaylistName,
	})
	_ = sess.SendMessage(protocol.TypeBotState, env.ID, view)
}

// handleBotTrackUpload issues the ticket for one track. The bytes then go over
// HTTP, exactly as room files do: everything that can be refused is refused
// before a single byte is accepted.
func (g *Gateway) handleBotTrackUpload(sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageBots) {
		return
	}

	var req protocol.BotTrackUploadRequest
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	ticket, err := g.bots.RequestTrackUpload(sess.ClientUUID, req)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeBotTrackUploadInfo, env.ID, ticket)
}

func (g *Gateway) handleBotTrackDelete(ctx context.Context, sess *session.Session, env protocol.Envelope) {
	if !g.require(sess, env.ID, authz.PermManageBots) {
		return
	}

	var req protocol.BotTrackRef
	if err := json.Unmarshal(env.Data, &req); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return
	}

	view, err := g.bots.DeleteTrack(ctx, sess.ClientUUID, req)
	if err != nil {
		g.replyBotError(sess, env.ID, err)
		return
	}
	_ = sess.SendMessage(protocol.TypeBotState, env.ID, view)
}

// decodePlaylistSpec handles the permission check and the payload, which every
// playlist message shares.
func (g *Gateway) decodePlaylistSpec(sess *session.Session, env protocol.Envelope) (protocol.BotPlaylistSpec, bool) {
	if !g.require(sess, env.ID, authz.PermManageBots) {
		return protocol.BotPlaylistSpec{}, false
	}

	var spec protocol.BotPlaylistSpec
	if err := json.Unmarshal(env.Data, &spec); err != nil {
		sess.SendError(env.ID, protocol.ErrBadRequest, "the message payload is not valid")
		return protocol.BotPlaylistSpec{}, false
	}
	return spec, true
}

func trackTitle(view protocol.Bot) string {
	if view.Track == nil {
		return ""
	}
	return view.Track.Title
}

func (g *Gateway) replyBotError(sess *session.Session, id string, err error) {
	switch {
	case errors.Is(err, bots.ErrNotFound):
		sess.SendError(id, protocol.ErrBotNotFound, "no such bot exists")
	case errors.Is(err, bots.ErrDisabled):
		sess.SendError(id, protocol.ErrBotDisabled, "that bot is disabled")
	case errors.Is(err, bots.ErrEmptyQueue):
		sess.SendError(id, protocol.ErrBotEmpty, "that bot's music folder is empty")
	case errors.Is(err, bots.ErrBadAction):
		sess.SendError(id, protocol.ErrBotAction, "that command is not recognised for a bot")
	case errors.Is(err, bots.ErrPlaylistNotFound):
		sess.SendError(id, protocol.ErrPlaylistNotFound, "no such playlist exists for that bot")
	case errors.Is(err, bots.ErrPlaylistTaken):
		sess.SendError(id, protocol.ErrPlaylistNameUsed, "that bot already has a playlist by that name")
	case errors.Is(err, bots.ErrTooManyPlaylists):
		sess.SendError(id, protocol.ErrPlaylistTooMany, "that bot already has as many playlists as it allows")
	case errors.Is(err, bots.ErrNotAudio):
		sess.SendError(id, protocol.ErrTrackNotAudio,
			"that file is not one of the audio types this server accepts")
	case errors.Is(err, bots.ErrTrackNotFound):
		sess.SendError(id, protocol.ErrTrackNotFound, "that track is not in the playlist")
	case errors.Is(err, bots.ErrTooLarge):
		sess.SendError(id, protocol.ErrFileTooLarge, "that track is larger than this server allows")
	case errors.Is(err, bots.ErrQuotaFull):
		sess.SendError(id, protocol.ErrRoomQuotaFull, "that bot's music storage is full")
	case errors.Is(err, bots.ErrNameTaken):
		sess.SendError(id, protocol.ErrBotNameUsed, "another bot already uses that name")
	case errors.Is(err, bots.ErrTooManyBots):
		sess.SendError(id, protocol.ErrBotTooMany, "this server already has as many bots as it allows")
	case errors.Is(err, bots.ErrNoRoom):
		sess.SendError(id, protocol.ErrRoomNotFound, "move the bot into a room first")
	case errors.Is(err, bots.ErrNoMedia):
		sess.SendError(id, protocol.ErrMediaDisabled,
			"LiveKit must be configured and enabled to play music")
	default:
		var invalid *bots.ValidationError
		if errors.As(err, &invalid) {
			sess.SendError(id, protocol.ErrInvalidInput, invalid.Msg)
			return
		}
		slog.Error("bot operation failed", "client_uuid", sess.ClientUUID, "err", err)
		sess.SendError(id, protocol.ErrInternal, "internal server error")
	}
}
