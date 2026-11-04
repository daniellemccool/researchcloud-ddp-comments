// Package ctl is the desk's operator API: a unix socket the serving desk
// listens on (<DeskRoot>/state/desk.sock, mode 0600) and the client the
// `desk status|render|stop` commands use. Exactly one process mutates desk
// state — the serving desk — so the operator commands are clients of it, not
// second processes touching the files. One JSON request line per connection,
// one JSON response line back; every handler call runs with a 60 s timeout.
package ctl
