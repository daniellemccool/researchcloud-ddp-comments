package ctl

import (
	"bufio"
	"encoding/json"
	"net"
	"time"
)

// Call sends one request to the desk listening on the unix socket at path and
// returns its response. A transport failure (no desk, dead socket, timeout)
// is returned as an error; a handler failure arrives as Response.OK false.
func Call(path string, req Request) (Response, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(handlerTimeout + 10*time.Second))

	data, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return Response{}, err
	}
	line, err := bufio.NewReaderSize(conn, maxLine+1).ReadBytes('\n')
	if err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}
