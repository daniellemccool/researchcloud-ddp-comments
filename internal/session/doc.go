// Package session manages a collector's session pair: the private network
// net-<user> and the containers desk-proxy-<user> and desk-firefox-<user>
// exist only while the session runs (ADR-0021). Start creates the three
// objects from the rendered spec and reserves the running cap atomically
// under the global lock (ADR-0015); Stop removes them; Reconcile adopts
// running pairs after a desk restart, stops exited or stale ones and logs
// orphans without touching them. The desk itself is a container (SelfID is
// its hostname) and joins net-<user> only to reach the Firefox's noVNC port
// (ADR-0007). The manager refuses to touch any container or network whose
// labels lack desk.owner=desk or name another user, publishes no port, and
// imports neither the Docker SDK nor os/exec.
package session
