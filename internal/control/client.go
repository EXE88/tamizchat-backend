package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// ErrNotRunning is returned when no server is listening. It is not a failure:
// the panel is perfectly usable against a stopped server, it just cannot do the
// live things.
var ErrNotRunning = errors.New("server is not running")

// dialTimeout is short because the server is on loopback: if it does not answer
// quickly, it is not there.
const dialTimeout = 2 * time.Second

// Client talks to a running server from the panel.
type Client struct {
	path string
}

// NewClient builds a client for the server that owns dbPath.
func NewClient(dbPath string) *Client {
	return &Client{path: EndpointPath(dbPath)}
}

// Running reports whether a server is answering, and its process id.
func (c *Client) Running() (int, bool) {
	e, err := readEndpoint(c.path)
	if err != nil {
		return 0, false
	}
	conn, err := net.DialTimeout("tcp", e.Addr, dialTimeout)
	if err != nil {
		return 0, false
	}
	conn.Close()
	return e.PID, true
}

// Status asks the running server how it is doing.
func (c *Client) Status() (Status, error) {
	var out Status
	return out, c.call(CmdStatus, nil, &out)
}

// Online lists the connected users.
func (c *Client) Online() ([]OnlineUser, error) {
	var out []OnlineUser
	return out, c.call(CmdOnline, nil, &out)
}

// Reload makes the running server re-read everything the panel may have
// changed. This is what turns a panel edit into something that takes effect
// now rather than after a restart.
func (c *Client) Reload() (ReloadResult, error) {
	var out ReloadResult
	return out, c.call(CmdReload, nil, &out)
}

// Kick disconnects a user.
func (c *Client) Kick(clientUUID, reason string) error {
	return c.call(CmdKick, KickArgs{ClientUUID: clientUUID, Reason: reason}, nil)
}

// Notice sends a message to everyone connected.
func (c *Client) Notice(text string) error {
	return c.call(CmdNotice, NoticeArgs{Text: text}, nil)
}

// StopBots silences one bot, or every bot when botID is empty.
func (c *Client) StopBots(botID string) error {
	return c.call(CmdBotStop, BotStopArgs{BotID: botID}, nil)
}

func (c *Client) call(command string, args any, out any) error {
	e, err := readEndpoint(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotRunning
		}
		return err
	}

	conn, err := net.DialTimeout("tcp", e.Addr, dialTimeout)
	if err != nil {
		// A stale endpoint file from a server that is no longer running.
		return ErrNotRunning
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(callTimeout))

	req := Request{Token: e.Token, Command: command}
	if args != nil {
		raw, err := json.Marshal(args)
		if err != nil {
			return err
		}
		req.Args = raw
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send %s: %w", command, err)
	}

	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("read reply to %s: %w", command, err)
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "command failed"
		}
		return errors.New(resp.Error)
	}
	if out != nil && len(resp.Data) > 0 {
		return json.Unmarshal(resp.Data, out)
	}
	return nil
}
