package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// callsContain reports whether want appears in calls as an ordered subsequence.
func callsContain(calls []string, want []string) bool {
	i := 0
	for _, c := range calls {
		if i < len(want) && c == want[i] {
			i++
		}
	}
	return i == len(want)
}

func TestFakeLifecycle(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	net, err := f.NetworkCreate(ctx, "net-alice", map[string]string{"desk.user": "alice"})
	if err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	if net.Subnet != "10.200.1.0/24" {
		t.Fatalf("subnet = %q, want 10.200.1.0/24", net.Subnet)
	}
	insp, err := f.NetworkInspect(ctx, "net-alice")
	if err != nil {
		t.Fatalf("NetworkInspect: %v", err)
	}
	if insp.Subnet != "10.200.1.0/24" {
		t.Fatalf("inspect subnet = %q, want 10.200.1.0/24", insp.Subnet)
	}

	proxySpec := ContainerSpec{
		Name:      "desk-proxy-alice",
		Image:     "proxy:1",
		Labels:    map[string]string{"desk.user": "alice"},
		Endpoints: []Endpoint{{Network: "net-alice"}},
	}
	if _, err := f.ContainerCreate(ctx, proxySpec); err != nil {
		t.Fatalf("ContainerCreate proxy: %v", err)
	}
	firefoxSpec := ContainerSpec{
		Name:      "desk-firefox-alice",
		Image:     "firefox:1",
		Labels:    map[string]string{"desk.user": "alice"},
		Endpoints: []Endpoint{{Network: "net-alice"}},
	}
	if _, err := f.ContainerCreate(ctx, firefoxSpec); err != nil {
		t.Fatalf("ContainerCreate firefox: %v", err)
	}

	// Created-not-started containers are attached but have no address yet.
	created, err := f.ContainerInspect(ctx, "desk-firefox-alice")
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	if len(created.IPs) != 0 {
		t.Errorf("IPs of a created-not-started container = %v, want empty", created.IPs)
	}

	list, err := f.ContainerList(ctx, map[string]string{"desk.user": "alice"})
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ContainerList len = %d, want 2", len(list))
	}
	for _, c := range list {
		if c.ExitCode != 0 {
			t.Errorf("ContainerList ExitCode = %d, want 0 (the list API cannot report it)", c.ExitCode)
		}
	}

	// The daemon assigns addresses when the containers start.
	if err := f.ContainerStart(ctx, "desk-proxy-alice"); err != nil {
		t.Fatalf("ContainerStart proxy: %v", err)
	}
	if err := f.ContainerStart(ctx, "desk-firefox-alice"); err != nil {
		t.Fatalf("ContainerStart firefox: %v", err)
	}
	ips := map[string]string{}
	for _, name := range []string{"desk-proxy-alice", "desk-firefox-alice"} {
		c, err := f.ContainerInspect(ctx, name)
		if err != nil {
			t.Fatalf("ContainerInspect %s: %v", name, err)
		}
		if !c.Running {
			t.Errorf("%s not running after start", name)
		}
		ips[name] = c.IPs["net-alice"]
	}
	if ips["desk-proxy-alice"] != "10.200.1.2" {
		t.Errorf("proxy IP = %q, want 10.200.1.2", ips["desk-proxy-alice"])
	}
	if ips["desk-firefox-alice"] != "10.200.1.3" {
		t.Errorf("firefox IP = %q, want 10.200.1.3", ips["desk-firefox-alice"])
	}

	// A third container connected while running gets the next address.
	extraSpec := ContainerSpec{Name: "desk-extra-alice", Image: "extra:1"}
	if _, err := f.ContainerCreate(ctx, extraSpec); err != nil {
		t.Fatalf("ContainerCreate extra: %v", err)
	}
	if err := f.ContainerStart(ctx, "desk-extra-alice"); err != nil {
		t.Fatalf("ContainerStart extra: %v", err)
	}
	if err := f.NetworkConnect(ctx, "net-alice", "desk-extra-alice"); err != nil {
		t.Fatalf("NetworkConnect extra: %v", err)
	}
	extra, err := f.ContainerInspect(ctx, "desk-extra-alice")
	if err != nil {
		t.Fatalf("ContainerInspect extra: %v", err)
	}
	if extra.IPs["net-alice"] != "10.200.1.4" {
		t.Errorf("extra IP = %q, want 10.200.1.4", extra.IPs["net-alice"])
	}

	// The created specs are recorded for later assertions.
	if len(f.Created) != 3 {
		t.Errorf("len(Created) = %d, want 3", len(f.Created))
	}
	if got := f.Specs["desk-proxy-alice"]; got.Image != "proxy:1" || len(got.Endpoints) != 1 || got.Endpoints[0].Network != "net-alice" {
		t.Errorf("Specs[desk-proxy-alice] = %+v, want the proxy spec", got)
	}

	if err := f.ContainerStop(ctx, "desk-proxy-alice", 5*time.Second); err != nil {
		t.Fatalf("ContainerStop: %v", err)
	}
	c, err := f.ContainerInspect(ctx, "desk-proxy-alice")
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	if c.Running {
		t.Error("container still running after stop")
	}
	if len(c.IPs) != 0 {
		t.Errorf("IPs after stop = %v, want empty (address released)", c.IPs)
	}
	if err := f.ContainerRemove(ctx, "desk-proxy-alice"); err != nil {
		t.Fatalf("ContainerRemove: %v", err)
	}
	want := []string{
		"ContainerStart desk-proxy-alice",
		"ContainerStop desk-proxy-alice",
		"ContainerRemove desk-proxy-alice",
	}
	if !callsContain(f.Calls, want) {
		t.Errorf("Calls = %v, want subsequence %v", f.Calls, want)
	}
	if _, ok := f.Specs["desk-proxy-alice"]; ok {
		t.Error("Specs still holds the removed container")
	}

	if _, err := f.ContainerCreate(ctx, firefoxSpec); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate ContainerCreate err = %v, want ErrConflict", err)
	}
	if _, err := f.ContainerInspect(ctx, "desk-proxy-alice"); !errors.Is(err, ErrNotFound) {
		t.Errorf("inspect after remove err = %v, want ErrNotFound", err)
	}

	if err := f.NetworkRemove(ctx, "net-alice"); !errors.Is(err, ErrConflict) {
		t.Errorf("NetworkRemove with attached container err = %v, want ErrConflict", err)
	}
	for _, name := range []string{"desk-firefox-alice", "desk-extra-alice"} {
		if err := f.ContainerRemove(ctx, name); err != nil {
			t.Fatalf("ContainerRemove %s: %v", name, err)
		}
	}
	if err := f.NetworkRemove(ctx, "net-alice"); err != nil {
		t.Fatalf("NetworkRemove after detach: %v", err)
	}
	if _, err := f.NetworkInspect(ctx, "net-alice"); !errors.Is(err, ErrNotFound) {
		t.Errorf("inspect after network remove err = %v, want ErrNotFound", err)
	}
}

func TestFakeInjectedFailure(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	if _, err := f.NetworkCreate(ctx, "net-alice", nil); err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	spec := ContainerSpec{
		Name:      "desk-firefox-alice",
		Image:     "firefox:1",
		Endpoints: []Endpoint{{Network: "net-alice"}},
	}
	if _, err := f.ContainerCreate(ctx, spec); err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}

	f.Fail["ContainerStart desk-firefox-alice"] = errors.New("boom")
	err := f.ContainerStart(ctx, "desk-firefox-alice")
	if err == nil || err.Error() != "boom" {
		t.Fatalf("ContainerStart err = %v, want boom", err)
	}
	c, err := f.ContainerInspect(ctx, "desk-firefox-alice")
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	if c.Running {
		t.Error("container running after a failed start")
	}
	found := false
	for _, call := range f.Calls {
		if call == "ContainerStart desk-firefox-alice" {
			found = true
		}
	}
	if !found {
		t.Errorf("Calls = %v, want ContainerStart desk-firefox-alice recorded", f.Calls)
	}
}

func TestFakeExecResults(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	spec := ContainerSpec{Name: "desk-proxy-alice", Image: "proxy:1"}
	if _, err := f.ContainerCreate(ctx, spec); err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	if err := f.ContainerStart(ctx, "desk-proxy-alice"); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}

	f.ExecResults["run-partner create"] = Exec{ExitCode: 3, Stdout: "out", Stderr: "err"}
	ex, err := f.Exec(ctx, "desk-proxy-alice", []string{"run-partner", "create", "acct"}, []string{"A=B"}, []byte("stdin"))
	if err != nil {
		t.Fatalf("Exec keyed: %v", err)
	}
	if ex.ExitCode != 3 || ex.Stdout != "out" || ex.Stderr != "err" {
		t.Errorf("keyed Exec = %+v, want {3 out err}", ex)
	}
	ex2, err := f.Exec(ctx, "desk-proxy-alice", []string{"other", "cmd"}, nil, nil)
	if err != nil {
		t.Fatalf("Exec unkeyed: %v", err)
	}
	if ex2.ExitCode != 0 {
		t.Errorf("unkeyed Exec exit = %d, want 0", ex2.ExitCode)
	}

	if len(f.Calls) < 2 {
		t.Fatalf("Calls = %v, want at least the two Exec calls", f.Calls)
	}
	last := f.Calls[len(f.Calls)-2:]
	want := []string{"Exec desk-proxy-alice run-partner create", "Exec desk-proxy-alice other cmd"}
	for i := range want {
		if last[i] != want[i] {
			t.Errorf("Calls tail = %v, want %v", last, want)
		}
	}
	if len(f.Execs) != 2 {
		t.Fatalf("Execs len = %d, want 2", len(f.Execs))
	}
	first := f.Execs[0]
	if first.Container != "desk-proxy-alice" ||
		strings.Join(first.Cmd, " ") != "run-partner create acct" ||
		len(first.Env) != 1 || first.Env[0] != "A=B" ||
		string(first.Stdin) != "stdin" {
		t.Errorf("Execs[0] = %+v, want full argv/env/stdin recorded", first)
	}
}

func TestFakeExecValidation(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	if _, err := f.Exec(ctx, "ghost", []string{"a", "b"}, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("Exec on a missing container err = %v, want ErrNotFound", err)
	}
	spec := ContainerSpec{Name: "c-alice", Image: "img:1"}
	if _, err := f.ContainerCreate(ctx, spec); err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	if _, err := f.Exec(ctx, "c-alice", []string{"a", "b"}, nil, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("Exec on a not-running container err = %v, want ErrConflict", err)
	}
	if len(f.Execs) != 2 {
		t.Errorf("Execs len = %d, want both calls recorded", len(f.Execs))
	}
}

func TestFakeHelpersFollowAddressRules(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	if _, err := f.NetworkCreate(ctx, "net-alice", nil); err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	specA := ContainerSpec{Name: "a", Image: "img:1", Endpoints: []Endpoint{{Network: "net-alice"}}}
	if _, err := f.ContainerCreate(ctx, specA); err != nil {
		t.Fatalf("ContainerCreate a: %v", err)
	}
	if err := f.ContainerStart(ctx, "a"); err != nil {
		t.Fatalf("ContainerStart a: %v", err)
	}
	c, err := f.ContainerInspect(ctx, "a")
	if err != nil {
		t.Fatalf("ContainerInspect a: %v", err)
	}
	if c.IPs["net-alice"] != "10.200.1.2" {
		t.Fatalf("a IP = %q, want 10.200.1.2", c.IPs["net-alice"])
	}

	// Exiting releases the address.
	f.SetExited("a", 7)
	c, err = f.ContainerInspect(ctx, "a")
	if err != nil {
		t.Fatalf("ContainerInspect a: %v", err)
	}
	if c.Running || c.ExitCode != 7 {
		t.Errorf("after SetExited: running=%v exit=%d, want false/7", c.Running, c.ExitCode)
	}
	if _, ok := c.IPs["net-alice"]; ok {
		t.Errorf("IPs after SetExited = %v, want no address for net-alice", c.IPs)
	}

	// Starting again gets a NEW number, never the old one.
	if err := f.ContainerStart(ctx, "a"); err != nil {
		t.Fatalf("ContainerStart a again: %v", err)
	}
	c, err = f.ContainerInspect(ctx, "a")
	if err != nil {
		t.Fatalf("ContainerInspect a: %v", err)
	}
	if c.IPs["net-alice"] != "10.200.1.3" {
		t.Errorf("a IP after restart = %q, want 10.200.1.3", c.IPs["net-alice"])
	}

	// MarkRunning assigns the next number on every attached network.
	specB := ContainerSpec{Name: "b", Image: "img:1", Endpoints: []Endpoint{{Network: "net-alice"}}}
	if _, err := f.ContainerCreate(ctx, specB); err != nil {
		t.Fatalf("ContainerCreate b: %v", err)
	}
	f.MarkRunning("b")
	c, err = f.ContainerInspect(ctx, "b")
	if err != nil {
		t.Fatalf("ContainerInspect b: %v", err)
	}
	if !c.Running {
		t.Error("b not running after MarkRunning")
	}
	if c.IPs["net-alice"] != "10.200.1.4" {
		t.Errorf("b IP = %q, want 10.200.1.4", c.IPs["net-alice"])
	}
}

func TestFakeNetworkConnectTwice(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	if _, err := f.NetworkCreate(ctx, "net-alice", nil); err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	spec := ContainerSpec{Name: "c-alice", Image: "img:1"}
	if _, err := f.ContainerCreate(ctx, spec); err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	if err := f.NetworkConnect(ctx, "net-alice", "c-alice"); err != nil {
		t.Fatalf("first NetworkConnect: %v", err)
	}
	err := f.NetworkConnect(ctx, "net-alice", "c-alice")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second NetworkConnect err = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "already exists in network") {
		t.Errorf("err = %q, want it to contain %q", err.Error(), "already exists in network")
	}
}
