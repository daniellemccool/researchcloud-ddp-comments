// Package render turns the desk's configuration plus a user name and seat
// index into the exact docker.ContainerSpecs the session manager creates for
// a collector's pair (ADR-0021), renders the Firefox enterprise policy that
// forces every request through that user's proxy with the proxy's CA
// installed (ADR-0007), and writes the per-user files on the volume
// (ADR-0013). It imports only the types of internal/docker, never the Docker
// SDK, and publishes no port anywhere (ADR-0006).
package render
