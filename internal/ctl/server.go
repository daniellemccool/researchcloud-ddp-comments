package ctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Request is one operator call: Op is "status", "render" or "stop"; User is
// set only for "stop".
type Request struct {
	Op   string `json:"op"` // status | render | stop
	User string `json:"user,omitempty"`
}

// Response is the desk's answer: OK with Text for "status", OK alone for
// "render" and "stop", or OK false with Error.
type Response struct {
	OK    bool   `json:"ok"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// Handler is what the serving desk exposes on the socket; cmd/desk's ops
// (over *session.Manager) satisfies it.
type Handler interface {
	Status(ctx context.Context) (string, error)
	Render(ctx context.Context) error
	Stop(ctx context.Context, user string) error
}

// handlerTimeout bounds every handler call.
const handlerTimeout = 60 * time.Second

// maxLine bounds one request or response line on the socket.
const maxLine = 1 << 20

// errTooLong is returned by readLine for a line longer than maxLine.
var errTooLong = errors.New("request too long")

// dispatch runs one request against h. An unknown op or a handler error
// becomes OK false with the error text; the caller never sees a transport
// error here.
func dispatch(ctx context.Context, h Handler, req Request) Response {
	switch req.Op {
	case "status":
		text, err := h.Status(ctx)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, Text: text}
	case "render":
		if err := h.Render(ctx); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true}
	case "stop":
		if err := h.Stop(ctx, req.User); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true}
	default:
		return Response{Error: "unknown op " + req.Op}
	}
}

// Serve listens on the unix socket at path and answers one JSON request line
// per connection with one JSON response line, until ctx is cancelled: the
// listener and every live connection are closed and Serve returns nil. A
// stale file at path is removed first — unless it still answers, in which
// case Serve returns "desk already serving at <path>". The socket is created
// inside a fresh 0700 temp directory next to path and renamed into place, so
// it is mode 0600 and never world-connectable even under a permissive umask.
func Serve(ctx context.Context, path string, h Handler) error {
	return serve(ctx, path, h, nil)
}

// ServeReady is Serve with a readiness signal: once the socket is in place at
// path with mode 0600 and listening, it sends one value on ready (never
// blocking) before the first accept.
func ServeReady(ctx context.Context, path string, h Handler, ready chan<- struct{}) error {
	return serve(ctx, path, h, func() {
		select {
		case ready <- struct{}{}:
		default:
		}
	})
}

// serve is Serve/ServeReady with the optional onListen callback.
func serve(ctx context.Context, path string, h Handler, onListen func()) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// A stale socket that still answers means another desk is serving there.
	if conn, err := net.Dial("unix", path); err == nil {
		_ = conn.Close()
		return fmt.Errorf("desk already serving at %s", path)
	}
	// Create the socket inside a fresh 0700 temp directory next to the final
	// path, then rename it into place.
	tmpDir, err := os.MkdirTemp(dir, ".desk-sock-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	tmpPath := filepath.Join(tmpDir, "desk.sock")
	ln, err := net.Listen("unix", tmpPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = ln.Close()
		return fmt.Errorf("remove stale %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = ln.Close()
		return err
	}
	if onListen != nil {
		onListen()
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	conns := map[net.Conn]struct{}{}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		mu.Lock()
		for c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			wg.Wait()
			if ctx.Err() != nil {
				// Closed by the cancelled context: stop cleanly.
				return nil
			}
			return err
		}
		wg.Add(1)
		mu.Lock()
		conns[conn] = struct{}{}
		mu.Unlock()
		go func() {
			defer wg.Done()
			defer func() {
				mu.Lock()
				delete(conns, conn)
				mu.Unlock()
			}()
			serveConn(ctx, conn, h)
		}()
	}
}

// serveConn reads one request line, runs the handler with a 60 s timeout and
// writes one response line.
func serveConn(ctx context.Context, conn net.Conn, h Handler) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(handlerTimeout + 10*time.Second))
	line, err := readLine(conn)
	if err != nil {
		if errors.Is(err, errTooLong) {
			writeResponse(conn, Response{Error: errTooLong.Error()})
		} else {
			writeResponse(conn, Response{Error: "bad request: " + err.Error()})
		}
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(conn, Response{Error: "bad request: " + err.Error()})
		return
	}
	hctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()
	writeResponse(conn, dispatch(hctx, h, req))
}

// readLine reads one newline-terminated line, bounded by maxLine: a longer
// line is errTooLong.
func readLine(conn net.Conn) ([]byte, error) {
	r := bufio.NewReader(io.LimitReader(conn, maxLine+1))
	line, err := r.ReadBytes('\n')
	if len(line) > maxLine {
		return nil, errTooLong
	}
	if err != nil {
		return nil, err
	}
	return line, nil
}

// writeResponse writes one JSON response line; a failed write is dropped (the
// client is gone).
func writeResponse(conn net.Conn, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(data, '\n'))
}
