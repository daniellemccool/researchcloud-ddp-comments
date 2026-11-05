package docker

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// sdkClient implements Client against the real daemon.
type sdkClient struct {
	cli *client.Client
}

// wrapErr classifies a daemon error by class AND message, keeping the daemon's
// error chain in the wrap (two %w verbs, so errors.Is matches both the sentinel
// and the daemon error). The daemon reports some conditions the desk cares
// about as 403/500 with a telling message rather than 404/409: a second
// NetworkConnect of a running container ("already exists in network"), a
// NetworkRemove with attached endpoints ("has active endpoints"), and a
// NetworkDisconnect of a container that is not attached ("is not connected to").
func wrapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case cerrdefs.IsNotFound(err):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case cerrdefs.IsConflict(err):
		return fmt.Errorf("%w: %w", ErrConflict, err)
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "already exists in network"),
		strings.Contains(msg, "has active endpoints"):
		return fmt.Errorf("%w: %w", ErrConflict, err)
	case strings.Contains(msg, "is not connected to"):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	default:
		return err
	}
}

// labelFilters builds a filter matching every given label as key=value.
func labelFilters(labels map[string]string) filters.Args {
	args := filters.NewArgs()
	for k, v := range labels {
		args.Add("label", k+"="+v)
	}
	return args
}

// apiAtLeast reports whether the negotiated API version is at least major.minor.
func apiAtLeast(version string, major, minor int) bool {
	var maj, min int
	if _, err := fmt.Sscanf(version, "%d.%d", &maj, &min); err != nil {
		return false
	}
	if maj != major {
		return maj > major
	}
	return min >= minor
}

func networkFromInspect(n network.Inspect) Network {
	out := Network{ID: n.ID, Name: n.Name, Labels: n.Labels, Options: n.Options}
	if len(n.IPAM.Config) > 0 {
		out.Subnet = n.IPAM.Config[0].Subnet
	}
	return out
}

func (s *sdkClient) NetworkCreate(ctx context.Context, name string, labels, opts map[string]string) (Network, error) {
	// ADR-0007: collector networks are internal bridges with no gateway;
	// opts carries the driver options (e.g. the isolated gateway mode) that
	// keep the bridge gateway itself unreachable from the network.
	resp, err := s.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Internal: true,
		Driver:   "bridge",
		Labels:   labels,
		Options:  opts,
	})
	if err != nil {
		return Network{}, wrapErr(err)
	}
	return s.NetworkInspect(ctx, resp.ID)
}

func (s *sdkClient) NetworkInspect(ctx context.Context, name string) (Network, error) {
	n, err := s.cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		return Network{}, wrapErr(err)
	}
	return networkFromInspect(n), nil
}

func (s *sdkClient) NetworkList(ctx context.Context, labels map[string]string) ([]Network, error) {
	nets, err := s.cli.NetworkList(ctx, network.ListOptions{Filters: labelFilters(labels)})
	if err != nil {
		return nil, wrapErr(err)
	}
	out := make([]Network, 0, len(nets))
	for _, n := range nets {
		out = append(out, networkFromInspect(n))
	}
	return out, nil
}

func (s *sdkClient) NetworkRemove(ctx context.Context, name string) error {
	return wrapErr(s.cli.NetworkRemove(ctx, name))
}

func (s *sdkClient) NetworkConnect(ctx context.Context, networkName, containerName string) error {
	return wrapErr(s.cli.NetworkConnect(ctx, networkName, containerName, nil))
}

func (s *sdkClient) NetworkDisconnect(ctx context.Context, networkName, containerName string) error {
	return wrapErr(s.cli.NetworkDisconnect(ctx, networkName, containerName, false))
}

func (s *sdkClient) ContainerCreate(ctx context.Context, spec ContainerSpec) (string, error) {
	cfg := &container.Config{
		Image:  spec.Image,
		Cmd:    spec.Cmd,
		Env:    spec.Env,
		Labels: spec.Labels,
		User:   spec.User,
	}
	hc := hostConfig(spec)
	nc := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	for _, ep := range spec.Endpoints {
		nc.EndpointsConfig[ep.Network] = &network.EndpointSettings{Aliases: ep.Aliases}
	}
	// Attaching to several networks at create time needs API 1.44 or newer.
	// When the negotiated version is older, create with the first endpoint
	// only and NetworkConnect the remaining networks before returning.
	if len(spec.Endpoints) > 1 {
		s.cli.NegotiateAPIVersion(ctx)
		if !apiAtLeast(s.cli.ClientVersion(), 1, 44) {
			first := spec.Endpoints[0]
			nc.EndpointsConfig = map[string]*network.EndpointSettings{
				first.Network: {Aliases: first.Aliases},
			}
			resp, err := s.cli.ContainerCreate(ctx, cfg, hc, nc, nil, spec.Name)
			if err != nil {
				return "", wrapErr(err)
			}
			for _, ep := range spec.Endpoints[1:] {
				settings := &network.EndpointSettings{Aliases: ep.Aliases}
				if err := s.cli.NetworkConnect(ctx, ep.Network, resp.ID, settings); err != nil {
					// Leave no half-attached container behind.
					_ = s.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
					return "", wrapErr(err)
				}
			}
			return resp.ID, nil
		}
	}
	resp, err := s.cli.ContainerCreate(ctx, cfg, hc, nc, nil, spec.Name)
	if err != nil {
		return "", wrapErr(err)
	}
	return resp.ID, nil
}

func (s *sdkClient) ContainerStart(ctx context.Context, name string) error {
	return wrapErr(s.cli.ContainerStart(ctx, name, container.StartOptions{}))
}

func (s *sdkClient) ContainerStop(ctx context.Context, name string, timeout time.Duration) error {
	// The daemon's timeout is whole seconds; round a sub-second timeout up
	// to 1, never down to 0 (0 would mean SIGKILL immediately).
	secs := int(timeout.Seconds())
	if secs < 1 {
		secs = 1
	}
	return wrapErr(s.cli.ContainerStop(ctx, name, container.StopOptions{Timeout: &secs}))
}

func (s *sdkClient) ContainerRemove(ctx context.Context, name string) error {
	return wrapErr(s.cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true, RemoveVolumes: true}))
}

func (s *sdkClient) ContainerInspect(ctx context.Context, name string) (Container, error) {
	resp, err := s.cli.ContainerInspect(ctx, name)
	if err != nil {
		return Container{}, wrapErr(err)
	}
	out := Container{
		ID:     resp.ID,
		Name:   strings.TrimPrefix(resp.Name, "/"),
		Image:  resp.Config.Image,
		Labels: resp.Config.Labels,
		IPs:    map[string]string{},
	}
	if resp.HostConfig != nil {
		out.Memory = resp.HostConfig.Memory
	}
	if resp.State != nil {
		out.Running = resp.State.Running
		out.ExitCode = resp.State.ExitCode
	}
	if resp.NetworkSettings != nil {
		for netName, ep := range resp.NetworkSettings.Networks {
			out.Networks = append(out.Networks, netName)
			if ep.IPAddress != "" {
				out.IPs[netName] = ep.IPAddress
			}
		}
	}
	sort.Strings(out.Networks)
	return out, nil
}

func (s *sdkClient) ContainerList(ctx context.Context, labels map[string]string) ([]Container, error) {
	summaries, err := s.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: labelFilters(labels)})
	if err != nil {
		return nil, wrapErr(err)
	}
	out := make([]Container, 0, len(summaries))
	for _, sum := range summaries {
		c := Container{
			ID:      sum.ID,
			Image:   sum.Image,
			Labels:  sum.Labels,
			Running: sum.State == "running",
			IPs:     map[string]string{},
		}
		if len(sum.Names) > 0 {
			c.Name = strings.TrimPrefix(sum.Names[0], "/")
		}
		if sum.NetworkSettings != nil {
			for netName, ep := range sum.NetworkSettings.Networks {
				c.Networks = append(c.Networks, netName)
				if ep.IPAddress != "" {
					c.IPs[netName] = ep.IPAddress
				}
			}
		}
		sort.Strings(c.Networks)
		out = append(out, c)
	}
	return out, nil
}

func (s *sdkClient) Exec(ctx context.Context, containerName string, cmd []string, env []string, stdin []byte) (Exec, error) {
	created, err := s.cli.ContainerExecCreate(ctx, containerName, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  len(stdin) > 0,
		Env:          env,
		Cmd:          cmd,
	})
	if err != nil {
		return Exec{}, wrapErr(err)
	}
	resp, err := s.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return Exec{}, wrapErr(err)
	}
	defer resp.Close()

	var stdout, stderr bytes.Buffer
	if len(stdin) > 0 {
		// Write stdin in its own goroutine (then close the write side for
		// EOF) so a large stdin and a large output cannot deadlock. The
		// goroutine exits on its own once the connection is closed.
		go func() {
			_, _ = resp.Conn.Write(stdin)
			if cw, ok := resp.Conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
		}()
	}
	// After the hijack the connection is no longer bound to ctx, so run the
	// demultiplexing copy in a goroutine and abandon it when ctx is done.
	copyDone := make(chan error, 1)
	go func() {
		_, cerr := stdcopy.StdCopy(&stdout, &stderr, resp.Reader)
		copyDone <- cerr
	}()
	select {
	case <-ctx.Done():
		resp.Close()
		return Exec{}, ctx.Err()
	case err := <-copyDone:
		if err != nil {
			// A cancellation that races the copy failure is reported as the
			// cancellation.
			if ctx.Err() != nil {
				return Exec{}, ctx.Err()
			}
			return Exec{}, err
		}
	}
	insp, err := s.cli.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return Exec{}, wrapErr(err)
	}
	return Exec{ExitCode: insp.ExitCode, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

func (s *sdkClient) ServerVersion(ctx context.Context) (Version, error) {
	v, err := s.cli.ServerVersion(ctx)
	if err != nil {
		return Version{}, wrapErr(err)
	}
	return Version{Engine: v.Version, API: v.APIVersion}, nil
}

// hostConfig builds the HostConfig of a created container from its spec:
// the bind mounts, the shm size, the restart policy and the memory limits
// (ADR-0023). No PortBindings and PublishAllPorts stays false: nothing is
// ever published (ADR-0006).
func hostConfig(spec ContainerSpec) *container.HostConfig {
	mounts := make([]mount.Mount, 0, len(spec.Mounts))
	for _, m := range spec.Mounts {
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	hc := &container.HostConfig{
		Mounts:  mounts,
		ShmSize: spec.ShmSize,
		Resources: container.Resources{
			Memory:     spec.Memory,
			MemorySwap: spec.MemorySwap,
		},
	}
	if spec.Restart != "" {
		hc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(spec.Restart)}
	}
	return hc
}
