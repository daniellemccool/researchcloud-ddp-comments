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

// wrapErr maps the daemon's 404 to ErrNotFound and 409 to ErrConflict, keeping
// the daemon's message in the wrap; errors.Is works on the result.
func wrapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case cerrdefs.IsNotFound(err):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case cerrdefs.IsConflict(err):
		return fmt.Errorf("%w: %v", ErrConflict, err)
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
	out := Network{ID: n.ID, Name: n.Name, Labels: n.Labels}
	if len(n.IPAM.Config) > 0 {
		out.Subnet = n.IPAM.Config[0].Subnet
	}
	return out
}

func (s *sdkClient) NetworkCreate(ctx context.Context, name string, labels map[string]string) (Network, error) {
	// ADR-0007: collector networks are internal bridges with no gateway.
	resp, err := s.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Internal: true,
		Driver:   "bridge",
		Labels:   labels,
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
		// No PortBindings and PublishAllPorts stays false: nothing is ever
		// published (ADR-0006).
	}
	if spec.Restart != "" {
		hc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(spec.Restart)}
	}
	nc := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	for _, ep := range spec.Endpoints {
		nc.EndpointsConfig[ep.Network] = &network.EndpointSettings{Aliases: ep.Aliases}
	}
	// Attaching to several networks at create time needs API 1.44 or newer.
	// When the negotiated version is older, create with the first endpoint
	// only and NetworkConnect the remaining networks before returning.
	if len(spec.Endpoints) > 1 && !apiAtLeast(s.cli.ClientVersion(), 1, 44) {
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
				return "", wrapErr(err)
			}
		}
		return resp.ID, nil
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
	secs := int(timeout.Seconds())
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
	if resp.State != nil {
		out.Running = resp.State.Running
		out.ExitCode = resp.State.ExitCode
	}
	if resp.NetworkSettings != nil {
		for netName, ep := range resp.NetworkSettings.Networks {
			out.Networks = append(out.Networks, netName)
			out.IPs[netName] = ep.IPAddress
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
				c.IPs[netName] = ep.IPAddress
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
		if _, err := resp.Conn.Write(stdin); err != nil {
			return Exec{}, err
		}
		// Signal EOF on the write side so the command can finish reading.
		if cw, ok := resp.Conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}
	// Demultiplex the raw stream into stdout and stderr.
	if _, err := stdcopy.StdCopy(&stdout, &stderr, resp.Reader); err != nil {
		return Exec{}, err
	}
	insp, err := s.cli.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return Exec{}, wrapErr(err)
	}
	return Exec{ExitCode: insp.ExitCode, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}
