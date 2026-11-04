package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReconcileReconnectsRunningPair(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.runningPair(t, "alice", "tiktok-1.14.3")
	before := len(f.fake.Calls)
	if err := f.m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := f.fake.Calls[before:]
	connected := false
	for _, c := range calls {
		if c == "NetworkConnect net-alice deskself" {
			connected = true
		}
		if strings.HasPrefix(c, "ContainerCreate") || strings.HasPrefix(c, "ContainerStart") ||
			strings.HasPrefix(c, "NetworkCreate") || strings.HasPrefix(c, "ContainerRemove") ||
			strings.HasPrefix(c, "NetworkRemove") {
			t.Errorf("reconcile created/removed something: %v", calls)
		}
	}
	if !connected {
		t.Errorf("no NetworkConnect net-alice deskself in %v", calls)
	}
	if !strings.Contains(f.logBuf.String(), "adopted alice") {
		t.Errorf("log = %q, want it to contain %q", f.logBuf.String(), "adopted alice")
	}
	// WriteUserFiles ran for the live row.
	if _, err := os.Stat(filepath.Join(f.cfg.DeskRoot, "firefox", "alice", "policies.json")); err != nil {
		t.Errorf("policies.json after reconcile: %v", err)
	}
}

func TestReconcileStopsExitedPair(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.runningPair(t, "alice", "tiktok-1.14.3")
	f.fake.SetExited("desk-firefox-alice", 1)
	if err := f.m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.gone(t, "alice")
	if !strings.Contains(f.logBuf.String(), "stopped alice:") {
		t.Errorf("log = %q, want it to contain %q", f.logBuf.String(), "stopped alice:")
	}
}

func TestReconcileLogsOrphanUntouched(t *testing.T) {
	f := newFixture(t, 4, "alice") // mallory has no registry row
	f.runningPair(t, "mallory", "tiktok-1.14.3")
	if err := f.m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	log := f.logBuf.String()
	for _, want := range []string{"orphan:", "net-mallory", "desk-proxy-mallory", "desk-firefox-mallory"} {
		if !strings.Contains(log, want) {
			t.Errorf("log = %q, want it to contain %q", log, want)
		}
	}
	ctx := context.Background()
	if _, err := f.fake.NetworkInspect(ctx, "net-mallory"); err != nil {
		t.Errorf("orphan network touched: %v", err)
	}
	if _, err := f.fake.ContainerInspect(ctx, "desk-proxy-mallory"); err != nil {
		t.Errorf("orphan proxy touched: %v", err)
	}
	if _, err := f.fake.ContainerInspect(ctx, "desk-firefox-mallory"); err != nil {
		t.Errorf("orphan firefox touched: %v", err)
	}
}

func TestReconcileStartsNothing(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.runningPair(t, "alice", "tiktok-1.14.3")
	before := len(f.fake.Calls)
	if err := f.m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.fake.Calls[before:] {
		if strings.HasPrefix(c, "ContainerStart") || strings.HasPrefix(c, "ContainerCreate") || strings.HasPrefix(c, "NetworkCreate") {
			t.Errorf("reconcile started something: %v", f.fake.Calls[before:])
			break
		}
	}
}

func TestReconcileStopsStaleGeneration(t *testing.T) {
	f := newFixture(t, 4, "alice") // cfg generation is tiktok-1.14.3
	f.runningPair(t, "alice", "tiktok-1.14.2")
	if err := f.m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.gone(t, "alice")
	if !strings.Contains(f.logBuf.String(), "generation changed") {
		t.Errorf("log = %q, want it to contain %q", f.logBuf.String(), "generation changed")
	}
}
