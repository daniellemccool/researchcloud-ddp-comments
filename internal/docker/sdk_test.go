package docker

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

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

	suffix := "sdktest-" + strconv.Itoa(os.Getpid())
	netName := "net-" + suffix
	labels := map[string]string{"desk.owner": "sdktest"}
	t.Cleanup(func() { _ = c.NetworkRemove(ctx, netName) })

	if _, err := c.NetworkCreate(ctx, netName, labels); err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}

	// The network must be internal, checked through the raw SDK client too.
	raw, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("raw client: %v", err)
	}
	rawInsp, err := raw.NetworkInspect(ctx, netName, network.InspectOptions{})
	if err != nil {
		t.Fatalf("raw NetworkInspect: %v", err)
	}
	if !rawInsp.Internal {
		t.Error("raw inspect: Internal = false, want true")
	}

	n, err := c.NetworkInspect(ctx, netName)
	if err != nil {
		t.Fatalf("NetworkInspect: %v", err)
	}
	if n.Subnet == "" {
		t.Error("Subnet is empty, want the first IPAM config's subnet")
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

	if err := c.NetworkRemove(ctx, netName); err != nil {
		t.Fatalf("NetworkRemove: %v", err)
	}
	if err := c.NetworkRemove(ctx, netName); !errors.Is(err, ErrNotFound) {
		t.Errorf("second NetworkRemove err = %v, want ErrNotFound", err)
	}
	if _, err := c.ContainerInspect(ctx, "no-such-container-"+suffix); !errors.Is(err, ErrNotFound) {
		t.Errorf("ContainerInspect of a missing container err = %v, want ErrNotFound", err)
	}
}
