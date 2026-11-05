package config

import (
	"os"
	"path/filepath"
	"testing"
)

const gib = int64(1) << 30

func TestCapByMemoryVariants(t *testing.T) {
	tests := []struct {
		name       string
		maxRunning int
		memTotal   int64
		wantN      int
		wantCapped bool
	}{
		{"2 core 16 GB", 1, 16 * gib, 1, false},
		{"4 core 16 GB", 3, 16 * gib, 3, false},
		{"8 core 32 GB", 7, 32 * gib, 7, false},
		{"6 on 16 GB", 6, 16 * gib, 3, true},
		{"tiny box keeps one", 5, 2 * gib, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, capped := CapByMemory(tt.maxRunning, tt.memTotal)
			if n != tt.wantN || capped != tt.wantCapped {
				t.Errorf("CapByMemory(%d, %d) = (%d, %v), want (%d, %v)",
					tt.maxRunning, tt.memTotal, n, capped, tt.wantN, tt.wantCapped)
			}
		})
	}
}

func TestReadMemTotal(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "meminfo")
	if err := os.WriteFile(good, []byte("MemFree:         1000 kB\nMemTotal:       16384000 kB\nSwapTotal: 0 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMemTotal(good)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(16384000) * 1024; got != want {
		t.Errorf("ReadMemTotal = %d, want %d", got, want)
	}

	bad := filepath.Join(dir, "nomem")
	if err := os.WriteFile(bad, []byte("MemFree: 1000 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMemTotal(bad); err == nil {
		t.Error("ReadMemTotal without a MemTotal line = nil error, want error")
	}
	if _, err := ReadMemTotal(filepath.Join(dir, "absent")); err == nil {
		t.Error("ReadMemTotal of a missing file = nil error, want error")
	}
}
