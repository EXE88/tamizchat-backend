package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"time"
)

// callTimeout bounds one command, so a stuck panel connection cannot hold a
// server goroutine forever.
const callTimeout = 15 * time.Second

// Handler is what the rest of the server exposes to the panel. app wires it.
type Handler interface {
	Status(ctx context.Context) Status
	Online(ctx context.Context) []OnlineUser
	Reload(ctx context.Context) (ReloadResult, error)
	Kick(ctx context.Context, args KickArgs) error
	Notice(ctx context.Context, args NoticeArgs) error
	StopBots(ctx context.Context, args BotStopArgs) error
}

// Server accepts panel connections.
type Server struct {
	handler  Handler
	token    string
	path     string
	listener net.Listener
}

// Listen starts the control listener and publishes its address.
func Listen(dbPath string, handler Handler) (*Server, error) {
	// Loopback only. The control channel is for the machine's operator, not
	// for the network.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		listener.Close()
		return nil, err
	}

	s := &Server{
		handler:  handler,
		token:    base64.RawURLEncoding.EncodeToString(raw[:]),
		path:     EndpointPath(dbPath),
		listener: listener,
	}

	if err := writeEndpoint(s.path, endpoint{
		Addr: listener.Addr().String(), Token: s.token, PID: os.Getpid(),
	}); err != nil {
		listener.Close()
		return nil, err
	}

	go s.accept()
	slog.Info("control channel listening", "addr", listener.Addr().String(), "file", s.path)
	return s, nil
}

// Addr is the address the panel connects to.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Close stops listening and removes the endpoint file, so a panel started later
// does not try to talk to a server that is gone.
func (s *Server) Close() error {
	err := s.listener.Close()
	_ = os.Remove(s.path)
	return err
}

func (s *Server) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // the listener was closed
		}
		go s.serve(conn)
	}
}

// serve handles exactly one command per connection. Keeping it to one keeps the
// protocol stateless, and the panel is not chatty enough for that to matter.
func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(callTimeout))

	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeResponse(conn, Response{Error: "malformed request"})
		return
	}

	// The token is compared in constant time even though this is a loopback
	// socket: an attacker who can connect can otherwise guess it byte by byte.
	if subtle.ConstantTimeCompare([]byte(s.token), []byte(req.Token)) != 1 {
		slog.Warn("rejected control command with a bad token", "cmd", req.Command)
		writeResponse(conn, Response{Error: "unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	data, err := s.dispatch(ctx, req)
	if err != nil {
		writeResponse(conn, Response{Error: err.Error()})
		return
	}
	writeResponse(conn, Response{OK: true, Data: data})
}

func (s *Server) dispatch(ctx context.Context, req Request) (json.RawMessage, error) {
	switch req.Command {
	case CmdStatus:
		return encode(s.handler.Status(ctx))

	case CmdOnline:
		return encode(s.handler.Online(ctx))

	case CmdReload:
		result, err := s.handler.Reload(ctx)
		if err != nil {
			return nil, err
		}
		return encode(result)

	case CmdKick:
		var args KickArgs
		if err := decodeArgs(req.Args, &args); err != nil {
			return nil, err
		}
		return nil, s.handler.Kick(ctx, args)

	case CmdNotice:
		var args NoticeArgs
		if err := decodeArgs(req.Args, &args); err != nil {
			return nil, err
		}
		return nil, s.handler.Notice(ctx, args)

	case CmdBotStop:
		var args BotStopArgs
		if err := decodeArgs(req.Args, &args); err != nil {
			return nil, err
		}
		return nil, s.handler.StopBots(ctx, args)

	default:
		return nil, errors.New("unknown command: " + req.Command)
	}
}

func decodeArgs(raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		return errors.New("this command needs arguments")
	}
	return json.Unmarshal(raw, into)
}

func encode(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	return raw, err
}

func writeResponse(conn net.Conn, resp Response) {
	_ = json.NewEncoder(conn).Encode(resp)
}
