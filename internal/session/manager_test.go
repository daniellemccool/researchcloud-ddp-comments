package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/locks"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/registry"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
)

// fixture is one test's world: a temp DeskRoot with a ready provision.json
// and pair.json per user, a live registry row per user, a Fake holding the
// shared egress bridge and the desk's own running container, and a Manager
// wired to all of it.
type fixture struct {
	m      *Manager
	fake   *docker.Fake
	cfg    *config.Config
	reg    *registry.Registry
	logBuf *bytes.Buffer
}

// newFixture builds the world for users (each gets a live row at index i+1).
// maxRunning is the desk's MaxRunning cap.
func newFixture(t *testing.T, maxRunning int, users ...string) *fixture {
	t.Helper()
	ctx := context.Background()
	cfg := &config.Config{
		DeskRoot:           t.TempDir(),
		HostDeskRoot:       t.TempDir(),
		FQDN:               "desk.example.org",
		Platform:           "tiktok",
		ZeeschuimerVersion: "1.14.3",
		FourcatEnabled:     false,
		DeskproxyImage:     "registry.example.org/deskproxy:1.2.3",
		FirefoxImage:       "registry.example.org/firefox:4.5.6",
		MaxSessions:        8,
		MaxRunning:         maxRunning,
	}
	fake := docker.NewFake()
	// The shared egress bridge the rendered proxy joins at create time; the
	// compose project owns it, so the fixture provides it (the Fake validates
	// every endpoint's network on ContainerCreate).
	if _, err := fake.NetworkCreate(ctx, "desk_desk-egress", nil, nil); err != nil {
		t.Fatal(err)
	}
	// The desk itself: a running container with no labels, so NetworkConnect
	// has something to connect.
	if _, err := fake.ContainerCreate(ctx, docker.ContainerSpec{Name: "deskself"}); err != nil {
		t.Fatal(err)
	}
	if err := fake.ContainerStart(ctx, "deskself"); err != nil {
		t.Fatal(err)
	}
	// Registry with one live row per user.
	reg := &registry.Registry{Path: filepath.Join(cfg.DeskRoot, "registry.tsv"), MaxSessions: cfg.MaxSessions}
	var sb strings.Builder
	for i, u := range users {
		sb.WriteString(u + "\t" + strconv.Itoa(i+1) + "\tlive\n")
	}
	if err := os.WriteFile(reg.Path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ready provision state plus pair.json per user (the provisioner's
	// render step wrote them).
	for i, u := range users {
		writeReady(t, cfg, u)
		if err := render.WriteUserFiles(cfg, render.Build(cfg, u, i+1)); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	m := &Manager{
		Cfg:        cfg,
		Docker:     fake,
		Reg:        reg,
		Locks:      &locks.Set{},
		SelfID:     "deskself",
		HealthWait: 50 * time.Millisecond,
		Probe:      func(context.Context, string) bool { return true },
		Log:        log.New(&buf, "", 0),
	}
	return &fixture{m: m, fake: fake, cfg: cfg, reg: reg, logBuf: &buf}
}

// writeReady writes a provision.json with every step done.
func writeReady(t *testing.T, cfg *config.Config, user string) {
	t.Helper()
	dir := filepath.Join(cfg.DeskRoot, "state", "users", user)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	steps := map[string]string{}
	for _, s := range provision.Steps {
		steps[s] = "done"
	}
	data, err := json.Marshal(provision.State{Steps: steps, AppliedGeneration: render.Generation(cfg), Updated: time.Now().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "provision.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// runningPair creates a running pair for user in the Fake, labelled with the
// given generation, without connecting the desk.
func (f *fixture) runningPair(t *testing.T, user, generation string) {
	t.Helper()
	ctx := context.Background()
	shared := map[string]string{"desk.owner": "desk", "desk.user": user, "desk.generation": generation}
	if _, err := f.fake.NetworkCreate(ctx, "net-"+user, withRole(shared, "net"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.fake.ContainerCreate(ctx, docker.ContainerSpec{
		Name:      "desk-proxy-" + user,
		Labels:    withRole(shared, "proxy"),
		Endpoints: []docker.Endpoint{{Network: "net-" + user}, {Network: "desk_desk-egress"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.fake.ContainerStart(ctx, "desk-proxy-"+user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.fake.ContainerCreate(ctx, docker.ContainerSpec{
		Name:      "desk-firefox-" + user,
		Labels:    withRole(shared, "firefox"),
		Endpoints: []docker.Endpoint{{Network: "net-" + user}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.fake.ContainerStart(ctx, "desk-firefox-"+user); err != nil {
		t.Fatal(err)
	}
}

// withRole copies labels and adds desk.role.
func withRole(labels map[string]string, role string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	out["desk.role"] = role
	return out
}

// startBare creates and starts a bare container with the given role labels
// (no endpoints), used to make Running() report a (partial) pair.
func (f *fixture) startBare(t *testing.T, name, role, user string) {
	t.Helper()
	ctx := context.Background()
	labels := withRole(map[string]string{"desk.owner": "desk", "desk.user": user, "desk.generation": "tiktok-1.14.3"}, role)
	if _, err := f.fake.ContainerCreate(ctx, docker.ContainerSpec{Name: name, Labels: labels}); err != nil {
		t.Fatal(err)
	}
	if err := f.fake.ContainerStart(ctx, name); err != nil {
		t.Fatal(err)
	}
}

// startFirefox creates and starts a bare firefox container for user.
func (f *fixture) startFirefox(t *testing.T, user string) {
	t.Helper()
	f.startBare(t, "desk-firefox-"+user, "firefox", user)
}

// gone asserts every object of user's pair is absent from the Fake.
func (f *fixture) gone(t *testing.T, user string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.fake.NetworkInspect(ctx, "net-"+user); !errors.Is(err, docker.ErrNotFound) {
		t.Errorf("net-%s still present: %v", user, err)
	}
	if _, err := f.fake.ContainerInspect(ctx, "desk-proxy-"+user); !errors.Is(err, docker.ErrNotFound) {
		t.Errorf("desk-proxy-%s still present: %v", user, err)
	}
	if _, err := f.fake.ContainerInspect(ctx, "desk-firefox-"+user); !errors.Is(err, docker.ErrNotFound) {
		t.Errorf("desk-firefox-%s still present: %v", user, err)
	}
}
