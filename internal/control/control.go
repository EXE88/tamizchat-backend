// Package control is the private channel between the admin panel and a running
// server.
//
// Until now the panel could only write to the database, so every change waited
// for a restart. The panel now writes as before and then tells the running
// process to re-read — and it can also do things that only exist in memory, like
// listing who is online or kicking someone.
//
// The listener binds to loopback on a random port and writes its address and a
// fresh token to a file next to the database, readable only by the user running
// the server. That is deliberately the same trust boundary the database already
// has: anyone who can read the server's data directory can already do anything.
package control

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Commands the panel can send.
const (
	CmdStatus  = "status"
	CmdOnline  = "online"
	CmdReload  = "reload"
	CmdKick    = "kick"
	CmdNotice  = "notice"
	CmdBotStop = "bot.stop"
)

// Request is one command from the panel.
type Request struct {
	Token   string          `json:"token"`
	Command string          `json:"cmd"`
	Args    json.RawMessage `json:"args,omitempty"`
}

// Response is the server's answer.
type Response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Status is what the panel shows about a running server.
type Status struct {
	Version     string `json:"version"`
	PID         int    `json:"pid"`
	StartedAt   int64  `json:"started_at"`
	UptimeSec   int64  `json:"uptime_sec"`
	ListenAddr  string `json:"listen_addr"`
	ServerName  string `json:"server_name"`
	OnlineUsers int    `json:"online_users"`
	Rooms       int    `json:"rooms"`
	Bots        int    `json:"bots"`
	BotsPlaying int    `json:"bots_playing"`
	MediaOK     bool   `json:"media_ok"`
	Goroutines  int    `json:"goroutines"`
	HeapMB      int    `json:"heap_mb"`
}

// OnlineUser is one connected client, as the panel lists them.
type OnlineUser struct {
	ClientUUID string `json:"client_uuid"`
	Username   string `json:"username"`
	RoomID     string `json:"room_id"`
	RoomName   string `json:"room_name"`
	Roles      string `json:"roles"`
	Muted      bool   `json:"muted"`
	Remote     string `json:"remote"`
	OnlineSec  int64  `json:"online_sec"`
}

// KickArgs asks the server to disconnect somebody.
type KickArgs struct {
	ClientUUID string `json:"client_uuid"`
	Reason     string `json:"reason,omitempty"`
}

// NoticeArgs sends a message to everyone connected.
type NoticeArgs struct {
	Text string `json:"text"`
}

// BotStopArgs stops a bot, or all of them when the id is empty.
type BotStopArgs struct {
	BotID string `json:"bot_id,omitempty"`
}

// ReloadResult reports what a reload actually changed, so the panel can say
// something more useful than "done".
type ReloadResult struct {
	Settings int `json:"settings"`
	Rooms    int `json:"rooms"`
	Roles    int `json:"roles"`
	Bots     int `json:"bots"`
}

// endpoint is what the listener publishes for the panel to find.
type endpoint struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

// EndpointPath is where the running server advertises its control address. It
// sits beside the database, because that is the directory both halves of the
// program already agree on.
func EndpointPath(dbPath string) string {
	dir := filepath.Dir(dbPath)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "control.json")
}

func writeEndpoint(path string, e endpoint) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	// Written only for the user running the server: the token in it is the
	// whole authentication.
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write control endpoint: %w", err)
	}
	return nil
}

func readEndpoint(path string) (endpoint, error) {
	var e endpoint
	raw, err := os.ReadFile(path)
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, fmt.Errorf("control endpoint file is not readable: %w", err)
	}
	if e.Addr == "" || e.Token == "" {
		return e, fmt.Errorf("control endpoint file is incomplete")
	}
	return e, nil
}
