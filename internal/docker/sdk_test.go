package docker

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// ensureImage pulls ref through the raw SDK client when it is not present.
func ensureImage(t *testing.T, ctx context.Context, raw *client.Client, ref string) {
	t.Helper()
	images, err := raw.ImageList(ctx, image.ListOptions{})
	if err != nil {
		t.Fatalf("ImageList: %v", err)
	}
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == ref {
				return
			}
		}
	}
	reader, err := raw.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		t.Fatalf("ImagePull %s: %v", ref, err)
	}
	defer func() { _ = reader.Close() }()
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatalf("draining pull output for %s: %v", ref, err)
	}
}

// TestSDKAgainstDaemon exercises the real SDK client against the local daemon.
// It skips when no daemon socket is mounted into the test environment.
func TestSDKAgainstDaemon(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("no /var/run/docker.sock: daemon test skipped")
	}
	ctx := context.Background()
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	raw, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("raw client: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	suffix := "sdktest-" + strconv.Itoa(os.Getpid())
	netName := "net-" + suffix
	ctrName := "sdktest-" + suffix
	labels := map[string]string{"desk.owner": "sdktest"}
	t.Cleanup(func() {
		_ = c.ContainerRemove(ctx, ctrName)
		_ = c.NetworkRemove(ctx, netName)
	})

	// ADR-0007: the throwaway network is created with Docker's isolated
	// gateway mode, and the inspected network must report the option.
	opts := map[string]string{"com.docker.network.bridge.gateway_mode_ipv4": "isolated"}
	if _, err := c.NetworkCreate(ctx, netName, labels, opts); err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}

	// The network must be internal, checked through the raw SDK client too.
	rawInsp, err := raw.NetworkInspect(ctx, netName, network.InspectOptions{})
	if err != nil {
		t.Fatalf("raw NetworkInspect: %v", err)
	}
	if !rawInsp.Internal {
		t.Error("raw inspect: Internal = false, want true")
	}
	if got := rawInsp.Options["com.docker.network.bridge.gateway_mode_ipv4"]; got != "isolated" {
		t.Errorf("raw inspect: Options[gateway_mode_ipv4] = %q, want %q (options %v)", got, "isolated", rawInsp.Options)
	}

	n, err := c.NetworkInspect(ctx, netName)
	if err != nil {
		t.Fatalf("NetworkInspect: %v", err)
	}
	if n.Subnet == "" {
		t.Error("Subnet is empty, want the first IPAM config's subnet")
	}
	if got := n.Options["com.docker.network.bridge.gateway_mode_ipv4"]; got != "isolated" {
		t.Errorf("NetworkInspect Options[gateway_mode_ipv4] = %q, want %q (options %v)", got, "isolated", n.Options)
	}

	list, err := c.NetworkList(ctx, labels)
	if err != nil {
		t.Fatalf("NetworkList: %v", err)
	}
	found := false
	for _, got := range list {
		if got.Name == netName {
			found = true
		}
	}
	if !found {
		t.Errorf("NetworkList(%v) = %v, want it to contain %s", labels, list, netName)
	}

	// A tiny container on the network, to prove the error classification of
	// the daemon's 403/500 responses.
	ensureImage(t, ctx, raw, "alpine:3.20")
	spec := ContainerSpec{
		Name:      ctrName,
		Image:     "alpine:3.20",
		Cmd:       []string{"sleep", "30"},
		Labels:    labels,
		Endpoints: []Endpoint{{Network: netName}},
	}
	if _, err := c.ContainerCreate(ctx, spec); err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	if err := c.ContainerStart(ctx, ctrName); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}
	insp, err := c.ContainerInspect(ctx, ctrName)
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	if !insp.Running {
		t.Error("Running = false after start, want true")
	}
	if insp.IPs[netName] == "" {
		t.Errorf("IPs[%s] = %q after start, want an address", netName, insp.IPs[netName])
	}

	// Second connect of a running container: 403 "already exists in network".
	if err := c.NetworkConnect(ctx, netName, ctrName); !errors.Is(err, ErrConflict) {
		t.Errorf("second NetworkConnect err = %v, want ErrConflict", err)
	}
	// Remove while endpoints are attached: 403 "has active endpoints".
	if err := c.NetworkRemove(ctx, netName); !errors.Is(err, ErrConflict) {
		t.Errorf("NetworkRemove with attached container err = %v, want ErrConflict", err)
	}
	// Disconnect twice: the second is a 500 "is not connected to".
	if err := c.NetworkDisconnect(ctx, netName, ctrName); err != nil {
		t.Fatalf("first NetworkDisconnect: %v", err)
	}
	if err := c.NetworkDisconnect(ctx, netName, ctrName); !errors.Is(err, ErrNotFound) {
		t.Errorf("second NetworkDisconnect err = %v, want ErrNotFound", err)
	}

	if err := c.ContainerStop(ctx, ctrName, 2*time.Second); err != nil {
		t.Fatalf("ContainerStop: %v", err)
	}
	stopped, err := c.ContainerInspect(ctx, ctrName)
	if err != nil {
		t.Fatalf("ContainerInspect after stop: %v", err)
	}
	if stopped.Running {
		t.Error("Running = true after stop, want false")
	}
	if ip, ok := stopped.IPs[netName]; ok && ip != "" {
		t.Errorf("IPs[%s] = %q after stop, want empty or absent", netName, ip)
	}
	if err := c.ContainerRemove(ctx, ctrName); err != nil {
		t.Fatalf("ContainerRemove: %v", err)
	}
	if err := c.NetworkRemove(ctx, netName); err != nil {
		t.Fatalf("NetworkRemove after detach: %v", err)
	}
	if err := c.NetworkRemove(ctx, netName); !errors.Is(err, ErrNotFound) {
		t.Errorf("second NetworkRemove err = %v, want ErrNotFound", err)
	}
	if _, err := c.ContainerInspect(ctx, "no-such-container-"+suffix); !errors.Is(err, ErrNotFound) {
		t.Errorf("ContainerInspect of a missing container err = %v, want ErrNotFound", err)
	}
}

// TestSDKExecTimeout proves Exec honours ctx after the hijack: a command that
// outlives the context deadline returns ctx.Err() promptly. Socket only.
func TestSDKExecTimeout(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("no /var/run/docker.sock: daemon test skipped")
	}
	ctx := context.Background()
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("raw client: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	name := "sdktest-exec-" + strconv.Itoa(os.Getpid())
	t.Cleanup(func() { _ = c.ContainerRemove(ctx, name) })
	ensureImage(t, ctx, raw, "alpine:3.20")
	spec := ContainerSpec{Name: name, Image: "alpine:3.20", Cmd: []string{"sleep", "300"}}
	if _, err := c.ContainerCreate(ctx, spec); err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	if err := c.ContainerStart(ctx, name); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}

	ectx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = c.Exec(ectx, name, []string{"sleep", "10"}, nil, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Exec err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Exec returned after %v, want about 1s (500ms deadline)", elapsed)
	}
}
