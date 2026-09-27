package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"omarchy-omamessages/core"
)

type Request struct {
	Cmd  string         `json:"cmd"`
	Args map[string]any `json:"args,omitempty"`
}

// Str returns a string argument ("" when missing or not a string).
func (r Request) Str(k string) string {
	v, _ := r.Args[k].(string)
	return v
}

// Strs returns a list argument. JSON lists decode as []any; a lone string is
// taken as a one-element list. Missing means nil.
func (r Request) Strs(k string) []string {
	switch v := r.Args[k].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{v}
	}
	return nil
}

// extraCommands holds commands compiled in only by build tags (see
// server_fake.go). They get the same hub and request as handle.
var extraCommands = map[string]func(hub *Hub, req Request) Response{}

type Response struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Result any    `json:"result,omitempty"`
}

// listen binds the control socket. A live daemon already bound there makes
// this return errAlreadyRunning so a second `serve` exits quietly.
var errAlreadyRunning = errors.New("daemon already running")

func listen() (net.Listener, error) {
	path, err := socketPath()
	if err != nil {
		return nil, err
	}
	if conn, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
		mine := samePeer(conn)
		_ = conn.Close()
		if !mine {
			return nil, errForeignPeer
		}
		return nil, errAlreadyRunning
	}
	_ = os.Remove(path)
	// Owner-only from the moment it exists, not after a chmod.
	old := syscall.Umask(0o077)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return ln, nil
}

func serveSocket(ln net.Listener, hub *Hub, quit chan<- struct{}) {
	var once sync.Once
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logger.Warn().Err(err).Msg("Accept failed")
			continue
		}
		go func() {
			defer conn.Close()
			if !samePeer(conn) {
				logger.Warn().Msg("Refused a control connection from another user")
				return
			}
			_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
			line, err := bufio.NewReader(conn).ReadBytes('\n')
			if err != nil && len(line) == 0 {
				return
			}
			var req Request
			if err := json.Unmarshal(line, &req); err != nil {
				writeResp(conn, Response{Error: "bad request: " + err.Error()})
				return
			}
			resp := handle(hub, req, func() { once.Do(func() { close(quit) }) })
			writeResp(conn, resp)
		}()
	}
}

func writeResp(conn net.Conn, r Response) {
	data, _ := json.Marshal(r)
	_, _ = conn.Write(append(data, '\n'))
}

func handle(hub *Hub, req Request, requestQuit func()) Response {
	arg := func(k string) string { return strings.TrimSpace(req.Str(k)) }
	fail := func(err error) Response { return Response{Error: err.Error()} }
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	// routed runs fn against the provider that owns the "id" argument.
	routed := func(fn func(p core.Provider, native string) error) Response {
		if err := hub.DoConv(arg("id"), fn); err != nil {
			return fail(err)
		}
		return Response{OK: true}
	}
	switch req.Cmd {
	case "ping":
		return Response{OK: true, Result: "pong"}
	case "status":
		return Response{OK: true, Result: hub.Status()}
	case "refresh":
		if id := arg("provider"); id != "" {
			if err := hub.Do(core.ProviderID(id), func(p core.Provider) error { return p.Refresh(ctx) }); err != nil {
				return fail(err)
			}
			return Response{OK: true}
		}
		var errs []string
		for _, mp := range hub.Merge().Providers {
			if !mp.Enabled {
				continue
			}
			if err := hub.Do(mp.ID, func(p core.Provider) error { return p.Refresh(ctx) }); err != nil {
				errs = append(errs, mp.Name+": "+err.Error())
			}
		}
		if len(errs) > 0 {
			return fail(errors.New(strings.Join(errs, "; ")))
		}
		return Response{OK: true}
	case "connect", "connect-input", "cancel-connect", "disconnect":
		id := core.ProviderID(arg("provider"))
		err := hub.Do(id, func(p core.Provider) error {
			switch req.Cmd {
			case "connect":
				return p.Connect(ctx, arg("method"))
			case "connect-input":
				return p.ConnectInput(ctx, req.Str("value"))
			case "cancel-connect":
				p.CancelConnect()
				return nil
			}
			return p.Disconnect(ctx)
		})
		if err != nil {
			return fail(err)
		}
		return Response{OK: true}
	case "new":
		pid := core.ProviderID(arg("provider"))
		var native string
		err := hub.Do(pid, func(p core.Provider) error {
			var err error
			native, err = p.StartChat(ctx, arg("to"), req.Str("text"))
			return err
		})
		if native != "" {
			// Even if the first message failed, the chat exists: say which.
			return Response{OK: err == nil, Error: errString(err), Result: core.JoinID(pid, native)}
		}
		if err != nil {
			return fail(err)
		}
		return Response{Error: "no chat was created"}
	case "contacts":
		var list []core.Participant
		err := hub.Do(core.ProviderID(arg("provider")), func(p core.Provider) error {
			var err error
			list, err = p.Contacts(ctx, arg("query"))
			return err
		})
		if err != nil {
			return fail(err)
		}
		if list == nil {
			list = []core.Participant{}
		}
		return Response{OK: true, Result: list}
	case "fetch-media":
		idx, err := strconv.Atoi(arg("idx"))
		if err != nil {
			return fail(errors.New("idx must be a number"))
		}
		var path string
		err = hub.DoConv(arg("id"), func(p core.Provider, native string) error {
			var err error
			path, err = p.FetchMedia(ctx, native, arg("msg"), idx)
			return err
		})
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Result: path}
	case "typing":
		if err := hub.SetTyping(ctx, arg("id"), arg("on") == "true"); err != nil {
			return fail(err)
		}
		return Response{OK: true}
	case "open":
		return routed(func(p core.Provider, native string) error { return p.Open(ctx, native, arg("read") != "false") })
	case "more":
		return routed(func(p core.Provider, native string) error { return p.More(ctx, native) })
	case "send":
		if err := hub.Send(ctx, arg("id"), req.Str("text"), req.Strs("files")); err != nil {
			return fail(err)
		}
		return Response{OK: true}
	case "quit":
		requestQuit()
		return Response{OK: true}
	}
	if cmd, ok := extraCommands[req.Cmd]; ok {
		return cmd(hub, req)
	}
	return Response{Error: "unknown command " + req.Cmd}
}

// call sends one request to a running daemon.
func call(req Request) (Response, error) {
	path, err := socketPath()
	if err != nil {
		return Response{}, err
	}
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return Response{}, errors.New("daemon is not running")
	}
	defer conn.Close()
	// Never hand codes, passwords or cookies to someone else's process.
	if !samePeer(conn) {
		return Response{}, errForeignPeer
	}
	_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
	data, _ := json.Marshal(req)
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return Response{}, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
