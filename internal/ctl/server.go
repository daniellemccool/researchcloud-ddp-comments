package ctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// listener is closed and Serve returns nil. A stale file at path (a leftover
// socket or anything else) is removed first; the socket is created mode 0600.
func Serve(ctx context.Context, path string, h Handler) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// A stale socket file would make Listen fail; anything else at the path
	// is replaced as well.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale %s: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return err
	}

	var wg sync.WaitGroup
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				// Closed by the cancelled context: stop cleanly.
				wg.Wait()
				return nil
			}
			wg.Wait()
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
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
		writeResponse(conn, Response{Error: "bad request: " + err.Error()})
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

// readLine reads one newline-terminated line, bounded by maxLine.
func readLine(conn net.Conn) ([]byte, error) {
	r := bufio.NewReaderSize(conn, maxLine+1)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) > maxLine {
		return nil, errors.New("line too long")
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
