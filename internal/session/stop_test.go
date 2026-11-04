package session

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/render"
)

func TestStopRemovesExactlyThreeObjects(t *testing.T) {
	f := newFixture(t, 4, "alice")
	ctx := context.Background()
	if res, err := f.m.Start(ctx, "alice"); res != Started || err != nil {
		t.Fatalf("Start = %v, %v; want Started, nil", res, err)
	}

	before := len(f.fake.Calls)
	if err := f.m.Stop(ctx, "alice"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	want := []string{
		"NetworkInspect net-alice",
		"ContainerInspect desk-proxy-alice",
		"ContainerInspect desk-firefox-alice",
		"ContainerStop desk-firefox-alice",
		"ContainerStop desk-proxy-alice",
		"ContainerRemove desk-firefox-alice",
		"ContainerRemove desk-proxy-alice",
		"NetworkDisconnect net-alice deskself",
		"NetworkRemove net-alice",
	}
	if got := f.fake.Calls[before:]; !reflect.DeepEqual(got, want) {
		t.Errorf("stop calls =\n%v\nwant\n%v", got, want)
	}
	f.gone(t, "alice")
	_, rt, err := render.ReadPair(f.cfg, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if rt != nil {
		t.Errorf("runtime after stop = %+v, want nil", rt)
	}

	// A second Stop is a no-op returning nil: it records only the inspects,
	// no ContainerStop/ContainerRemove/NetworkRemove/NetworkDisconnect call.
	before2 := len(f.fake.Calls)
	if err := f.m.Stop(ctx, "alice"); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	for _, c := range f.fake.Calls[before2:] {
		if !strings.HasPrefix(c, "NetworkInspect") && !strings.HasPrefix(c, "ContainerInspect") {
			t.Errorf("second Stop made a non-inspect call %q: %v", c, f.fake.Calls[before2:])
		}
	}
	f.gone(t, "alice")
}

func TestStopRefusesForeignObjects(t *testing.T) {
	f := newFixture(t, 4, "alice")
	ctx := context.Background()
	// A desk-proxy-alice without the desk.owner label.
	if _, err := f.fake.ContainerCreate(ctx, docker.ContainerSpec{
		Name:   "desk-proxy-alice",
		Labels: map[string]string{"desk.user": "alice", "desk.role": "proxy"},
	}); err != nil {
		t.Fatal(err)
	}
	err := f.m.Stop(ctx, "alice")
	if err == nil || !strings.Contains(err.Error(), "desk-proxy-alice") {
		t.Fatalf("Stop err = %v; want an error naming desk-proxy-alice", err)
	}
	for _, c := range f.fake.Calls {
		if strings.HasPrefix(c, "ContainerStop") || strings.HasPrefix(c, "ContainerRemove") || strings.HasPrefix(c, "NetworkRemove") {
			t.Errorf("refused stop touched objects: %v", f.fake.Calls)
			break
		}
	}
	if _, err := f.fake.ContainerInspect(ctx, "desk-proxy-alice"); err != nil {
		t.Errorf("foreign container was removed: %v", err)
	}
}
