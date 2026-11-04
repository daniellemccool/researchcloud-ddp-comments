package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
)

func TestStartSequence(t *testing.T) {
	f := newFixture(t, 4, "alice")
	var probes []string
	f.m.Probe = func(_ context.Context, url string) bool {
		probes = append(probes, url)
		return true
	}
	res, err := f.m.Start(context.Background(), "alice")
	if err != nil || res != Started {
		t.Fatalf("Start = %v, %v; want Started, nil", res, err)
	}

	// Exact call order after the running-count ContainerList.
	want := []string{
		"NetworkInspect net-alice",
		"ContainerInspect desk-proxy-alice",
		"ContainerInspect desk-firefox-alice",
		"NetworkCreate net-alice",
		"ContainerCreate desk-proxy-alice",
		"ContainerCreate desk-firefox-alice",
		"NetworkConnect net-alice deskself",
		"ContainerStart desk-proxy-alice",
		"ContainerInspect desk-proxy-alice",
		"ContainerStart desk-firefox-alice",
	}
	idx := -1
	for i, c := range f.fake.Calls {
		if c == "ContainerList" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no ContainerList for the running count; calls = %v", f.fake.Calls)
	}
	if got := f.fake.Calls[idx+1:]; !reflect.DeepEqual(got, want) {
		t.Errorf("calls after the count =\n%v\nwant\n%v", got, want)
	}

	// The proxy's recorded spec carries the subnet read back from the
	// created network. The fixture's shared egress bridge is the fake's
	// first network, so net-alice is 10.200.2.0/24.
	spec, ok := f.fake.Specs["desk-proxy-alice"]
	if !ok {
		t.Fatalf("no spec recorded for desk-proxy-alice: %v", f.fake.Created)
	}
	if n := len(spec.Cmd); n < 2 || spec.Cmd[n-2] != "--listen-cidr" || spec.Cmd[n-1] != "10.200.2.0/24" {
		t.Errorf("proxy Cmd = %v, want it to end with --listen-cidr 10.200.2.0/24", spec.Cmd)
	}

	// The network carries the spec's labels plus desk.role=net.
	net, err := f.fake.NetworkInspect(context.Background(), "net-alice")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"desk.owner": "desk", "desk.user": "alice", "desk.generation": "tiktok-1.14.3", "desk.role": "net"} {
		if net.Labels[k] != v {
			t.Errorf("network label %s = %q, want %q (labels %v)", k, net.Labels[k], v, net.Labels)
		}
	}

	// ADR-0007: the pair network is created with Docker's isolated gateway
	// mode, so the bridge gateway is not reachable from the browser.
	if got := net.Options[networkGatewayModeOption]; got != networkGatewayModeIsolated {
		t.Errorf("network Options[%s] = %q, want %q (options %v)", networkGatewayModeOption, got, networkGatewayModeIsolated, net.Options)
	}

	// pair.json holds the runtime with that subnet.
	_, rt, err := render.ReadPair(f.cfg, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if rt == nil || rt.Subnet != "10.200.2.0/24" || rt.NetworkID != net.ID {
		t.Errorf("runtime = %+v, want subnet 10.200.2.0/24 and network id %s", rt, net.ID)
	}

	// The health probe went to the proxy's address read back from
	// ContainerInspect after ContainerStart: the desk connected while
	// running and took .2, the proxy got .3 when it started.
	insp, err := f.fake.ContainerInspect(context.Background(), "desk-proxy-alice")
	if err != nil {
		t.Fatal(err)
	}
	if insp.IPs["net-alice"] != "10.200.2.3" {
		t.Errorf("proxy IP on net-alice = %q, want 10.200.2.3", insp.IPs["net-alice"])
	}
	if len(probes) != 1 || probes[0] != "http://10.200.2.3:8081/healthz" {
		t.Errorf("probe urls = %v, want [http://10.200.2.3:8081/healthz]", probes)
	}
}

func TestStartBusyAtCap(t *testing.T) {
	f := newFixture(t, 1, "alice", "bob")
	ctx := context.Background()
	// bob's pair is fully running (both containers, F71).
	f.runningPair(t, "bob", "tiktok-1.14.3")
	before := len(f.fake.Calls)
	res, err := f.m.Start(ctx, "alice")
	if err != nil || res != Busy {
		t.Fatalf("Start = %v, %v; want Busy, nil", res, err)
	}
	for _, c := range f.fake.Calls[before:] {
		if strings.HasPrefix(c, "NetworkCreate") || strings.HasPrefix(c, "ContainerCreate") {
			t.Errorf("busy start made a create call: %v", f.fake.Calls[before:])
			break
		}
	}
}

func TestStartGoneWithoutLiveRowOrReady(t *testing.T) {
	f := newFixture(t, 4, "alice")
	ctx := context.Background()
	// No registry row for carol.
	if res, err := f.m.Start(ctx, "carol"); res != Gone || err != nil {
		t.Errorf("Start(carol) = %v, %v; want Gone, nil", res, err)
	}
	// Live row but provisioning not ready.
	if err := os.Remove(filepath.Join(f.cfg.DeskRoot, "state", "users", "alice", "provision.json")); err != nil {
		t.Fatal(err)
	}
	if res, err := f.m.Start(ctx, "alice"); res != Gone || err != nil {
		t.Errorf("Start(alice, not ready) = %v, %v; want Gone, nil", res, err)
	}
}

func TestStartConcurrentSameUserStartsOnce(t *testing.T) {
	f := newFixture(t, 4, "alice")
	const n = 10
	results := make([]StartResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = f.m.Start(context.Background(), "alice")
		}(i)
	}
	wg.Wait()
	creates := 0
	for _, c := range f.fake.Calls {
		if c == "NetworkCreate net-alice" {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("NetworkCreate net-alice count = %d, want 1", creates)
	}
	for i := range results {
		if results[i] != Started || errs[i] != nil {
			t.Errorf("goroutine %d = %v, %v; want Started, nil", i, results[i], errs[i])
		}
	}
}

func TestStartConcurrentTwoUsersOneSlot(t *testing.T) {
	f := newFixture(t, 1, "alice", "bob")
	f.m.Probe = func(context.Context, string) bool {
		time.Sleep(20 * time.Millisecond)
		return true
	}
	users := []string{"alice", "bob"}
	type outcome struct {
		res StartResult
		err error
	}
	outcomes := make([]outcome, len(users))
	var wg sync.WaitGroup
	for i := range users {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i].res, outcomes[i].err = f.m.Start(context.Background(), users[i])
		}(i)
	}
	wg.Wait()
	started, busy := 0, 0
	for _, o := range outcomes {
		if o.err != nil {
			t.Fatalf("Start err = %v", o.err)
		}
		switch o.res {
		case Started:
			started++
		case Busy:
			busy++
		default:
			t.Errorf("result = %v, want Started or Busy", o.res)
		}
	}
	if started != 1 || busy != 1 {
		t.Errorf("started = %d, busy = %d; want 1 and 1", started, busy)
	}
	creates := 0
	for _, c := range f.fake.Calls {
		if strings.HasPrefix(c, "NetworkCreate net-") {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("network creates = %d, want 1", creates)
	}
}

func TestStartRemovesExitedLeftover(t *testing.T) {
	f := newFixture(t, 4, "alice")
	ctx := context.Background()
	// An exited firefox and its network from an earlier session, with our labels.
	shared := map[string]string{"desk.owner": "desk", "desk.user": "alice", "desk.generation": "tiktok-1.14.3"}
	if _, err := f.fake.NetworkCreate(ctx, "net-alice", withRole(shared, "net"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.fake.ContainerCreate(ctx, docker.ContainerSpec{
		Name:      "desk-firefox-alice",
		Labels:    withRole(shared, "firefox"),
		Endpoints: []docker.Endpoint{{Network: "net-alice"}},
	}); err != nil {
		t.Fatal(err)
	}
	f.fake.SetExited("desk-firefox-alice", 1)

	res, err := f.m.Start(ctx, "alice")
	if err != nil || res != Started {
		t.Fatalf("Start = %v, %v; want Started, nil", res, err)
	}
	sawRemoveFF, sawRemoveNet := false, false
	for _, c := range f.fake.Calls {
		if c == "ContainerRemove desk-firefox-alice" {
			sawRemoveFF = true
		}
		if c == "NetworkRemove net-alice" {
			sawRemoveNet = true
		}
	}
	if !sawRemoveFF || !sawRemoveNet {
		t.Errorf("leftover removal missing (ff=%v net=%v); calls = %v", sawRemoveFF, sawRemoveNet, f.fake.Calls)
	}
	// A fresh pair exists and runs.
	if _, err := f.fake.NetworkInspect(ctx, "net-alice"); err != nil {
		t.Errorf("net-alice after start: %v", err)
	}
	insp, err := f.fake.ContainerInspect(ctx, "desk-firefox-alice")
	if err != nil || !insp.Running {
		t.Errorf("firefox after start: err = %v, running = %v", err, insp.Running)
	}
}

func TestStartRefusesForeignContainer(t *testing.T) {
	f := newFixture(t, 4, "alice")
	ctx := context.Background()
	// A desk-firefox-alice without the desk.owner label.
	if _, err := f.fake.ContainerCreate(ctx, docker.ContainerSpec{
		Name:   "desk-firefox-alice",
		Labels: map[string]string{"desk.user": "alice", "desk.role": "firefox"},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.m.Start(ctx, "alice")
	if res != StartFailed || err == nil {
		t.Fatalf("Start = %v, %v; want StartFailed with an error", res, err)
	}
	if !strings.Contains(err.Error(), "desk-firefox-alice") {
		t.Errorf("error %q does not name the object", err)
	}
	for _, c := range f.fake.Calls {
		if strings.HasPrefix(c, "ContainerRemove") || strings.HasPrefix(c, "NetworkRemove") {
			t.Errorf("refused start removed something: %v", f.fake.Calls)
			break
		}
	}
	if _, err := f.fake.ContainerInspect(ctx, "desk-firefox-alice"); err != nil {
		t.Errorf("foreign container was removed: %v", err)
	}
}

func TestStartFailureAfterNetworkCleansUp(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.fake.Fail["ContainerStart desk-proxy-alice"] = errors.New("boom")
	res, err := f.m.Start(context.Background(), "alice")
	if res != StartFailed || err == nil {
		t.Fatalf("Start = %v, %v; want StartFailed with an error", res, err)
	}
	f.gone(t, "alice")
	_, rt, rerr := render.ReadPair(f.cfg, "alice")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if rt != nil {
		t.Errorf("runtime after failed start = %+v, want nil", rt)
	}
}

func TestStartHealthTimeout(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.m.Probe = func(context.Context, string) bool { return false }
	res, err := f.m.Start(context.Background(), "alice")
	if res != StartFailed || err == nil {
		t.Fatalf("Start = %v, %v; want StartFailed with an error", res, err)
	}
	f.gone(t, "alice")
}

// assertRebuilt checks that exactly one network and two running containers of
// user exist and the desk is connected to the network exactly once.
func assertRebuilt(t *testing.T, f *fixture, user string) {
	t.Helper()
	ctx := context.Background()
	nets, err := f.fake.NetworkList(ctx, map[string]string{"desk.user": user})
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 {
		t.Errorf("networks for %s = %d, want exactly 1", user, len(nets))
	}
	ctrs, err := f.fake.ContainerList(ctx, map[string]string{"desk.user": user})
	if err != nil {
		t.Fatal(err)
	}
	if len(ctrs) != 2 {
		t.Errorf("containers for %s = %d, want exactly 2", user, len(ctrs))
	}
	for _, c := range ctrs {
		if !c.Running {
			t.Errorf("container %s is not running after the rebuild", c.Name)
		}
	}
	self, err := f.fake.ContainerInspect(ctx, "deskself")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range self.Networks {
		if n == "net-"+user {
			found = true
		}
	}
	if !found || self.IPs["net-"+user] == "" {
		t.Errorf("deskself not connected once to net-%s: networks = %v, ips = %v", user, self.Networks, self.IPs)
	}
}

func TestStartRebuildsAfterFirefoxDied(t *testing.T) {
	f := newFixture(t, 4, "alice")
	ctx := context.Background()
	if res, err := f.m.Start(ctx, "alice"); res != Started || err != nil {
		t.Fatalf("first Start = %v, %v; want Started, nil", res, err)
	}
	f.fake.SetExited("desk-firefox-alice", 1)
	// The desk is still attached to net-alice; the leftover path must run
	// the stop sequence (disconnect included) before rebuilding.
	res, err := f.m.Start(ctx, "alice")
	if res != Started || err != nil {
		t.Fatalf("second Start = %v, %v; want Started, nil", res, err)
	}
	assertRebuilt(t, f, "alice")
	connects, disconnects := 0, 0
	for _, c := range f.fake.Calls {
		if c == "NetworkConnect net-alice deskself" {
			connects++
		}
		if c == "NetworkDisconnect net-alice deskself" {
			disconnects++
		}
	}
	if connects != 2 || disconnects != 1 {
		t.Errorf("connects = %d, disconnects = %d; want 2 and 1 (leftover path disconnected before removing)", connects, disconnects)
	}
}

func TestStartRebuildsAfterProxyDied(t *testing.T) {
	f := newFixture(t, 4, "alice")
	ctx := context.Background()
	if res, err := f.m.Start(ctx, "alice"); res != Started || err != nil {
		t.Fatalf("first Start = %v, %v; want Started, nil", res, err)
	}
	f.fake.SetExited("desk-proxy-alice", 1)
	// A Firefox running beside an exited proxy is a leftover: the pair is
	// rebuilt whole (F71).
	res, err := f.m.Start(ctx, "alice")
	if res != Started || err != nil {
		t.Fatalf("second Start = %v, %v; want Started, nil", res, err)
	}
	assertRebuilt(t, f, "alice")
}

// ctxClient wraps the Fake and fails every call whose context is done, so the
// test can observe whether cleanup ran on the caller's (cancelled) ctx or on a
// fresh one.
type ctxClient struct{ docker.Client }

func (c ctxClient) NetworkCreate(ctx context.Context, name string, labels, opts map[string]string) (docker.Network, error) {
	if err := ctx.Err(); err != nil {
		return docker.Network{}, err
	}
	return c.Client.NetworkCreate(ctx, name, labels, opts)
}

func (c ctxClient) NetworkInspect(ctx context.Context, name string) (docker.Network, error) {
	if err := ctx.Err(); err != nil {
		return docker.Network{}, err
	}
	return c.Client.NetworkInspect(ctx, name)
}

func (c ctxClient) NetworkList(ctx context.Context, labels map[string]string) ([]docker.Network, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Client.NetworkList(ctx, labels)
}

func (c ctxClient) NetworkRemove(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Client.NetworkRemove(ctx, name)
}

func (c ctxClient) NetworkConnect(ctx context.Context, network, container string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Client.NetworkConnect(ctx, network, container)
}

func (c ctxClient) NetworkDisconnect(ctx context.Context, network, container string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Client.NetworkDisconnect(ctx, network, container)
}

func (c ctxClient) ContainerCreate(ctx context.Context, spec docker.ContainerSpec) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return c.Client.ContainerCreate(ctx, spec)
}

func (c ctxClient) ContainerStart(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Client.ContainerStart(ctx, name)
}

func (c ctxClient) ContainerStop(ctx context.Context, name string, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Client.ContainerStop(ctx, name, timeout)
}

func (c ctxClient) ContainerRemove(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Client.ContainerRemove(ctx, name)
}

func (c ctxClient) ContainerInspect(ctx context.Context, name string) (docker.Container, error) {
	if err := ctx.Err(); err != nil {
		return docker.Container{}, err
	}
	return c.Client.ContainerInspect(ctx, name)
}

func (c ctxClient) ContainerList(ctx context.Context, labels map[string]string) ([]docker.Container, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Client.ContainerList(ctx, labels)
}

func (c ctxClient) Exec(ctx context.Context, container string, cmd []string, env []string, stdin []byte) (docker.Exec, error) {
	if err := ctx.Err(); err != nil {
		return docker.Exec{}, err
	}
	return c.Client.Exec(ctx, container, cmd, env, stdin)
}

func TestStartCancelledCtxStillCleansUp(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.m.Docker = ctxClient{f.fake}
	ctx, cancel := context.WithCancel(context.Background())
	// The probe blocks until the caller's ctx is done, so the start fails
	// BECAUSE the ctx was cancelled.
	f.m.Probe = func(pctx context.Context, _ string) bool {
		<-pctx.Done()
		return false
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	res, err := f.m.Start(ctx, "alice")
	if res != StartFailed || err == nil {
		t.Fatalf("Start = %v, %v; want StartFailed with an error", res, err)
	}
	// Cleanup ran on a fresh context: nothing is left behind.
	f.gone(t, "alice")
	_, rt, rerr := render.ReadPair(f.cfg, "alice")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if rt != nil {
		t.Errorf("runtime after cancelled start = %+v, want nil", rt)
	}
}
