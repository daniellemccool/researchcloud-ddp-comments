// Package docker is the desk's single point of contact with the Docker
// daemon. Every other package talks to the Client interface (or to the Fake
// in tests) and never imports the Docker SDK or os/exec; the depguard rules
// in .golangci.yml confine github.com/docker/docker and os/exec to this
// package. The desk uses it to create and remove a collector's private
// network and container pair when a session starts and stops (ADR-0021),
// to find the bundled 4CAT containers, and to run commands inside them.
package docker
