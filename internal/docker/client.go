package docker

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/docker/docker/client"
)

// ErrNotFound wraps the daemon's 404; errors.Is(err, ErrNotFound) is true for
// every error this package returns for a missing object. ErrConflict wraps
// the daemon's 409 the same way.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Client is the narrow Docker surface the rest of the desk uses. The only
// production implementation is the SDK client built by New; tests use Fake.
type Client interface {
	// NetworkCreate creates an internal bridge network with the given labels
	// and bridge driver options. opts are the driver options of
	// network.CreateOptions.Options, e.g. the isolated gateway mode of
	// ADR-0007; nil means no options.
	NetworkCreate(ctx context.Context, name string, labels, opts map[string]string) (Network, error)
	NetworkInspect(ctx context.Context, name string) (Network, error)
	// NetworkList returns every network carrying all the given labels.
	NetworkList(ctx context.Context, labels map[string]string) ([]Network, error)
	NetworkRemove(ctx context.Context, name string) error
	NetworkConnect(ctx context.Context, network, container string) error
	NetworkDisconnect(ctx context.Context, network, container string) error
	ContainerCreate(ctx context.Context, spec ContainerSpec) (id string, err error)
	ContainerStart(ctx context.Context, name string) error
	// ContainerStop sends SIGTERM and kills the container after timeout.
	ContainerStop(ctx context.Context, name string, timeout time.Duration) error
	// ContainerRemove removes the container with force and anonymous volumes.
	ContainerRemove(ctx context.Context, name string) error
	ContainerInspect(ctx context.Context, name string) (Container, error)
	// ContainerList returns containers in all states carrying all the given
	// labels. ExitCode is always 0: the list API cannot report it.
	ContainerList(ctx context.Context, labels map[string]string) ([]Container, error)
	Exec(ctx context.Context, container string, cmd []string, env []string, stdin []byte) (Exec, error)
}

// Network is a Docker network as the desk sees it.
type Network struct {
	ID, Name, Subnet string // Subnet: the first IPAM config's subnet, e.g. "172.20.0.0/16"
	Labels           map[string]string
	Options          map[string]string // the bridge driver options the network was created with, from inspect
}

// Container is a Docker container as the desk sees it.
type Container struct {
	ID, Name, Image string // Name without the leading "/"
	Running         bool
	ExitCode        int
	Labels          map[string]string
	Networks        []string          // names of the networks the container is attached to
	IPs             map[string]string // network name -> IPv4 address on that network; a network without an address has no key
}

// Mount is a bind mount.
type Mount struct {
	Source, Target string
	ReadOnly       bool
}

// Endpoint is one network a container joins at create time, with its aliases.
type Endpoint struct {
	Network string
	Aliases []string
}

// ContainerSpec describes a container to create. No published ports are ever
// set (ADR-0006).
type ContainerSpec struct {
	Name, Image string
	Cmd, Env    []string
	Labels      map[string]string
	Mounts      []Mount
	Endpoints   []Endpoint // every network the container joins at create time, with its aliases
	ShmSize     int64      // bytes; 0 = daemon default
	Restart     string     // "unless-stopped" or ""
	User        string     // "" = the image's default
}

// Exec is the result of running a command inside a container.
type Exec struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// New returns a Client backed by the Docker SDK, configured from the
// environment with API version negotiation.
func New() (Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &sdkClient{cli: cli}, nil
}

// SelfID returns the host name, which inside a container is the container id.
func SelfID() (string, error) {
	return os.Hostname()
}
