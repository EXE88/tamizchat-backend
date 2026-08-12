package access

import (
	"context"
	"log/slog"
	"time"

	"tamizchat/internal/storage"
)

// Sanction is an active ban or mute.
type Sanction struct {
	Kind      string
	Reason    string
	CreatedBy string
	CreatedAt int64
	ExpiresAt int64 // 0 means permanent
}

// Active reports whether the sanction still applies at t.
func (s Sanction) Active(t time.Time) bool {
	return s.ExpiresAt == 0 || s.ExpiresAt > t.Unix()
}

// Ban blocks a user from connecting. duration of zero is permanent.
func (m *Manager) Ban(ctx context.Context, actorUUID, targetUUID, username, reason string, duration time.Duration) (Sanction, error) {
	return m.sanction(ctx, storage.SanctionBan, actorUUID, targetUUID, username, reason, duration)
}

// Mute stops a user from sending messages, without disconnecting them.
func (m *Manager) Mute(ctx context.Context, actorUUID, targetUUID, username, reason string, duration time.Duration) (Sanction, error) {
	return m.sanction(ctx, storage.SanctionMute, actorUUID, targetUUID, username, reason, duration)
}

func (m *Manager) sanction(ctx context.Context, kind, actorUUID, targetUUID, username, reason string, duration time.Duration) (Sanction, error) {
	if targetUUID == actorUUID {
		return Sanction{}, &ValidationError{Msg: "you cannot do that to yourself"}
	}
	if actorUUID != "" && m.OutranksOrEqual(actorUUID, targetUUID) {
		return Sanction{}, ErrOutranked
	}

	var expires int64
	if duration > 0 {
		expires = now().Add(duration).Unix()
	}

	record := storage.Sanction{
		ID:         storage.NewUUID(),
		ClientUUID: targetUUID,
		Kind:       kind,
		Reason:     reason,
		Username:   username,
		CreatedBy:  actorUUID,
		ExpiresAt:  expires,
	}
	if err := m.store.PutSanction(ctx, record); err != nil {
		return Sanction{}, err
	}

	value := Sanction{
		Kind:      kind,
		Reason:    reason,
		CreatedBy: actorUUID,
		CreatedAt: now().Unix(),
		ExpiresAt: expires,
	}

	m.mu.Lock()
	if m.sanctions[targetUUID] == nil {
		m.sanctions[targetUUID] = make(map[string]storage.Sanction)
	}
	m.sanctions[targetUUID][kind] = record
	m.mu.Unlock()

	slog.Info("sanction applied", "kind", kind, "target", targetUUID,
		"by", actorUUID, "until", expires, "reason", reason)
	return value, nil
}

// Lift removes a ban or mute. It reports whether one was in place.
func (m *Manager) Lift(ctx context.Context, kind, targetUUID string) (bool, error) {
	removed, err := m.store.DeleteSanction(ctx, targetUUID, kind)
	if err != nil {
		return false, err
	}

	m.mu.Lock()
	delete(m.sanctions[targetUUID], kind)
	if len(m.sanctions[targetUUID]) == 0 {
		delete(m.sanctions, targetUUID)
	}
	m.mu.Unlock()

	if removed {
		slog.Info("sanction lifted", "kind", kind, "target", targetUUID)
	}
	return removed, nil
}

// BanOf returns the user's active ban, if any.
func (m *Manager) BanOf(clientUUID string) (Sanction, bool) {
	return m.activeSanction(clientUUID, storage.SanctionBan)
}

// IsBanned is the check the handshake makes.
func (m *Manager) IsBanned(clientUUID string) bool {
	_, ok := m.BanOf(clientUUID)
	return ok
}

// MuteOf returns the user's active mute, if any.
func (m *Manager) MuteOf(clientUUID string) (Sanction, bool) {
	return m.activeSanction(clientUUID, storage.SanctionMute)
}

// IsMuted is the check the chat manager makes on every message.
func (m *Manager) IsMuted(clientUUID string) bool {
	_, ok := m.MuteOf(clientUUID)
	return ok
}

// activeSanction looks a sanction up and treats an expired one as absent. The
// expired row is left in the database for the panel's history; the periodic
// purge and the next restart clean it up.
func (m *Manager) activeSanction(clientUUID, kind string) (Sanction, bool) {
	m.mu.RLock()
	record, ok := m.sanctions[clientUUID][kind]
	m.mu.RUnlock()
	if !ok {
		return Sanction{}, false
	}

	value := Sanction{
		Kind:      record.Kind,
		Reason:    record.Reason,
		CreatedBy: record.CreatedBy,
		CreatedAt: record.CreatedAt,
		ExpiresAt: record.ExpiresAt,
	}
	if !value.Active(now()) {
		return Sanction{}, false
	}
	return value, true
}

// ActiveSanctions lists everything still in force, for the panel and the
// admin client.
func (m *Manager) ActiveSanctions() []storage.Sanction {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []storage.Sanction
	for _, kinds := range m.sanctions {
		for _, record := range kinds {
			s := Sanction{Kind: record.Kind, ExpiresAt: record.ExpiresAt}
			if s.Active(now()) {
				out = append(out, record)
			}
		}
	}
	return out
}

// Log records a moderation action. A failure to write the log must not undo the
// action itself, so callers report the error and carry on.
func (m *Manager) Log(ctx context.Context, e storage.ModEntry) {
	if err := m.store.AppendModLog(ctx, e); err != nil {
		slog.Error("moderation log write failed", "action", e.Action, "err", err)
	}
}
